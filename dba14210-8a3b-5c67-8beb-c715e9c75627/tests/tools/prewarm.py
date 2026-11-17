#!/usr/bin/env python3
"""Build-time GOCACHE pre-warm at the one fixed path grading uses.

Runs in the prewarm build stage only; it never reaches a shipped layer.

The go command folds a package's absolute source directory into its build
action ID when -trimpath is unset (cmd/go/internal/work/exec.go, `fmt.Fprintf(h,
"dir %s\\n", p.Dir)`), so a cache warmed at any other path is dead weight. The
module is therefore assembled at GRADE_MODULE_DIR -- the single constant
tests/Dockerfile sets once and both this script and grade.py read -- and
GOFLAGS, CGO_ENABLED and GOOS/GOARCH must match grade.py's too. The path is
recorded in WARM_PATH_NAME inside the cache so grade.py can refuse to grade at a
path the cache was not warmed at instead of silently recompiling the closure.

Two passes warm the two halves of the closure once each:

  1. the buggy base with the package's own tests removed, built the way grading
     builds a candidate, which keys every non-test vendored package;
  2. the base with its own test sources restored, run with -run=^$, which keys
     the test-only vendored packages (fake clientsets, testify) that no non-test
     build reaches.

The gold tree is then built so the two candidates the gate itself depends on are
warm. Each candidate's own internal/kube object still recompiles at grading time
because its source bytes differ; that is one small package, not the closure.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import tarfile
import time
from pathlib import Path

WARM_PATH_NAME = ".warm-path"

# Reward Kit imports every *.py under the tests root, so this file does no work at import time.


def root() -> Path:
    return Path(os.environ.get("PREWARM_ROOT", "/prewarm"))


def go_bin() -> str:
    return os.environ.get("PREWARM_GO", "/usr/local/go/bin/go")


def module_dir() -> Path:
    return Path(os.environ["GRADE_MODULE_DIR"])


def gocache() -> Path:
    return Path(os.environ["GOCACHE"])


def extract(archive: Path, dest: Path) -> None:
    with tarfile.open(archive, "r:*") as tar:
        for member in tar.getmembers():
            name = member.name.lstrip("./")
            if not name or name.startswith("/") or ".." in name.split("/"):
                raise ValueError(f"unsafe archive member {member.name!r}")
            if member.issym() or member.islnk():
                raise ValueError(f"link member refused: {member.name!r}")
        tar.extractall(dest)  # noqa: S202 - members validated above


def writable_tree(root: Path) -> None:
    root.chmod(0o755)
    for path in root.rglob("*"):
        path.chmod(0o755 if path.is_dir() else 0o644)


def apply_candidate(cand_dir: Path, module: Path) -> None:
    for path in sorted(cand_dir.rglob("*")):
        if not path.is_file() or path.name == "files.json":
            continue
        dst = module / path.relative_to(cand_dir)
        dst.parent.mkdir(parents=True, exist_ok=True)
        if dst.exists():
            dst.unlink()
        dst.write_bytes(path.read_bytes())
        dst.chmod(0o644)


def seal_readonly(cache: Path, warmed_at: Path) -> None:
    """Make the warmed cache root-owned and immutable to the test identity.

    grade.py hardlinks this tree per candidate (cp -al), so the hardlinks share
    these inodes and these modes: a submitted test running as uid 20002 can add
    new entries to its own snapshot's directories but cannot rewrite a single
    warmed object, and so cannot change what any other candidate compiles
    against. Deliberately applied after the last go invocation, because go needs
    to write here while warming.
    """
    (cache / WARM_PATH_NAME).write_text(f"{warmed_at}\n", encoding="utf-8")
    for path in sorted(cache.rglob("*"), reverse=True):
        if path.is_symlink():
            continue
        os.chown(path, 0, 0)
        path.chmod(0o555 if path.is_dir() else 0o444)
    os.chown(cache, 0, 0)
    cache.chmod(0o555)


def go(args: list[str], label: str, cwd: Path) -> None:
    started = time.time()
    proc = subprocess.run([go_bin(), *args], cwd=cwd, env=dict(os.environ), check=False)
    print(f"{label}: go {' '.join(args)} -> {proc.returncode} in {time.time() - started:.1f}s", flush=True)
    if proc.returncode != 0:
        raise SystemExit(f"prewarm pass {label!r} failed")


def main() -> int:
    prewarm_root = root()
    module = module_dir()
    cache = gocache()
    manifest = json.loads((prewarm_root / "candidates" / "manifest.json").read_text())
    module_rel = manifest["module_relpath"]
    package = manifest["package_relpath"]
    base = prewarm_root / "base"
    extract(prewarm_root / "base-module.tar.gz", base)

    module.parent.mkdir(parents=True, exist_ok=True)
    if module.exists():
        shutil.rmtree(module)
    shutil.copytree(base / module_rel, module)
    writable_tree(module)

    saved = {
        path.name: path.read_bytes()
        for path in sorted((module / package).glob("*_test.go"))
    }

    build_target = f"./{package}/..."

    apply_candidate(prewarm_root / "candidates" / manifest["buggy_id"], module)
    for path in sorted((module / package).glob("*_test.go")):
        path.unlink()
    go(["build", build_target], "buggy-no-tests", module)

    for name, data in saved.items():
        (module / package / name).write_bytes(data)
        (module / package / name).chmod(0o644)
    go(["test", "-count=1", "-run=^$", build_target], "buggy-own-tests", module)

    apply_candidate(prewarm_root / "candidates" / manifest["gold_id"], module)
    for path in sorted((module / package).glob("*_test.go")):
        path.unlink()
    go(["build", build_target], "gold-no-tests", module)

    shutil.rmtree(module)
    shutil.rmtree(base, ignore_errors=True)
    seal_readonly(cache, module)
    files = [p for p in cache.rglob("*") if p.is_file()]
    size = sum(p.stat().st_size for p in files)
    print(
        f"prewarm: GOCACHE {size / (1 << 20):.0f} MiB in {len(files)} files, "
        f"warmed at {module}, sealed root-owned read-only",
        flush=True,
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
