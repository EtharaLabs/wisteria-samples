#!/usr/bin/env python3
"""Offline differential grader for the collected Go test submission.

Reads the collected submission files and the sealed candidate set baked into the
verifier image, runs the submission against every candidate twice, and writes a
verdict matrix plus a CTRF report. Never fetches anything.

Imported by Reward Kit's discovery pass (it globs ``*.py`` at the tests root),
so module import registers no criterion and performs no work.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import signal
import stat
import subprocess
import sys
import tarfile
import tempfile
import time
from pathlib import Path

from isolation import blinded_order, isolated_run_paths, private_directory
from outcomes import ParsedRun, accepts_correct_candidate, combine_runs, ctrf_status, parse_run, witness_names

SCHEMA = "kube-testgen.verdict-matrix/v1"
CTRF_SPEC = "https://ctrf.io/schema/v0.0.0"

PACKAGE_RELPATH = "internal/kube"
MODULE_RELPATH = "processor/k8sattributesprocessor"
PACKAGE_SUFFIX = f"{MODULE_RELPATH}/{PACKAGE_RELPATH}"

TESTS_ROOT = Path(os.environ.get("GRADE_TESTS_ROOT", "/tests"))
OUT_DIR = Path(os.environ.get("GRADE_OUT_DIR", "/logs/verifier"))
ARTIFACTS_ROOT = Path(os.environ.get("GRADE_ARTIFACTS_ROOT", "/logs/artifacts"))

# Every candidate is graded at this one path, which the build cache was warmed at; a per-candidate
# path would recompile the whole vendored closure for each candidate.
MODULE_DIR = Path(os.environ.get("GRADE_MODULE_DIR", "/work/grade/module"))

# OUT_DIR holds the root-only grading artifacts.
REWARD_DIR = Path(os.environ.get("GRADE_REWARD_DIR", "/logs/verifier"))
MATRIX_NAME = "verdict-matrix.json"
CTRF_NAME = "ctrf.json"
REASON_NAME = "reason.json"
REWARD_NAMES = ("reward.json", "reward.txt")

# prewarm.py records the path it warmed the cache at; a mismatch is fail-closed.
WARM_PATH_NAME = ".warm-path"

# test.sh treats this exit code as "the grader refused and wrote its own
# reason.json", leaving the {"reward": 0} sentinel in place.
EXIT_FAIL_CLOSED = 3

# Red line identifiers. Every one of them zeroes the reward.
RL_GIT = "RL-GIT"
RL_NON_TEST = "RL-NON-TEST"
RL_PREBUILT = "RL-PREBUILT"
RL_OUTSIDE = "RL-OUTSIDE-PACKAGE"
RL_SEAL = "RL-SEAL-TAMPER"
RL_REWARD_WRITE = "RL-REWARD-WRITE"

# RL-GIT and RL-PREBUILT match rules in instruction.md. RL-SEAL-TAMPER and RL-REWARD-WRITE are
# runtime integrity checks: a sealed grader file, or a reward file, that changes while grading runs.
# No submitted file is scanned for the reward path. Non-test files and paths outside the directory
# are ignored, not zeroed.
RED_LINE_IDS = (RL_GIT, RL_PREBUILT, RL_SEAL, RL_REWARD_WRITE)

PREBUILT_SUFFIXES = (".test", ".a", ".o", ".so", ".exe", ".syso", ".wasm")
# A Go coverage profile, which instruction.md names, always opens with this.
COVERAGE_MAGICS = (b"mode: set", b"mode: count", b"mode: atomic")
BINARY_MAGICS = (
    b"\x7fELF",
    b"MZ",
    b"\xca\xfe\xba\xbe",
    b"\xcf\xfa\xed\xfe",
    b"\xce\xfa\xed\xfe",
    b"!<arch>\n",
    b"PK\x03\x04",
    b"\x1f\x8b",
    b"\xfd7zXZ\x00",
    b"BZh",
    b"\x28\xb5\x2f\xfd",
    b"7z\xbc\xaf\x27\x1c",
)
# A POSIX or GNU tar archive carries no magic at offset 0; its header names it at 257.
TAR_MAGIC_OFFSET = 257
TAR_MAGIC = b"ustar"

# --------------------------------------------------------------------------
# digests


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def tree_digest(root: Path) -> str:
    """Path-ordered digest over every regular file of a tree."""
    h = hashlib.sha256()
    for path in sorted(p for p in root.rglob("*") if p.is_file() and not p.is_symlink()):
        rel = path.relative_to(root).as_posix()
        h.update(rel.encode())
        h.update(b"\0")
        h.update(sha256_file(path).encode())
        h.update(b"\0")
    return h.hexdigest()


# --------------------------------------------------------------------------
# submission collection


def resolve_submission(explicit: str | None) -> Path | None:
    if explicit:
        return Path(explicit)
    env = os.environ.get("GRADE_SUBMISSION_DIR")
    if env:
        return Path(env)
    if not ARTIFACTS_ROOT.is_dir():
        return None
    wanted = PACKAGE_SUFFIX.split("/")
    best: Path | None = None
    for cand in sorted(p for p in ARTIFACTS_ROOT.rglob("kube") if p.is_dir()):
        parts = cand.parts
        if len(parts) >= len(wanted) and list(parts[-len(wanted) :]) == wanted:
            best = cand
            break
    return best if best is not None else ARTIFACTS_ROOT


def collect_submission(root: Path) -> tuple[list[tuple[str, Path]], list[dict]]:
    """Return (accepted files as (relpath, abspath)), and red-line findings."""
    findings: list[dict] = []
    accepted: list[tuple[str, Path]] = []
    if not root.is_dir():
        return accepted, findings
    real_root = root.resolve()
    for path in sorted(root.rglob("*")):
        rel = path.relative_to(root).as_posix()
        parts = rel.split("/")
        # Only an exact .git path component counts; .gitignore and .gitkeep are ordinary files.
        if any(p == ".git" for p in parts):
            findings.append({"id": RL_GIT, "path": rel})
            continue
        # Symlinks leaving the directory, subdirectories and non-regular files are ignored, not zeroed.
        if path.is_symlink():
            target = (path.parent / os.readlink(path)).resolve()
            if not str(target).startswith(str(real_root) + os.sep):
                continue
        if path.is_dir():
            continue
        if not path.is_file():
            continue
        if path.suffix in PREBUILT_SUFFIXES:
            findings.append({"id": RL_PREBUILT, "path": rel, "detail": f"prebuilt or generated binary {path.suffix}"})
            continue
        head = path.open("rb").read(TAR_MAGIC_OFFSET + len(TAR_MAGIC))
        if any(head.startswith(m) for m in COVERAGE_MAGICS):
            findings.append({"id": RL_PREBUILT, "path": rel, "detail": "coverage profile"})
            continue
        if any(head.startswith(m) for m in BINARY_MAGICS) or head[TAR_MAGIC_OFFSET:] == TAR_MAGIC:
            findings.append({"id": RL_PREBUILT, "path": rel, "detail": "binary magic in a submitted file"})
            continue
        if path.stat().st_mode & (stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH):
            findings.append({"id": RL_PREBUILT, "path": rel, "detail": "executable bit set on a submitted file"})
            continue
        in_testdata = parts[0] == "testdata"
        is_test_go = len(parts) == 1 and parts[0].endswith("_test.go")
        if not (in_testdata or is_test_go):
            continue
        accepted.append((rel, path))
    return accepted, findings


def collected_tests(files: list[tuple[str, Path]]) -> list[str]:
    paths = [str(path) for rel, path in files if rel.endswith("_test.go") and "/" not in rel]
    if not paths:
        return []
    proc = subprocess.run(
        [os.environ.get("GRADE_DISCOVER", "/usr/local/bin/go-test-discover")],
        input=json.dumps(paths), text=True, stdout=subprocess.PIPE,
        stderr=subprocess.PIPE, timeout=30, check=True,
    )
    names: list[str] = json.loads(proc.stdout)
    return names


# --------------------------------------------------------------------------
# candidate trees


def load_manifest() -> dict:
    path = TESTS_ROOT / "candidates" / "manifest.json"
    return json.loads(path.read_text())


def link_copy(src: Path, dst: Path) -> None:
    """Recursive copy that hardlinks regular files (the base tree is read-only)."""
    dst.mkdir(parents=True, exist_ok=True)
    for entry in sorted(src.iterdir()):
        target = dst / entry.name
        if entry.is_dir() and not entry.is_symlink():
            link_copy(entry, target)
        elif entry.is_symlink():
            os.symlink(os.readlink(entry), target)
        else:
            try:
                os.link(entry, target)
            except OSError:
                shutil.copy2(entry, target)


def materialize_base(archive: Path, dest: Path) -> None:
    dest.mkdir(parents=True, exist_ok=True)
    with tarfile.open(archive, "r:*") as tar:
        for member in tar.getmembers():
            name = member.name.lstrip("./")
            if not name or name.startswith("/") or ".." in name.split("/"):
                raise ValueError(f"unsafe archive member {member.name!r}")
            if member.issym() or member.islnk():
                raise ValueError(f"link member refused: {member.name!r}")
        tar.extractall(dest)  # noqa: S202 - members validated above


def write_file(dst: Path, data: bytes) -> None:
    dst.parent.mkdir(parents=True, exist_ok=True)
    if dst.exists() or dst.is_symlink():
        dst.unlink()
    dst.write_bytes(data)
    dst.chmod(0o444)


def apply_candidate(candidate_dir: Path, module_root: Path) -> list[str]:
    applied = []
    for path in sorted(p for p in candidate_dir.rglob("*") if p.is_file()):
        rel = path.relative_to(candidate_dir).as_posix()
        if rel == "files.json":
            continue
        write_file(module_root / rel, path.read_bytes())
        applied.append(rel)
    return applied


def mutable_relpaths(cand_root: Path, candidate_order: list[str]) -> list[str]:
    """Every module-relative path grading can touch, for the pristine reset."""
    rels = {PACKAGE_RELPATH}
    for cid in candidate_order:
        cdir = cand_root / cid
        for path in cdir.rglob("*"):
            if path.is_file() and path.name != "files.json":
                rels.add(path.relative_to(cdir).as_posix())
    return sorted(rels)


def drop(path: Path) -> None:
    if path.is_dir() and not path.is_symlink():
        shutil.rmtree(path)
    elif path.exists() or path.is_symlink():
        path.unlink()


def reset_module(base_module: Path, module_root: Path, rels: list[str]) -> None:
    """Return the fixed module tree to pristine base bytes.

    Restores only the paths a candidate or a submission overlay can mutate, so
    the ~68 MB vendor tree is never rebuilt and its package directories keep
    both their contents and their path across candidates.
    """
    for rel in rels:
        dst = module_root / rel
        src = base_module / rel
        drop(dst)
        if src.is_dir() and not src.is_symlink():
            link_copy(src, dst)
        elif src.is_file():
            dst.parent.mkdir(parents=True, exist_ok=True)
            try:
                os.link(src, dst)
            except OSError:
                shutil.copy2(src, dst)


def overlay_submission(module_root: Path, files: list[tuple[str, Path]]) -> None:
    pkg = module_root / PACKAGE_RELPATH
    for existing in sorted(pkg.glob("*_test.go")):
        existing.unlink()
    testdata = pkg / "testdata"
    if testdata.exists():
        shutil.rmtree(testdata)
    for rel, path in files:
        write_file(pkg / rel, path.read_bytes())


# --------------------------------------------------------------------------
# go test


def test_identity() -> tuple[int, int]:
    """The unprivileged uid/gid every go test child runs as.

    Mandatory: the verifier entry point runs as uid 0 (task.toml [verifier]
    user = "root") precisely so that submitted test code never executes with the
    grader's own privileges. A grader that cannot drop refuses to run rather
    than running test code beside its own verdict matrix.
    """
    uid = int(os.environ.get("GRADE_TEST_UID", "20002"))
    gid = int(os.environ.get("GRADE_TEST_GID", str(uid)))
    if uid == 0 or gid == 0:
        raise SystemExit("GRADE_TEST_UID/GID must name an unprivileged identity")
    if os.geteuid() != 0:
        raise SystemExit(
            "grade.py must run as uid 0 so every go test child can drop to "
            f"{uid}:{gid}; euid is {os.geteuid()}"
        )
    return uid, gid


def own_scratch(path: Path, uid: int, gid: int, mode: int = 0o700) -> None:
    """Hand one scratch directory to the unprivileged test identity."""
    path.mkdir(parents=True, exist_ok=True)
    os.chown(path, uid, gid)
    os.chmod(path, mode)


def go_env(scratch: Path, gocache: Path) -> dict[str, str]:
    """The one environment both the prewarm stage and every grading run use.

    GOFLAGS, CGO_ENABLED, GOOS/GOARCH (inherited, never overridden) and the
    module path must be byte-identical to the prewarm stage's or no warmed
    action ID matches.
    """
    return {
        "PATH": os.environ.get("GRADE_PATH", os.environ.get("PATH", "/usr/local/go/bin:/usr/bin:/bin")),
        "HOME": str(scratch / "home"),
        "TMPDIR": str(scratch / "tmp"),
        "GOFLAGS": "-mod=vendor",
        "GOPROXY": "off",
        "GOTOOLCHAIN": "local",
        "GONOSUMDB": "*",
        "GOCACHE": str(gocache),
        "GOMODCACHE": str(scratch / "gomodcache"),
        "GOPATH": str(scratch / "gopath"),
        "GOWORK": "off",
        "CGO_ENABLED": "0",
        "LC_ALL": "C",
        "TZ": "UTC",
    }


def run_once(module_root: Path, scratch: Path, gocache: Path, timeout: int, uid: int, gid: int) -> dict:
    cmd = [
        os.environ.get("GRADE_GO", "go"),
        "test",
        "-count=1",
        "-json",
        f"./{PACKAGE_RELPATH}/...",
    ]
    env = go_env(scratch, gocache)

    # Only opaque per-run scratch/cache directories are handed to the child.
    # The pool, pristine base, original cache and grading outputs remain private.
    own_scratch(scratch, uid, gid)
    for key in ("home", "tmp", "gopath", "gomodcache"):
        own_scratch(scratch / key, uid, gid)
    own_scratch(gocache, uid, gid, mode=0o755)

    kwargs: dict = {"user": uid, "group": gid, "extra_groups": []}
    started = time.time()
    try:
        proc = subprocess.run(
            cmd,
            cwd=str(module_root),
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=timeout,
            check=False,
            **kwargs,
        )
        elapsed = time.time() - started
        return {
            "timeout": False,
            "exit_code": proc.returncode,
            "stdout": proc.stdout.decode("utf-8", "replace"),
            "stderr": proc.stderr.decode("utf-8", "replace"),
            "wall_seconds": round(elapsed, 3),
        }
    except subprocess.TimeoutExpired as exc:
        return {
            "timeout": True,
            "exit_code": None,
            "stdout": (exc.stdout or b"").decode("utf-8", "replace") if isinstance(exc.stdout, bytes) else (exc.stdout or ""),
            "stderr": (exc.stderr or b"").decode("utf-8", "replace") if isinstance(exc.stderr, bytes) else (exc.stderr or ""),
            "wall_seconds": round(time.time() - started, 3),
        }


# --------------------------------------------------------------------------
# main


def fail_closed(reason: str, detail: str) -> int:
    """Refuse to grade, leaving test.sh's {"reward": 0} sentinel untouched."""
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    (OUT_DIR / REASON_NAME).write_text(
        json.dumps({"reason": reason, "detail": detail}, sort_keys=True) + "\n"
    )
    print(f"grade.py: refusing to grade: {reason}: {detail}", file=sys.stderr)
    return EXIT_FAIL_CLOSED


