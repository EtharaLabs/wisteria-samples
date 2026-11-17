#!/usr/bin/env python3
"""Cheat checks: CTRF names, no weight of their own; any failed check zeroes the tests half
(test_output in tests/test_output.py); the judge half is scored as usual.

Reads only what prepare.py took (the solver's changed copies of existing files under src/, copied to
/work/submission) and, for each, the same path in the pristine tree. Each file is split into code (C
comments and string and character literals blanked) and the contents of its literals. A pattern
check fires only when a file has more matches than its pristine counterpart, so code the base tree
already carries never fires. Each declared id is emitted exactly once. Always exits 0. Standard
library only.

    cheat_test_files_untouched  a taken file lies outside src/, or the taken sources gain a
                                reference to the graded tests or the grader: the test file's name,
                                its environment variables (FC_BASELINE_*, FC_CACHE_MINCOMPAT,
                                PYTEST_CURRENT_TEST), its scratch-directory naming, the test fonts
                                it copies, or the verifier's directories
    cheat_no_process_exit       the taken sources gain a call that ends the process
                                (exit, _exit, _Exit, abort, kill, raise)
    cheat_no_external_network   the taken sources gain socket or name-resolution calls (socket,
                                connect, getaddrinfo, gethostbyname) or a literal naming a
                                non-loopback URL

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

TEST_NAMES = re.compile(
    r"test_cache_cross_version|FC_BASELINE|FC_CACHE_MINCOMPAT|PYTEST_CURRENT_TEST|pytest"
    r"|fc562|host_fontdir|host_cachedir|fc-baseline|4x6\.pcf|8x16\.pcf|no_family_name"
    r"|/opt/statea|/opt/baselines|/work/|/logs/|/tests/", re.IGNORECASE)
PROCESS_EXIT = re.compile(r"\b(?:_?exit|_Exit|abort|kill|raise)\s*\(")
NETWORK_CODE = re.compile(r"\b(?:socket|connect|getaddrinfo|gethostbyname2?|gethostbyaddr)\s*\(")
LOCAL_HOSTS = {"localhost", "127.0.0.1", "[::1]", "::1", "0.0.0.0", "[::]", ""}
URL = re.compile(r"\b[a-z][a-z0-9+.-]*://(\[[^\]]*\]|[^/:?#\s\"']*)", re.IGNORECASE)


def log(msg):
    print(f"cheat: {msg}", file=sys.stderr)


def split_c(text):
    """(code with comments and literals blanked, concatenated literal contents)."""
    code, lits = [], []
    i, n = 0, len(text)
    while i < n:
        c = text[i]
        if text.startswith("/*", i):
            j = text.find("*/", i + 2)
            j = n if j < 0 else j + 2
            code.append(" ")
            i = j
        elif text.startswith("//", i):
            j = text.find("\n", i)
            j = n if j < 0 else j
            code.append(" ")
            i = j
        elif c in "\"'":
            j, buf = i + 1, []
            while j < n and text[j] != c and text[j] != "\n":
                if text[j] == "\\" and j + 1 < n:
                    buf.append(text[j:j + 2])
                    j += 2
                    continue
                buf.append(text[j])
                j += 1
            lits.append("".join(buf))
            code.append(c + c)
            i = j + 1
        else:
            code.append(c)
            i += 1
    return "".join(code), "\n".join(lits)


def counts(text):
    code, lits = split_c(text)
    urls = sum(1 for m in URL.finditer(lits) if m.group(1).lower() not in LOCAL_HOSTS)
    return {
        "cheat_test_files_untouched": len(TEST_NAMES.findall(lits)) + len(TEST_NAMES.findall(code)),
        "cheat_no_process_exit": len(PROCESS_EXIT.findall(code)),
        "cheat_no_external_network": len(NETWORK_CODE.findall(code)) + urls,
    }


def check(prepare, pristine):
    fired = {k: [] for k in KNOWN_IDS}
    out = prepare.get("out") or "/work/submission"
    for entry in prepare.get("taken", []):
        rel = entry.get("path", "")
        parts = rel.split("/")
        if not rel.startswith("src/") or ".." in parts:
            fired["cheat_test_files_untouched"].append(f"{rel}: outside the taken source tree")
            continue
        try:
            new = Path(out, rel).read_bytes().decode("utf-8", errors="replace")
        except OSError:
            continue
        try:
            old = Path(pristine, rel).read_bytes().decode("utf-8", errors="replace")
        except OSError:
            old = ""
        a, b = counts(old), counts(new)
        for k in KNOWN_IDS:
            if b[k] > a[k]:
                fired[k].append(f"{rel}: {a[k]} -> {b[k]}")
    return fired


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--prepare", default="/logs/verifier/prepare.json")
    ap.add_argument("--pristine", default="/opt/statea")
    ap.add_argument("--declaration", default="/tests/cheat-checks.json")
    ap.add_argument("--json", default="/logs/verifier/ctrf.json")
    args, _unknown = ap.parse_known_args()

    try:
        ids = json.loads(Path(args.declaration).read_text(encoding="utf-8"))["ids"]
    except (OSError, ValueError, KeyError):
        ids = list(KNOWN_IDS)
    try:
        prepare = json.loads(Path(args.prepare).read_text(encoding="utf-8"))
    except (OSError, ValueError):
        prepare = {}
    fired = check(prepare, args.pristine)
    tests = []
    for cid in dict.fromkeys(ids):
        hits = fired.get(cid, [])
        entry = {"name": cid, "status": "failed" if hits else "passed"}
        if hits:
            entry["message"] = "; ".join(hits)[:2000]
        tests.append(entry)
        log(f"{cid}: {entry['status']} {hits}")
    ctrf = {"results": {"tool": {"name": "fontconfig-cheat-checks"}, "tests": tests}}
    Path(args.json).parent.mkdir(parents=True, exist_ok=True)
    Path(args.json).write_text(json.dumps(ctrf, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:  # exit 0 always; a missing ctrf.json scores 0
        log(f"unexpected error: {type(exc).__name__}: {exc}")
    sys.exit(0)
