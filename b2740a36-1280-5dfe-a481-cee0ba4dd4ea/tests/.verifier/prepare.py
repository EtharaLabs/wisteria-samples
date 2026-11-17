#!/usr/bin/env python3
"""Take the solver's edits to existing fontconfig library source files out of the collected tree.

The first --source directory that holds a fontconfig source tree is used (Harbor's collected
artifact first, then the runner's own path). Under it, only src/ is walked, without following
symlinks.

A file there is taken when the same path is a regular file of the pristine tree, the solver's copy is
a regular file reached through no symlink, at most MAX_BYTES long, and its bytes differ from the
pristine copy; its bytes are copied to --out at the same relative path and both SHA-256 digests are
recorded. Files that exist only in the solver's tree (new sources, build output from an in-tree
build) are counted and ignored, symlinks and oversized files are recorded as refused, and files of
the pristine tree that the solver deleted are recorded and stay in place (the solver's files are
laid over the pristine tree). Everything outside src/ (meson.build, test/, the tools, conf.d) is
ignored. Always exits 0 and always writes --json. Standard library only.

    prepare.py --source /logs/artifacts/app/fontconfig --source /app/fontconfig \
               --pristine /opt/statea --out /work/submission --json /logs/verifier/prepare.json
"""

import argparse
import hashlib
import json
import os
import shutil
import stat
import sys
from pathlib import Path

SOURCE_ROOT = "src"
SKIPPED_ROOT = None
MARKERS = ("meson.build", "src/fccache.c", "fontconfig/fontconfig.h.in")
MAX_BYTES = 4 * 1024 * 1024
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


def walk(base):
    """(regular files, non-regular entries) under base/src, '/'-separated."""
    regular, other = [], []
    if not real_dir(base, SOURCE_ROOT):
        return regular, other
    for dirpath, dirnames, filenames in os.walk(os.path.join(base, SOURCE_ROOT), followlinks=False):
        kept = []
        for d in dirnames:
            full = os.path.join(dirpath, d)
            rel = os.path.relpath(full, base).replace(os.sep, "/")
            if SKIPPED_ROOT is not None and rel == SKIPPED_ROOT:
                continue
            if os.path.islink(full):
                other.append(rel)
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
    return "ok", data


def prepare(source, pristine, out):
    taken, refused, deleted = [], [], []
    added_ignored = 0
    pristine_regular, _ = walk(pristine)
    pristine_set = set(pristine_regular)
    regular, other = walk(source)
    seen = set(regular) | set(other)
    for rel in other:
        if rel in pristine_set:
            refused.append({"path": rel, "status": "not_regular"})
        else:
            added_ignored += 1
    for rel in regular:
        if rel not in pristine_set:
            added_ignored += 1
            continue
        status, data = read_regular(os.path.join(source, rel))
        if status != "ok":
            refused.append({"path": rel, "status": status})
            continue
        base_digest = sha256_file(os.path.join(pristine, rel))
        digest = sha256_bytes(data)
        if digest == base_digest:
            continue
        if len(taken) >= MAX_FILES:
            refused.append({"path": rel, "status": "too_many_files"})
            continue
        dst = Path(out, rel)
        dst.parent.mkdir(parents=True, exist_ok=True)
        dst.write_bytes(data)
        taken.append({"path": rel, "sha256": digest, "base_sha256": base_digest})
    deleted.extend(rel for rel in pristine_regular if rel not in seen)
    return {"taken": taken, "refused": refused, "deleted_ignored": sorted(deleted),
            "added_ignored": added_ignored}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--source", action="append", default=[])
    ap.add_argument("--pristine", default="/opt/statea")
    ap.add_argument("--out", default="/work/submission")
    ap.add_argument("--json", default="/logs/verifier/prepare.json")
    args, _unknown = ap.parse_known_args()

    result = {"ok": False, "source": None, "status": "no_source", "out": args.out, "taken": [],
              "refused": [], "deleted_ignored": [], "added_ignored": 0}
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
        f"added_ignored={result['added_ignored']}")
    Path(args.json).parent.mkdir(parents=True, exist_ok=True)
    Path(args.json).write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:  # exit 0 always; a missing prepare.json scores 0
        log(f"unexpected error: {type(exc).__name__}: {exc}")
    sys.exit(0)