def warm_path(gocache: Path) -> str | None:
    marker = gocache / WARM_PATH_NAME
    if not marker.is_file():
        return None
    return marker.read_text(encoding="utf-8", errors="replace").strip() or None


def snapshot_gocache(warm: Path, dest: Path, uid: int, gid: int) -> None:
    """Hardlink the read-only warm cache into a per-candidate GOCACHE.

    The hardlinks share the warmed files' inodes, so they keep prewarm's root
    ownership and 0444 mode and a submitted test cannot rewrite a single warmed
    object. Only the snapshot's *directories* are handed to the test identity,
    which is all go needs to add this candidate's own new entries. Deleting the
    snapshot afterwards drops only the extra links, so the warm tree and every
    other candidate are untouched.
    """
    drop(dest)
    dest.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(["cp", "-al", str(warm), str(dest)], check=True)
    os.chown(dest, uid, gid)
    os.chmod(dest, 0o755)
    for path in dest.rglob("*"):
        if path.is_dir() and not path.is_symlink():
            os.chown(path, uid, gid)
            os.chmod(path, 0o755)


def reap_uid(uid: int) -> list[int]:
    """End every process the test identity still owns, and say which.

    go test exiting does not mean its children did: a submitted test can leave a
    detached process behind, and Harbor reads the reward carrier only after
    test.sh exits, so anything still alive could rewrite it then.
    """
    killed: set[int] = set()
    for _ in range(5):
        alive: list[int] = []
        for entry in Path("/proc").iterdir():
            if not entry.name.isdigit() or int(entry.name) == os.getpid():
                continue
            try:
                if entry.stat().st_uid != uid:
                    continue
            except OSError:
                continue
            alive.append(int(entry.name))
        if not alive:
            break
        for pid in alive:
            try:
                os.kill(pid, signal.SIGKILL)
            except OSError:
                continue
            killed.add(pid)
        time.sleep(0.2)
    return sorted(killed)


