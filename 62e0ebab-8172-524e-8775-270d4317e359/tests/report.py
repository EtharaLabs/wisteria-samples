#!/usr/bin/env python3
"""Reduce one verifier step to one machine-readable report.

tests/test.sh calls this once per step.  Importing this module defines
functions and nothing else: it registers no criterion, opens no file and
touches no network, so Reward Kit scanning the tests root is harmless.

Every subcommand writes its report whether the step passed or failed, and
every failure carries a reason string.  A step that produced nothing still
produces a report, so an absent report can only mean the verifier itself never
reached the step.
"""

from __future__ import annotations

import argparse
import ast
import hashlib
import json
import os
import re
import stat
import sys
from pathlib import Path

# The outcomes `go test -json` reports per test, mapped to the three words a
# report uses.  Anything else is recorded verbatim so it can never be mistaken
# for a pass.
_ACTIONS = {"pass": "pass", "fail": "fail", "skip": "skip"}


def write(path: Path, body: dict[str, object]) -> None:
    """Write one report atomically, sorted, with a trailing newline."""
    path.parent.mkdir(parents=True, exist_ok=True)
    scratch = path.with_suffix(path.suffix + ".part")
    scratch.write_text(json.dumps(body, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    scratch.replace(path)


def _events(raw: str) -> tuple[list[dict[str, object]], list[str]]:
    """Split a `go test -json` stream into decoded events and junk lines.

    The stream is append-only JSON objects, one per line, but a build failure
    or a panic can interleave plain text, so undecodable lines are kept aside
    rather than dropped or raised.
    """
    events: list[dict[str, object]] = []
    junk: list[str] = []
    for raw_line in raw.splitlines():
        line = raw_line.strip()
        if not line:
            continue
        if not line.startswith("{"):
            junk.append(line)
            continue
        try:
            loaded = json.loads(line)
        except json.JSONDecodeError:
            junk.append(line)
            continue
        if isinstance(loaded, dict):
            events.append(loaded)
        else:
            junk.append(line)
    return events, junk


def gotest(args: argparse.Namespace) -> int:
    """Reduce a `go test -json` stream to one outcome per requested name."""
    wanted = [
        name.strip()
        for name in Path(args.names).read_text(encoding="utf-8").splitlines()
        if name.strip()
    ]
    raw = sys.stdin.read()
    if args.raw is not None:
        Path(args.raw).parent.mkdir(parents=True, exist_ok=True)
        Path(args.raw).write_text(raw, encoding="utf-8")
    events, junk = _events(raw)

    outcomes: dict[str, str] = {}
    output: dict[str, list[str]] = {}
    build_failed = False
    for event in events:
        action = event.get("Action")
        name = event.get("Test")
        text = event.get("Output")
        said_build_failed = (
            action == "output" and isinstance(text, str) and "[build failed]" in text
        )
        if action == "build-fail" or said_build_failed:
            build_failed = True
        if not isinstance(name, str):
            continue
        if action == "output" and isinstance(text, str):
            output.setdefault(name, []).append(text.rstrip("\n"))
        if isinstance(action, str) and action in _ACTIONS:
            outcomes[name] = _ACTIONS[action]

    tests = {name: outcomes.get(name, "absent") for name in wanted}
    missing = sorted(name for name, state in tests.items() if state == "absent")
    reason = ""
    ok = True
    if build_failed:
        ok = False
        reason = "the staged package did not build, so no graded test ran"
    elif args.exit_code not in (0, 1):
        ok = False
        reason = f"the test driver exited {args.exit_code}, which is neither pass nor test failure"
    elif not events:
        ok = False
        reason = "the test driver emitted no JSON events"
    elif len(missing) == len(wanted):
        ok = False
        reason = "not one requested test was reported"

    write(
        Path(args.out),
        {
            "step": args.step,
            "ok": ok,
            "reason": reason,
            "exit_code": args.exit_code,
            "absent": missing,
            "tests": tests,
            "failure_output": {
                name: output.get(name, [])[-12:]
                for name, state in sorted(tests.items())
                if state == "fail"
            },
            "unparsed_lines": junk[:12],
        },
    )
    return 0


def replay(args: argparse.Namespace) -> int:
    """Merge one verdict per topology into the replay step's report."""
    topologies: dict[str, object] = {}
    reasons: dict[str, str] = {}
    for pair in args.verdict:
        name, _, where = pair.partition("=")
        path = Path(where)
        if not path.is_file():
            reasons[name] = f"the replay wrote no verdict at {where}"
            continue
        try:
            loaded = json.loads(path.read_text(encoding="utf-8"))
        except json.JSONDecodeError as problem:
            reasons[name] = f"the verdict at {where} is not JSON: {problem.msg}"
            continue
        if not isinstance(loaded, dict):
            reasons[name] = f"the verdict at {where} is not an object"
            continue
        checks = loaded.get("checks")
        preconditions = loaded.get("preconditions")
        if not isinstance(checks, dict):
            reasons[name] = f"the verdict at {where} carries no checks"
            continue
        failed_preconditions = (
            sorted(key for key, value in preconditions.items() if value is not True)
            if isinstance(preconditions, dict)
            else ["the verdict carries no preconditions"]
        )
        if failed_preconditions:
            reasons[name] = "the replay refused a verdict: " + ", ".join(failed_preconditions)
        topologies[name] = {
            "checks": checks,
            "pass": loaded.get("pass") is True,
            "timeline": loaded.get("timeline"),
            "nodes": loaded.get("nodes"),
            "origin_last_seq": loaded.get("origin_last_seq"),
            "failed_preconditions": failed_preconditions,
            "mirror": loaded.get("mirror"),
            "source": loaded.get("source"),
        }

    expected = [pair.partition("=")[0] for pair in args.verdict]
    absent = sorted(set(expected) - set(topologies))
    ok = not absent and not reasons
    reason = ""
    if absent:
        reason = "no usable verdict for " + ", ".join(absent)
    elif reasons:
        reason = "; ".join(f"{name}: {why}" for name, why in sorted(reasons.items()))
    write(
        Path(args.out),
        {"step": args.step, "ok": ok, "reason": reason, "topologies": topologies},
    )
    return 0


def plain(args: argparse.Namespace) -> int:
    """Write a report for a step whose whole outcome is an exit code."""
    write(
        Path(args.out),
        {
            "step": args.step,
            "ok": args.exit_code == 0,
            "reason": "" if args.exit_code == 0 else args.reason,
            "exit_code": args.exit_code,
            "detail": args.detail,
        },
    )
    return 0


def constants(source: str) -> dict[str, object]:
    """Every module-level literal constant of `source`, read without importing
    it.  test_output.py registers criteria on import, so this file never
    imports it: it parses the file and reads them instead."""
    tree = ast.parse(Path(source).read_text(encoding="utf-8"))
    found: dict[str, object] = {}
    for node in tree.body:
        if not isinstance(node, ast.Assign) or len(node.targets) != 1:
            continue
        target = node.targets[0]
        if not isinstance(target, ast.Name):
            continue
        try:
            found[target.id] = ast.literal_eval(node.value)
        except ValueError:
            continue
    return found


def names(args: argparse.Namespace) -> int:
    """Print the graded test names, read from test_output.py without importing it.

    The names test.sh selects and the names the criteria score are one list
    in test_output.py, read two ways, so they can never disagree.
    """
    found = constants(args.source)
    picked = found.get(args.constant)
    if isinstance(picked, dict):
        out = sorted(str(value) for value in picked.values())
    elif isinstance(picked, list):
        out = [str(value) for value in picked]
    else:
        print(f"report: {args.source} defines no list or mapping {args.constant}", file=sys.stderr)
        return 2
    for one in out:
        print(one)
    return 0


def usable(args: argparse.Namespace) -> int:
    """Exit 0 when a verdict exists, parses, and declares every precondition
    met; otherwise exit 1.

    A replay refuses a verdict when it could not set its own scene — when, for
    instance, it never observed the first delivery it needs before it can go
    on. That is a statement about the run, not about the server under test, so
    test.sh may try the topology again. A verdict whose preconditions all hold
    is usable however its checks came out, and is never retried: a genuine
    check failure must stand.
    """
    path = Path(args.verdict)
    if not path.is_file():
        return 1
    try:
        loaded = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return 1
    if not isinstance(loaded, dict):
        return 1
    preconditions = loaded.get("preconditions")
    if not isinstance(preconditions, dict) or not preconditions:
        return 1
    refused = sorted(key for key, value in preconditions.items() if value is not True)
    if refused:
        print("refused: " + ", ".join(refused), file=sys.stderr)
        return 1
    return 0 if isinstance(loaded.get("checks"), dict) else 1


# The three reward carriers.  tests/test.sh removes all three before the
# submission's code first runs and writes only its own fail-closed reward.json
# and reward.txt, which stay in place through this check so a reward file exists
# at every moment.  A carrier counts as present unless it is a regular file
# holding exactly the fail-closed bytes tests/test.sh names with --fail-closed,
# so any other carrier present when the check runs was written by something else.
_REWARD_FILES = ("reward.json", "reward-details.json", "reward.txt")


def _is_fail_closed(path: Path, expected: bytes | None) -> bool:
    """True when `path` is a regular file (not a symlink) holding exactly `expected`."""
    if expected is None:
        return False
    try:
        if not stat.S_ISREG(os.lstat(path).st_mode):
            return False
        with open(path, "rb") as handle:
            return handle.read(len(expected) + 1) == expected
    except OSError:
        return False

# The kinds of cheat check test_output.py's CHEAT_CHECKS constant may name.  The
# stage kinds read the submitted tree before any of it is copied, built or run;
# the runtime kinds read the verifier's own outputs just before Reward Kit runs.
_STAGE_KINDS = ("stage-symlink", "stage-harness")


def _verdict(ok: bool, reason: str) -> dict[str, str]:
    return {"status": "passed"} if ok else {"status": "failed", "message": reason}


def _symlink_escape(tree: Path) -> str:
    """The first symlink that leaves `tree`, or "" when none does."""
    if tree.is_symlink():
        return f"{tree} is itself a symlink"
    if not tree.is_dir():
        return ""
    base = tree.resolve()
    for top, dirs, files in base.walk(follow_symlinks=False):
        dirs.sort()
        for name in sorted(dirs + files):
            path = top / name
            if not path.is_symlink():
                continue
            target = path.readlink()
            landed = Path(os.path.normpath(top / target))
            if target.is_absolute() or not landed.is_relative_to(base):
                return f"{path.relative_to(base)} -> {target}"
    return ""


def _harness_counts(tree: Path, pattern: re.Pattern[str]) -> dict[str, int]:
    """Matches of `pattern` per non-test .go file of `tree`, keyed by the file's
    path relative to `tree`; symlinks and files without a match are left out."""
    counts: dict[str, int] = {}
    if tree.is_symlink() or not tree.is_dir():
        return counts
    base = tree.resolve()
    for top, dirs, files in base.walk(follow_symlinks=False):
        dirs.sort()
        for name in sorted(files):
            path = top / name
            if not name.endswith(".go") or name.endswith("_test.go") or path.is_symlink():
                continue
            hits = len(pattern.findall(path.read_text(encoding="utf-8", errors="replace")))
            if hits:
                counts[path.relative_to(base).as_posix()] = hits
    return counts


def cheat_static(args: argparse.Namespace) -> int:
    """Print the verdicts of the cheat checks that read the submitted tree.

    tests/test.sh runs this before it copies, builds or runs any submitted byte
    and keeps the printed verdicts in memory, so nothing the submission later
    runs can rewrite them.  A missing tree shows no cheat: both checks pass, and
    the graded steps record the missing tree themselves.
    """
    found = constants(args.source)
    declared = found.get("CHEAT_CHECKS")
    pattern = found.get("CHEAT_PATTERN")
    baseline = found.get("CHEAT_BASELINE")
    if (
        not isinstance(declared, dict)
        or not isinstance(pattern, str)
        or not isinstance(baseline, dict)
    ):
        print(f"report: {args.source} declares no cheat checks", file=sys.stderr)
        return 2
    tree = Path(args.tree)
    out: dict[str, dict[str, str]] = {}
    for ident, kind in sorted(declared.items()):
        if kind == "stage-symlink":
            escape = _symlink_escape(tree)
            out[str(ident)] = _verdict(not escape, f"a symlink leaves the submitted tree: {escape}")
        elif kind == "stage-harness":
            counts = _harness_counts(tree, re.compile(pattern))
            excess = sorted(path for path, hits in counts.items() if hits > baseline.get(path, 0))
            out[str(ident)] = _verdict(
                not excess, f"production code can tell it runs under go test: {', '.join(excess)}"
            )
    print(json.dumps(out, sort_keys=True))
    return 0


def _reports_changed(reports: Path, expect: dict[str, str]) -> list[str]:
    """Every step report that no longer hashes to the digest tests/test.sh
    recorded when it wrote it, is gone, or was never written by tests/test.sh."""
    changed: list[str] = []
    for name, digest in sorted(expect.items()):
        path = reports / name
        if path.is_symlink() or not path.is_file():
            changed.append(f"{name} (gone)")
        elif hashlib.sha256(path.read_bytes()).hexdigest() != digest:
            changed.append(f"{name} (rewritten)")
    if reports.is_dir():
        changed.extend(
            f"{entry.name} (not written by tests/test.sh)"
            for entry in sorted(reports.iterdir())
            if entry.name not in expect
        )
    return changed


def ctrf(args: argparse.Namespace) -> int:
    """Write the CTRF report the anti-cheat declaration is read against.

    `tests/cheat-checks.json` declares checks that read `failed` only when the
    submission's own bytes show a cheat.  Every declared id appears in this
    report exactly once, by test name, with a resolved status.

    The stage checks' verdicts arrive in --static, computed before any submitted
    byte ran.  The runtime checks are decided here, just before Reward Kit runs:
    no reward carrier other than tests/test.sh's own fail-closed ones (named by
    --fail-closed) may exist yet, and every step report must still hash to
    the digest tests/test.sh recorded when it wrote it, with no other file
    beside them.  A declared id this file cannot decide is refused, never
    guessed.  The report carries no reward and Reward Kit never reads it.
    """
    found = constants(args.source)
    declared = found.get("CHEAT_CHECKS")
    if not isinstance(declared, dict) or not declared:
        print(f"report: {args.source} declares no cheat checks", file=sys.stderr)
        return 2
    try:
        static = json.loads(args.static) if args.static else {}
    except ValueError:
        static = {}
    if not isinstance(static, dict):
        static = {}
    expect: dict[str, str] = {}
    for item in args.expect:
        name, sep, digest = str(item).partition("=")
        if not sep or not name or not digest:
            print(f"report: --expect {item!r} is not NAME=SHA256", file=sys.stderr)
            return 2
        expect[name] = digest
    fail_closed: dict[str, bytes] = {}
    for item in args.fail_closed:
        name, sep, text = str(item).partition("=")
        if not sep or name not in _REWARD_FILES:
            print(f"report: --fail-closed {item!r} is not CARRIER=TEXT", file=sys.stderr)
            return 2
        fail_closed[name] = (text + "\n").encode("utf-8")
    logs = Path(args.logs)
    rows: list[dict[str, str]] = []
    for ident, kind in sorted(declared.items()):
        if kind in _STAGE_KINDS:
            verdict = static.get(ident)
            if not isinstance(verdict, dict) or verdict.get("status") not in ("passed", "failed"):
                print(f"report: no verdict for the stage check {ident}", file=sys.stderr)
                return 2
            row = {"name": str(ident), **{k: str(v) for k, v in verdict.items()}}
        elif kind == "runtime-reward-files":
            present = [
                n
                for n in _REWARD_FILES
                if ((logs / n).exists() or (logs / n).is_symlink())
                and not _is_fail_closed(logs / n, fail_closed.get(n))
            ]
            row = {
                "name": str(ident),
                **_verdict(
                    not present, f"reward carriers before Reward Kit ran: {', '.join(present)}"
                ),
            }
        elif kind == "runtime-step-reports":
            changed = _reports_changed(Path(args.reports), expect)
            row = {
                "name": str(ident),
                **_verdict(not changed, f"step reports not as written: {', '.join(changed)}"),
            }
        else:
            print(f"report: {ident} names an unknown cheat-check kind {kind!r}", file=sys.stderr)
            return 2
        rows.append(row)
    write(Path(args.out), {"results": {"tests": rows}})
    return 0


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(required=True)

    one = sub.add_parser("gotest", help="reduce a go test -json stream read from stdin")
    one.add_argument("--step", required=True)
    one.add_argument("--names", required=True, help="file of one test name per line")
    one.add_argument("--exit-code", type=int, required=True)
    one.add_argument("--out", required=True)
    one.add_argument("--raw", default=None, help="keep the unreduced stream here")
    one.set_defaults(handler=gotest)

    two = sub.add_parser("replay", help="merge replay verdicts")
    two.add_argument("--step", required=True)
    two.add_argument("--verdict", action="append", default=[], metavar="NAME=PATH", required=True)
    two.add_argument("--out", required=True)
    two.set_defaults(handler=replay)

    three = sub.add_parser("plain", help="report a step whose outcome is an exit code")
    three.add_argument("--step", required=True)
    three.add_argument("--exit-code", type=int, required=True)
    three.add_argument("--reason", default="the step failed")
    three.add_argument("--detail", default="")
    three.add_argument("--out", required=True)
    three.set_defaults(handler=plain)

    four = sub.add_parser("names", help="print graded test names from test_output.py")
    four.add_argument("--source", required=True)
    four.add_argument("--constant", required=True)
    four.set_defaults(handler=names)

    five = sub.add_parser("usable", help="exit 0 when a verdict is usable as graded evidence")
    five.add_argument("--verdict", required=True)
    five.set_defaults(handler=usable)

    six = sub.add_parser("ctrf", help="write the CTRF report for the declared cheat checks")
    six.add_argument("--source", required=True)
    six.add_argument("--static", required=True, help="the verdicts cheat-static printed")
    six.add_argument("--reports", required=True)
    six.add_argument("--logs", required=True, help="the directory Reward Kit writes into")
    six.add_argument("--expect", action="append", default=[], metavar="NAME=SHA256")
    six.add_argument(
        "--fail-closed",
        action="append",
        default=[],
        metavar="CARRIER=TEXT",
        help="a carrier holding exactly TEXT and a newline, as tests/test.sh wrote it",
    )
    six.add_argument("--out", required=True)
    six.set_defaults(handler=ctrf)

    seven = sub.add_parser("cheat-static", help="print the stage cheat-check verdicts")
    seven.add_argument("--source", required=True)
    seven.add_argument("--tree", required=True)
    seven.set_defaults(handler=cheat_static)

    args = parser.parse_args(argv)
    handler: object = args.handler
    if not callable(handler):
        raise TypeError("the subcommand registered no handler")
    result = handler(args)
    return result if isinstance(result, int) else 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
