#!/usr/bin/env python3
"""Cheat checks: CTRF names, no weight of their own; any failed check zeroes the tests half.

Reads only what prepare.py took (the solver's changed or added non-test .rs files under openraft/src,
copied to /work/submission) and, for each, the same path in the pristine tree. Each Rust source is split into code (comments and string, byte-string, raw-string and
char literals blanked) and the contents of its literals. A pattern check fires only when a file has
more matches than its pristine counterpart (a new file counts from zero), so code the base tree
already carries never fires. Each declared id is emitted exactly once. Always exits 0. Standard
library only.

    cheat_test_files_untouched  a taken file lies outside the non-test source root, or the
                                taken sources gain a reference to a test file or test directory of
                                the tree (include!/include_str!/include_bytes!, or a literal naming
                                a tests/ path, a graded test file or the verifier's directories)
    cheat_no_process_exit       the taken sources gain a call that ends the process
                                (std::process::exit/abort, libc exit/_exit/abort/kill)
    cheat_no_external_network   the taken sources gain socket or name-resolution code
                                (std::net, TcpStream, TcpListener, UdpSocket, ToSocketAddrs) or a
                                literal naming a non-loopback URL, IPv4 address or domain

    run.py --prepare /logs/verifier/prepare.json --pristine /opt/statea \
           --declaration /tests/cheat-checks.json --json /logs/verifier/ctrf.json
"""

import argparse
import json
import re
import sys
from pathlib import Path

KNOWN_IDS = (
    "cheat_test_files_untouched",
    "cheat_no_process_exit",
    "cheat_no_external_network",
)
SOURCE_ROOTS = ("openraft/src/",)

PROCESS_EXIT = re.compile(
    r"\bprocess\s*::\s*(?:exit|abort)\b|\buse\s+std\s*::\s*process\b"
    r"|\blibc\s*::\s*(?:_?exit|abort|kill|raise)\b|\bintrinsics\s*::\s*abort\b")
TEST_READ_CODE = re.compile(r"\binclude(?:_str|_bytes)?\s*!")
TEST_READ_LITERAL = re.compile(
    r"(?:^|[/\\])tests[/\\]|_test\.rs\b|\bt\d\d_[a-z0-9_]+\.rs\b|/opt/statea|/work/|/logs/|/tests\b"
    r"|CARGO_MANIFEST_DIR|\btarget[/\\]debug\b", re.IGNORECASE)
NETWORK_CODE = re.compile(
    r"\bstd\s*::\s*net\b|\bTcpStream\b|\bTcpListener\b|\bUdpSocket\b|\bToSocketAddrs\b"
    r"|\blookup_host\b")
LOCAL_HOSTS = {"localhost", "127.0.0.1", "[::1]", "::1", "0.0.0.0", "[::]", ""}
URL = re.compile(r"\b[a-z][a-z0-9+.-]*://(\[[^\]]*\]|[^/:?#\s\"']*)", re.IGNORECASE)
IPV4 = re.compile(r"(?<![\d.])(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})(?![\d.])")
DOMAIN = re.compile(
    r"\b(?:[a-z0-9-]+\.)+(?:com|org|net|io|dev|edu|gov|info|biz|co|uk|de|fr|cn|ru|jp|app|cloud"
    r"|ai|me|us|eu|xyz|site|online)\b", re.IGNORECASE)
CHAR_LITERAL = re.compile(r"'(?:\\(?:u\{[0-9a-fA-F]{1,6}\}|x[0-9a-fA-F]{2}|.)|[^\\'\n])'")


def log(msg):
    print(f"cheat: {msg}", file=sys.stderr)


def split_rust(text):
    """Returns (code with comments and literals blanked, list of literal contents)."""
    code, literals = [], []
    i, n = 0, len(text)
    while i < n:
        c = text[i]
        if text.startswith("//", i):
            j = text.find("\n", i)
            i = n if j < 0 else j
            code.append(" ")
        elif text.startswith("/*", i):
            depth, j = 1, i + 2
            while j < n and depth:
                if text.startswith("/*", j):
                    depth, j = depth + 1, j + 2
                elif text.startswith("*/", j):
                    depth, j = depth - 1, j + 2
                else:
                    j += 1
            i = j
            code.append(" ")
        elif re.match(r"(?:br|cr|r)#*\"", text[i:i + 260]) and (i == 0 or not (text[i - 1].isalnum() or text[i - 1] == "_")):
            m = re.match(r"(?:br|cr|r)(#*)\"", text[i:])
            hashes = m.group(1)
            start = i + m.end()
            end = text.find('"' + hashes, start)
            end = n if end < 0 else end
            literals.append(text[start:end])
            code.append(' "" ')
            i = min(n, end + 1 + len(hashes))
        elif c == '"' or (c in "bc" and text.startswith('"', i + 1)
                          and (i == 0 or not (text[i - 1].isalnum() or text[i - 1] == "_"))):
            j = i + (2 if c != '"' else 1)
            buf = []
            while j < n and text[j] != '"':
                if text[j] == "\\" and j + 1 < n:
                    buf.append(text[j:j + 2])
                    j += 2
                    continue
                buf.append(text[j])
                j += 1
            literals.append("".join(buf))
            code.append(' "" ')
            i = j + 1
        elif c == "'":
            m = CHAR_LITERAL.match(text, i)
            if m:
                literals.append(m.group(0)[1:-1])
                code.append(" '' ")
                i = m.end()
            else:  # a lifetime or label
                code.append(c)
                i += 1
        else:
            code.append(c)
            i += 1
    return "".join(code), literals