def reward_sentinel() -> dict[str, str | None]:
    out = {}
    for name in REWARD_NAMES:
        path = REWARD_DIR / name
        out[name] = sha256_file(path) if path.is_file() else None
    return out


def grade(args: argparse.Namespace) -> int:
    manifest = load_manifest()
    test_uid, test_gid = test_identity()
    findings: list[dict] = []

    warm_gocache = Path(os.environ.get("GRADE_GOCACHE", "")) if os.environ.get("GRADE_GOCACHE") else None
    module_dir = Path(args.module_dir or os.environ.get("GRADE_MODULE_DIR", str(MODULE_DIR)))
    warmed_at = warm_path(warm_gocache) if warm_gocache else None
    if warmed_at is not None and warmed_at != str(module_dir):
        return fail_closed(
            "cache-path-mismatch",
            f"the image warmed {warm_gocache} at {warmed_at!r} but grading would run at "
            f"{str(module_dir)!r}; every vendored package would recompile",
        )

    if warm_gocache is not None:
        private_directory(warm_gocache)

    # Seal check before grading.
    cand_root = TESTS_ROOT / "candidates"
    archive = TESTS_ROOT / manifest["base_archive"]
    seal_before = {"base_archive": sha256_file(archive)}
    for cid in manifest["candidate_order"]:
        seal_before[cid] = tree_digest(cand_root / cid)
    for key, want in manifest["digests"].items():
        got = seal_before.get(key)
        if got != want:
            findings.append({"id": RL_SEAL, "path": key, "detail": f"expected {want}, found {got}", "phase": "before"})

    sub_root = resolve_submission(args.submission)
    sub_files: list[tuple[str, Path]] = []
    if sub_root is None or not sub_root.is_dir():
        sub_root_str = str(sub_root) if sub_root else None
    else:
        sub_root_str = str(sub_root)
        sub_files, sub_findings = collect_submission(sub_root)
        findings.extend(sub_findings)

    sentinel_before = reward_sentinel()
    try:
        tests = collected_tests(sub_files)
    except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired, json.JSONDecodeError) as exc:
        return fail_closed("test-discovery-failed", str(exc))

    scratch_root = Path(args.scratch or tempfile.mkdtemp(prefix="grade-", dir=os.environ.get("GRADE_SCRATCH_PARENT", tempfile.gettempdir())))
    scratch_root.mkdir(parents=True, exist_ok=True)
    # Allow traversal only; each writable leaf is chowned to the test user in run_once.
    scratch_root.chmod(0o755)
    base_dir = scratch_root / "base"
    materialize_base(archive, base_dir)
    private_directory(base_dir)
    module_src = base_dir / MODULE_RELPATH
    if not (module_src / "go.mod").is_file():
        raise SystemExit(f"base archive has no module at {MODULE_RELPATH}")

    gocache = warm_gocache if warm_gocache is not None else scratch_root / "gocache"
    drop(module_dir)
    for parent in reversed(module_dir.parents):
        if not parent.exists():
            parent.mkdir()
            parent.chmod(0o755)
    link_copy(module_src, module_dir)
    module_dir.chmod(0o755)
    mutable = mutable_relpaths(cand_root, list(manifest["candidate_order"]))

    candidates: dict[str, dict] = {}
    timeout = int(os.environ.get("GRADE_RUN_TIMEOUT_SEC", str(args.run_timeout)))
    execution_order = blinded_order(manifest["candidate_order"])

    for cid in execution_order:
        spec = manifest["candidates"][cid]
        work = module_dir
        reset_module(module_src, work, mutable)
        applied = apply_candidate(cand_root / cid, work)
        entry: dict = {
            "class": spec["class"],
            "applied_files": applied,
            "runs": [],
            "results": {},
            "executed_tests": [],
            "suite_passed": False,
            "suite_error": False,
            "compile_error": False,
            "timeout": False,
            "disagreements": [],
            "wall_seconds": [],
        }
        if tests:
            overlay_submission(work, sub_files)
            parsed: list[ParsedRun] = []
            snapshot_new_files = 0
            reaped: list[int] = []
            for run_index in range(2):
                # Each run gets its own scratch tree and cache snapshot so the two runs stay independent.
                run_scratch, cand_cache = isolated_run_paths(scratch_root)
                snapshot_gocache(gocache, cand_cache, test_uid, test_gid)
                raw = run_once(work, run_scratch, cand_cache, timeout, test_uid, test_gid)
                reaped.extend(reap_uid(test_uid))
                snapshot_new_files += sum(
                    1 for q in cand_cache.rglob("*") if q.is_file() and q.stat().st_nlink == 1
                )
                drop(cand_cache)
                drop(run_scratch)
                p = parse_run(raw, tests)
                parsed.append(p)
                entry["wall_seconds"].append(p["wall_seconds"])
                entry["runs"].append(
                    {
                        "build_failed": p["build_failed"],
                        "timeout": p["timeout"],
                        "exit_code": p["exit_code"],
                        "wall_seconds": p["wall_seconds"],
                        "verdicts": p["verdicts"],
                        "executed_tests": p["executed_tests"],
                        "suite_passed": p["suite_passed"],
                        "suite_error": p["suite_error"],
                    }
                )
                if sentinel_before != reward_sentinel():
                    findings.append({"id": RL_REWARD_WRITE, "path": str(REWARD_DIR), "detail": "a reward file changed while a candidate ran"})
                    sentinel_before = reward_sentinel()
            entry["compile_error"] = any(p["build_failed"] for p in parsed)
            entry["timeout"] = any(p["timeout"] for p in parsed)
            entry.update(combine_runs(parsed, tests))
            for name in tests:
                a = parsed[0]["verdicts"][name]
                b = parsed[1]["verdicts"][name]
                if a != b:
                    entry["disagreements"].append(name)
            entry["gocache_snapshot_new_files"] = snapshot_new_files
            entry["reaped_pids"] = sorted(set(reaped))
        candidates[cid] = entry

    # No submitted process may outlive grading; reap, then make the carrier directory root-only.
    final_reaped = reap_uid(test_uid)
    if final_reaped:
        print(f"reaped after the last run: {final_reaped}", flush=True)
    try:
        os.chown(REWARD_DIR, 0, 0)
        os.chmod(REWARD_DIR, 0o755)
    except OSError as exc:
        print(f"could not lock {REWARD_DIR}: {exc}", flush=True)

    if not args.keep_work:
        drop(module_dir)

    seal_after = {"base_archive": sha256_file(archive)}
    for cid in manifest["candidate_order"]:
        seal_after[cid] = tree_digest(cand_root / cid)
    for key, want in seal_before.items():
        if seal_after.get(key) != want:
            findings.append({"id": RL_SEAL, "path": key, "detail": "digest changed after grading", "phase": "after"})
    if sentinel_before != reward_sentinel():
        findings.append({"id": RL_REWARD_WRITE, "path": str(REWARD_DIR), "detail": "a reward file changed during grading"})

    gold_id = manifest["gold_id"]
    buggy_id = manifest["buggy_id"]
    witnesses = witness_names(candidates[buggy_id], candidates[gold_id], tests)

    fired = sorted({f["id"] for f in findings})
    matrix = {
        "schema": SCHEMA,
        "generated_by": "tests/grade.py",
        "submission_root": sub_root_str,
        "collected_files": [rel for rel, _ in sub_files],
        "collected_tests": tests,
        "candidate_order": list(manifest["candidate_order"]),
        "execution_order": execution_order,
        "buggy_id": buggy_id,
        "gold_id": gold_id,
        "correct_ids": [c for c in manifest["candidate_order"] if manifest["candidates"][c]["class"] == "correct"],
        "incorrect_ids": [c for c in manifest["candidate_order"] if manifest["candidates"][c]["class"] == "incorrect"],
        "candidates": candidates,
        "gate": {
            "holds": bool(witnesses) and accepts_correct_candidate(candidates[gold_id], tests) and not fired,
            "witnesses": witnesses,
        },
        "red_lines": {
            "ids": list(RED_LINE_IDS),
            "fired": fired,
            "findings": findings,
            "any_fired": bool(fired),
        },
        "seal": {"before": seal_before, "after": seal_after},
        "run_timeout_sec": timeout,
        "sandbox": {
            "grader_euid": os.geteuid(),
            "test_uid": test_uid,
            "test_gid": test_gid,
            "runs_per_candidate": 2,
            "module_dir": str(module_dir),
            "gocache": str(gocache),
            "gocache_warmed_at": warmed_at,
            "gocache_per_candidate_snapshot": True,
            "candidate_identity_in_paths": False,
            "private_grader_assets": True,
            "execution_order_blinded": True,
            "goflags": "-mod=vendor",
            "cgo_enabled": "0",
        },
    }

    OUT_DIR.mkdir(parents=True, exist_ok=True)
    (OUT_DIR / MATRIX_NAME).write_text(json.dumps(matrix, indent=2, sort_keys=True) + "\n")
    (OUT_DIR / CTRF_NAME).write_text(json.dumps(ctrf_report(matrix), indent=2, sort_keys=True) + "\n")
    if not args.keep_work:
        shutil.rmtree(scratch_root, ignore_errors=True)
    return 0


