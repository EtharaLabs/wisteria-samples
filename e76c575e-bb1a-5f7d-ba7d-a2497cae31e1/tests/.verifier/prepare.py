#!/usr/bin/env python3
"""Take the solver's non-test Rust sources of the openraft crate out of the collected tree.

The first --source directory that holds an openraft workspace is used (Harbor's collected artifact
first, then the runner's own path). Under it, only one source root is walked, without following
symlinks:

    openraft/src

A file there is taken when its name ends in .rs, it is not a test file (a file below a directory
named tests, or a file whose name ends in _test.rs), it is a regular file reached through no
symlink, at most MAX_BYTES long, valid UTF-8, and differs from (or is absent in) the pristine tree;
its bytes are copied to --out at the same relative path and its SHA-256 recorded. Symlinks,
oversized, non-UTF-8 and non-.rs files are recorded as refused. Files of the pristine tree that the
solver deleted are recorded and stay in place (the solver's files are laid over the pristine tree).
Every other file of the collected tree is ignored: test files, other crates, manifests, the lock
file, documentation, build scripts and build output. Changed or added test files are counted as
discarded for the record. Always exits 0 and always writes --json. Standard library only.

    prepare.py --source /logs/artifacts/app/openraft --source /app/openraft --pristine /opt/statea \
               --out /work/submission --json /logs/verifier/prepare.json
"""

import argparse
import hashlib
import json
import os
import shutil
import stat
import sys
from pathlib import Path

SOURCE_ROOTS = ("openraft/src",)
TEST_ROOTS = ("openraft/src", "openraft/tests", "tests")
MARKERS = ("Cargo.toml", "openraft/src", "tests/tests")
MAX_BYTES = 1024 * 1024
MAX_FILES = 2000


def log(msg):
    print(f"prepare: {msg}", file=sys.stderr)


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def real_dir(base, rel):
    """True when base/rel is a directory reached through real directories only."""
    path = base
    for part in rel.split("/"):
        path = os.path.join(path, part)
        if os.path.islink(path) or not os.path.isdir(path):
            return False
    return True


def files_under(base, rel_root):
    """(regular files, symlinks and other non-regular entries), relative to base, '/'-separated."""
    regular, other = [], []
    if not real_dir(base, rel_root):
        return regular, other
    for dirpath, dirnames, filenames in os.walk(os.path.join(base, rel_root), followlinks=False):
        kept = []
        for d in dirnames:
            full = os.path.join(dirpath, d)
            if os.path.islink(full):
                other.append(os.path.relpath(full, base).replace(os.sep, "/"))
            else:
                kept.append(d)
        dirnames[:] = sorted(kept)
        for f in sorted(filenames):
            full = os.path.join(dirpath, f)
            rel = os.path.relpath(full, base).replace(os.sep, "/")
            try:
                st = os.lstat(full)
            except OSError:
                other.append(rel)
                continue
            (regular if stat.S_ISREG(st.st_mode) else other).append(rel)
    return regular, other


def read_regular(path):
    """Returns (status, bytes)."""
    try:
        st = os.lstat(path)
    except OSError:
        return "unreadable", None
    if not stat.S_ISREG(st.st_mode):
        return "not_regular", None
    if st.st_size > MAX_BYTES:
        return "too_large", None
    try:
        fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        with os.fdopen(fd, "rb") as handle:
            data = handle.read(MAX_BYTES + 1)
    except OSError:
        return "unreadable", None
    if len(data) > MAX_BYTES:
        return "too_large", None
    try:
        data.decode("utf-8")
    except UnicodeDecodeError:
        return "not_utf8", None
    return "ok", data


def is_test_path(rel):
    parts = rel.split("/")
    return "tests" in parts[:-1] or parts[-1].endswith("_test.rs")


def is_source_path(rel):
    parts = rel.split("/")
    if rel.startswith("/") or ".." in parts or not rel.endswith(".rs") or is_test_path(rel):
        return False
    return any(rel.startswith(root + "/") for root in SOURCE_ROOTS)


def prepare(source, pristine, out):
    taken, refused, deleted = [], [], []
    seen = set()
    for rel_root in SOURCE_ROOTS:
        regular, other = files_under(source, rel_root)
        for rel in other:
            refused.append({"path": rel, "status": "not_regular"})
        for rel in regular:
            if is_test_path(rel):
                continue
            seen.add(rel)
            if not is_source_path(rel):
                refused.append({"path": rel, "status": "not_rust_source"})
                continue
            status, data = read_regular(os.path.join(source, rel))
            if status != "ok":
                refused.append({"path": rel, "status": status})
                continue
            base = os.path.join(pristine, rel)
            base_digest = sha256_file(base) if os.path.isfile(base) and not os.path.islink(base) else None
            digest = sha256_bytes(data)
            if digest == base_digest:
                continue
            if len(taken) >= MAX_FILES:
                refused.append({"path": rel, "status": "too_many_files"})
                continue
            dst = Path(out, rel)
            dst.parent.mkdir(parents=True, exist_ok=True)
            dst.write_bytes(data)
            taken.append({"path": rel, "sha256": digest, "base_sha256": base_digest,
                          "added": base_digest is None})
    for rel_root in SOURCE_ROOTS:
        regular, _other = files_under(pristine, rel_root)
        deleted.extend(rel for rel in regular if rel not in seen and not is_test_path(rel))
    discarded_tests = 0
    for rel_root in TEST_ROOTS:
        regular, other = files_under(source, rel_root)
        for rel in regular:
            if not is_test_path(rel):
                continue
            base = os.path.join(pristine, rel)
            try:
                if not os.path.isfile(base) or sha256_file(os.path.join(source, rel)) != sha256_file(base):
                    discarded_tests += 1
            except OSError:
                discarded_tests += 1
        discarded_tests += sum(1 for rel in other if is_test_path(rel))
    return {"taken": taken, "refused": refused, "deleted_ignored": sorted(deleted),
            "discarded_test_files": discarded_tests}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--source", action="append", default=[])
    ap.add_argument("--pristine", default="/opt/statea")
    ap.add_argument("--out", default="/work/submission")
    ap.add_argument("--json", default="/logs/verifier/prepare.json")
    args, _unknown = ap.parse_known_args()

    result = {"ok": False, "source": None, "status": "no_source", "out": args.out, "taken": [],
              "refused": [], "deleted_ignored": [], "discarded_test_files": 0}
    for source in args.source:
        if os.path.isdir(source) and all(os.path.exists(os.path.join(source, m)) for m in MARKERS):
            result["source"] = source
            break
    if result["source"] is not None:
        shutil.rmtree(args.out, ignore_errors=True)
        Path(args.out).mkdir(parents=True, exist_ok=True)
        try:
            result.update(prepare(result["source"], args.pristine, args.out))
            result.update(ok=True, status="ok")
        except Exception as exc:  # fail closed: a tree that cannot be taken scores 0
            result.update(ok=False, status=f"error: {type(exc).__name__}: {exc}")
    log(f"ok={result['ok']} status={result['status']} taken={len(result['taken'])} "
        f"refused={len(result['refused'])} deleted_ignored={len(result['deleted_ignored'])} "
        f"discarded_test_files={result['discarded_test_files']}")
    Path(args.json).parent.mkdir(parents=True, exist_ok=True)
    Path(args.json).write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:  # exit 0 always; a missing prepare.json scores 0
        log(f"unexpected error: {type(exc).__name__}: {exc}")
    sys.exit(0)