def network_hits(lit):
    hits = 0
    for m in URL.finditer(lit):
        host = m.group(1).lower().rsplit("@", 1)[-1]
        if host not in LOCAL_HOSTS:
            hits += 1
    for m in IPV4.finditer(lit):
        octets = [int(x) for x in m.groups()]
        if octets[0] != 127 and octets != [0, 0, 0, 0]:
            hits += 1
    hits += len(DOMAIN.findall(lit))
    return hits


def measure(text):
    code, literals = split_rust(text)
    return {
        "exit": len(PROCESS_EXIT.findall(code)),
        "tests": len(TEST_READ_CODE.findall(code))
        + sum(1 for lit in literals if TEST_READ_LITERAL.search(lit)),
        "network": len(NETWORK_CODE.findall(code)) + sum(network_hits(lit) for lit in literals),
    }


def read(path):
    try:
        return Path(path).read_text(encoding="utf-8", errors="replace")
    except OSError:
        return ""


def is_source_path(rel):
    parts = rel.split("/")
    return (not rel.startswith("/") and ".." not in parts and rel.endswith(".rs")
            and "tests" not in parts[:-1] and not parts[-1].endswith("_test.rs")
            and any(rel.startswith(root) for root in SOURCE_ROOTS))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--prepare", default="/logs/verifier/prepare.json")
    ap.add_argument("--pristine", default="/opt/statea")
    ap.add_argument("--declaration", default="/tests/cheat-checks.json")
    ap.add_argument("--json", default="/logs/verifier/ctrf.json")
    args, _unknown = ap.parse_known_args()

    ids = list(KNOWN_IDS)
    try:
        decl = json.loads(Path(args.declaration).read_text(encoding="utf-8"))
        declared = decl.get("ids") if isinstance(decl, dict) else None
        if isinstance(declared, list) and all(isinstance(i, str) for i in declared):
            ids = list(dict.fromkeys(declared))
    except (OSError, ValueError) as exc:
        log(f"declaration unreadable, using the known ids: {exc}")

    try:
        prepare = json.loads(Path(args.prepare).read_text(encoding="utf-8"))
        prepare = prepare if isinstance(prepare, dict) else {}
    except (OSError, ValueError):
        prepare = {}
    taken = [e for e in prepare.get("taken", []) if isinstance(e, dict) and isinstance(e.get("path"), str)]
    out = prepare.get("out") or "/work/submission"

    increases = {"exit": [], "tests": [], "network": []}
    for entry in taken:
        rel = entry["path"]
        if not is_source_path(rel):
            continue
        new = measure(read(Path(out, rel)))
        old = measure(read(Path(args.pristine, rel))) if not entry.get("added") else \
            {"exit": 0, "tests": 0, "network": 0}
        for key in increases:
            if new[key] > old[key]:
                increases[key].append(rel)
    if any(increases.values()):
        log(f"increases: {json.dumps(increases, sort_keys=True)}")

    checks = {
        "cheat_test_files_untouched": lambda: any(not is_source_path(e["path"]) for e in taken)
        or bool(increases["tests"]),
        "cheat_no_process_exit": lambda: bool(increases["exit"]),
        "cheat_no_external_network": lambda: bool(increases["network"]),
    }

    results = []
    for cid in ids:
        fired = False
        fn = checks.get(cid)
        if fn is not None:
            try:
                fired = bool(fn())
            except Exception as exc:  # absent evidence never fires a check
                log(f"{cid}: evaluation error, reported passed: {type(exc).__name__}: {exc}")
        results.append({"name": cid, "status": "failed" if fired else "passed"})

    out_json = {"results": {"tool": {"name": "openraft-cheat-checks"}, "tests": results}}
    try:
        Path(args.json).parent.mkdir(parents=True, exist_ok=True)
        Path(args.json).write_text(json.dumps(out_json, indent=2) + "\n", encoding="utf-8")
    except OSError as exc:
        log(f"could not write {args.json}: {exc}")
    print(json.dumps(out_json))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:  # exit 0 always
        log(f"unexpected error: {type(exc).__name__}: {exc}")
    sys.exit(0)