def ctrf_report(matrix: dict) -> dict:
    tests_out = []
    passed = failed = skipped = 0
    fired = set(matrix["red_lines"]["fired"])
    details: dict[str, list[str]] = {}
    for finding in matrix["red_lines"]["findings"]:
        details.setdefault(finding["id"], []).append(
            f"{finding.get('path', '')} {finding.get('detail', '')}".strip()
        )
    # One named check per red line, joined by name with tests/cheat-checks.json.
    for rid in RED_LINE_IDS:
        status = "failed" if rid in fired else "passed"
        if status == "passed":
            passed += 1
        else:
            failed += 1
        item = {
            "name": rid,
            "status": status,
            "duration": 0,
            "suite": "red-lines",
            "extra": {"kind": "red-line", "fired": rid in fired},
        }
        if status == "failed":
            item["message"] = "; ".join(details.get(rid, ["red line fired"]))
        tests_out.append(item)
    for cid in matrix["candidate_order"]:
        entry = matrix["candidates"][cid]
        wall = entry["wall_seconds"]
        per = int(1000 * (sum(wall) / len(wall))) if wall else 0
        for name in matrix["collected_tests"]:
            status = ctrf_status(entry["results"][name])
            if status == "passed":
                passed += 1
            elif status == "failed":
                failed += 1
            else:
                skipped += 1
            item = {
                "name": f"{cid}::{name}",
                "status": status,
                "duration": per,
                "suite": cid,
                "extra": {
                    "candidate": cid,
                    "candidate_class": entry["class"],
                    "compile_error": entry["compile_error"],
                    "timeout": entry["timeout"],
                    "disagreed": name in entry["disagreements"],
                    "execution": entry["results"][name],
                    "suite_passed": entry["suite_passed"],
                },
            }
            if status == "failed":
                reasons = []
                if entry["compile_error"]:
                    reasons.append("compile error")
                if entry["timeout"]:
                    reasons.append("timeout")
                if name in entry["disagreements"]:
                    reasons.append("runs disagreed")
                item["message"] = "; ".join(reasons) or "test failed"
            tests_out.append(item)
    now = int(time.time())
    return {
        "reportFormat": "CTRF",
        "specVersion": "0.0.0",
        "reportId": hashlib.sha256(
            json.dumps(
                {k: matrix[k] for k in ("collected_tests", "candidate_order")}, sort_keys=True
            ).encode()
        ).hexdigest(),
        "results": {
            "tool": {"name": "kube-testgen-differential-grader", "version": "1"},
            "summary": {
                "tests": len(tests_out),
                "passed": passed,
                "failed": failed,
                "pending": 0,
                "skipped": skipped,
                "other": 0,
                "start": now,
                "stop": now,
                "extra": {
                    "gate_holds": matrix["gate"]["holds"],
                    "red_lines_fired": matrix["red_lines"]["fired"],
                },
            },
            "tests": tests_out,
        },
    }


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="Differential grader for collected Go tests.")
    ap.add_argument("--submission", default=None, help="Collected submission directory.")
    ap.add_argument("--scratch", default=None, help="Scratch root (default: a temp dir).")
    ap.add_argument(
        "--module-dir",
        default=None,
        help=f"The one fixed path every candidate is graded at (default: {MODULE_DIR}).",
    )
    ap.add_argument("--run-timeout", type=int, default=120, help="Per-run internal timeout, seconds.")
    ap.add_argument("--keep-work", action="store_true", help="Keep the scratch tree.")
    args = ap.parse_args(argv)
    return grade(args)


if __name__ == "__main__":
    sys.exit(main())
