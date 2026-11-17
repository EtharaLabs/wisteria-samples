#!/usr/bin/env python3
"""Build the solver's non-test sources over the pristine tree and run the graded tests.

Steps, in order:
  1. copy the pristine tree (/opt/statea, State A with its lock file) to a scratch directory and lay
     the taken sources (prepare.json, /work/submission) over it;
  2. restore the verifier's own test sources: the upstream fix's versions of the graded test files
     of the openraft crate and of the tests crate, each checked by SHA-256 against
     graded/manifest.json before (State A's digest, or absent for a new file) and after it is placed;
  3. build offline: compile every test target of the openraft and tests packages (cargo test
     --no-run, vendored crates only, the lock file unchanged);
  4. run them all once in one cargo invocation (cargo's own per-binary harness, --no-fail-fast),
     skipping the tests the manifest leaves out of the graded set by exact name;
  5. run the fail-to-pass tests FAIL_TO_PASS_RUNS - 1 more times, by exact name, in their own
     binaries.

A test's key is "<test binary>::<test path>", where the binary is the integration test's name (for
example "life_cycle") or the crate name for library tests ("openraft", "tests"). A test's verdict is
"pass" only when its harness line reads "ok" in every one of its runs. A graded test that reports no
result in a run (it hung until the run's limit killed it, or its binary crashed) is rerun once, by
exact name, inside the same verifier run; if it still reports nothing, that run of it is a failure
with reason "no_result". A pass-to-pass test that reports FAILED is run once more by exact name and
judged by that second run alone, so a pre-existing test that fails only intermittently does not
decide the score while a test the repair breaks fails both runs. report.json lists every graded test
with each of its runs.
A build killed at its limit, or a graded file whose digest does not match, is an infrastructure
fault: the report says so and test.sh ends with its fail-closed reward 0. Always exits 0 and always
writes --json. Standard library only.

With --sandbox UID:GID (test.sh passes it; this script then runs as root), the scratch tree and the
build directory are handed to that uid and every cargo process runs as it, with no supplementary
groups. Its output goes to log files this script opened, so it can write nothing else under --logs.
Run as any other user with --sandbox, the report is an infrastructure fault.

    graded.py --prepare /logs/verifier/prepare.json --pristine /opt/statea --graded /tests/graded \
              --work /work --logs /logs/verifier --json /logs/verifier/report.json \
              --sandbox 20002:20002
"""

import argparse
import hashlib
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import time
from pathlib import Path

SCHEMA = "openraft.test-report/v1"
PACKAGES = ["-p", "openraft", "-p", "tests"]
CARGO = "/usr/local/cargo/bin/cargo"
CARGO_FLAGS = ["--offline", "--locked"]
BUILD_LIMIT = 1800
TEST_LIMIT = 1500
FAIL_TO_PASS_RUNS = 3
F2P_LIMIT = 300
RERUN_LIMIT = 600
RUNNING = re.compile(r"^\s*Running (?:unittests )?(\S+) \((?:.*[/\\])?([A-Za-z0-9_]+)-[0-9a-f]+(?:\.exe)?\)\s*$")
RESULT = re.compile(r"^test (\S+)(?: - should panic)? \.\.\. (ok|FAILED|ignored)\b")
SANDBOX = None  # (uid, gid) from --sandbox: cargo runs as this uid with no supplementary groups


def log(msg):
    print(f"graded: {msg}", file=sys.stderr, flush=True)


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def env_for(work):
    # No judge credential or judge setting reaches a process that runs submitted code.
    env = {k: v for k, v in os.environ.items()
           if (not k.startswith(("CARGO_", "RUST")) or k in ("CARGO_HOME", "RUSTUP_HOME"))
           and not k.startswith(("ANTHROPIC_", "OPENAI_", "CLAUDE_", "REWARDKIT_", "LITELLM_"))}
    env.update(CARGO_HOME=os.environ.get("CARGO_HOME") or "/home/verifier/.cargo",
               RUSTUP_HOME=os.environ.get("RUSTUP_HOME") or "/usr/local/rustup",
               CARGO_TARGET_DIR=str(Path(work) / "target"), CARGO_TERM_COLOR="never",
               CARGO_BUILD_JOBS="4", CARGO_INCREMENTAL="0", RUST_BACKTRACE="0",
               HOME=os.environ.get("HOME") or "/home/verifier")
    env["PATH"] = "/usr/local/cargo/bin:" + env.get("PATH", "/usr/bin:/bin")
    return env


def run(cmd, cwd, env, limit, log_path):
    """Runs cmd in its own process group. Returns (exit code or None when killed, seconds)."""
    start = time.monotonic()
    ids = {} if SANDBOX is None else {"user": SANDBOX[0], "group": SANDBOX[1], "extra_groups": []}
    with open(log_path, "wb") as out:
        proc = subprocess.Popen(cmd, cwd=cwd, env=env, stdout=out, stderr=subprocess.STDOUT,
                                stdin=subprocess.DEVNULL, start_new_session=True, **ids)
        try:
            code = proc.wait(timeout=limit)
        except subprocess.TimeoutExpired:
            code = None
        finally:
            try:
                os.killpg(proc.pid, signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                pass
            proc.wait()
    return code, round(time.monotonic() - start, 1)


def read_results(log_path):
    """{"binary::test": outcome} from cargo test's harness output."""
    cases, binary = {}, None
    try:
        lines = Path(log_path).read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError:
        return cases
    for line in lines:
        m = RUNNING.match(line)
        if m:
            binary = m.group(2)
            continue
        m = RESULT.match(line)
        if m and binary is not None:
            key = f"{binary}::{m.group(1)}"
            outcome = {"ok": "pass", "FAILED": "failure", "ignored": "skipped"}[m.group(2)]
            # A name reported twice is a pass only if every report of it passed.
            if cases.get(key, "pass") == "pass":
                cases[key] = outcome
    return cases


def target_args(binary):
    """cargo target selection for a test binary key."""
    if binary == "openraft":
        return ["-p", "openraft", "--lib"]
    if binary == "tests":
        return ["-p", "tests", "--lib"]
    return ["-p", "tests", "--test", binary]


def run_named(tree, env, names, limit, log_path):
    """Runs the given graded tests by exact name, one cargo invocation per package. Returns
    (worst exit code or None when any invocation was killed, seconds, {"binary::test": outcome})."""
    by_pkg = {}
    for n in names:
        binary, path = n.split("::", 1)
        pkg = "openraft" if binary == "openraft" else "tests"
        by_pkg.setdefault(pkg, {}).setdefault(binary, set()).add(path)
    results, codes, total = {}, [], 0.0
    for idx, (pkg, binaries) in enumerate(sorted(by_pkg.items())):
        targets, paths = [], set()
        for binary, ps in sorted(binaries.items()):
            targets += target_args(binary)[2:]
            paths |= ps
        part = f"{log_path}.{idx}" if len(by_pkg) > 1 else log_path
        code, secs = run([CARGO, "test", *CARGO_FLAGS, "-p", pkg, *targets, "--no-fail-fast", "--",
                          "--exact", *sorted(paths)], tree, env, limit, part)
        codes.append(code)
        total += secs
        got = read_results(part)
        results.update({k: v for k, v in got.items() if k in set(names)})
    code = None if any(c is None for c in codes) else max(codes, default=0)
    return code, round(total, 1), results


def settle(args, tree, env, report, names, cases, label):
    """{"name": run record} for one run of names; a test without a result line in that run is rerun
    once by exact name, and if it still reports nothing it fails with reason no_result."""
    out = {n: {"outcome": cases[n]} for n in names if n in cases}
    missing = [n for n in names if n not in cases]
    if missing:
        log_path = os.path.join(args.logs, f"cargo-test-{label}-rerun.log")
        code, secs, again = run_named(tree, env, missing, RERUN_LIMIT, log_path)
        log(f"rerun after {label}: exit={code} {secs}s missing={len(missing)} "
            f"still_missing={sorted(n for n in missing if n not in again)}")
        report["runs"].append({"label": f"{label}-rerun", "exit": code, "seconds": secs,
                               "killed": code is None, "cases": dict(sorted(again.items()))})
        for n in missing:
            if n in again:
                out[n] = {"outcome": again[n], "rerun": True}
            else:
                out[n] = {"outcome": "failure", "rerun": True, "reason": "no_result"}
    return out


def verdict_of(runs):
    """A test passes only when every one of its runs passed."""
    bad = [r for r in runs if r["outcome"] != "pass"]
    entry = {"verdict": "pass" if not bad else bad[0]["outcome"], "runs": runs}
    if bad and bad[0].get("reason"):
        entry["reason"] = bad[0]["reason"]
    return entry


def hand_over(root, ids):
    """chown root and everything below it to ids, without following symlinks."""
    os.lchown(root, *ids)
    for dirpath, dirnames, filenames in os.walk(root):
        for name in dirnames + filenames:
            os.lchown(os.path.join(dirpath, name), *ids)


def grade(args, prepare, manifest, report):
    work = Path(args.work)
    work.mkdir(parents=True, exist_ok=True)
    env = env_for(work)
    tree = str(work / "tree")
    shutil.rmtree(tree, ignore_errors=True)
    shutil.rmtree(work / "target", ignore_errors=True)
    shutil.copytree(args.pristine, tree, symlinks=True)

    # 1. The taken sources, laid over the pristine tree.
    out = prepare.get("out") or "/work/submission"
    for entry in prepare.get("taken", []):
        rel = entry["path"]
        src = os.path.join(out, rel)
        if sha256(src) != entry["sha256"]:
            raise RuntimeError(f"taken file changed after prepare: {rel}")
        dst = os.path.join(tree, rel)
        if os.path.islink(dst):
            os.unlink(dst)
        os.makedirs(os.path.dirname(dst), exist_ok=True)
        shutil.copyfile(src, dst)

    # 2. The verifier's own test sources.
    for entry in manifest["test_files"]:
        dst = os.path.join(tree, entry["path"])
        base = sha256(dst) if os.path.lexists(dst) else None
        if base != entry["base_sha256"]:
            report.update(infra_fault=True, detail=f"base digest mismatch: {entry['path']}")
            return
        os.makedirs(os.path.dirname(dst), exist_ok=True)
        shutil.copyfile(os.path.join(args.graded, entry["name"]), dst)
        if sha256(dst) != entry["sha256"]:
            report.update(infra_fault=True, detail=f"graded digest mismatch: {entry['path']}")
            return

    # The scratch tree and the build directory belong to the uid that builds and runs the tests.
    if SANDBOX is not None:
        hand_over(tree, SANDBOX)
        (work / "target").mkdir()
        os.lchown(work / "target", *SANDBOX)

    # 3. Build offline.
    build = {"ok": False}
    report["build"] = build
    code, secs = run([CARGO, "test", *CARGO_FLAGS, *PACKAGES, "--tests", "--no-run"],
                     tree, env, BUILD_LIMIT, os.path.join(args.logs, "cargo-build.log"))
    build.update(exit=code, seconds=secs)
    log(f"build: exit={code} {secs}s")
    if code is None:
        report.update(infra_fault=True, detail="build killed at its limit")
        return
    if code != 0:
        return
    build["ok"] = True

    # 4. Every graded test, once; a graded test with no result line is rerun once by exact name.
    skips = []
    for name in manifest.get("not_run", []):
        skips += ["--skip", name.split("::", 1)[1]]
    p2p_names = list(manifest["pass_to_pass"])
    f2p_names = list(manifest["fail_to_pass"])
    log_path = os.path.join(args.logs, "cargo-test.log")
    code, secs = run([CARGO, "test", *CARGO_FLAGS, *PACKAGES, "--tests", "--no-fail-fast", "--",
                      "--exact", *skips], tree, env, TEST_LIMIT, log_path)
    cases = read_results(log_path)
    log(f"test: exit={code} {secs}s cases={len(cases)} "
        f"not_passed={sorted(k for k, v in cases.items() if v != 'pass')}")
    report["runs"].append({"label": "all-1", "exit": code, "seconds": secs, "killed": code is None,
                           "cases": dict(sorted(cases.items()))})
    first = settle(args, tree, env, report, p2p_names + f2p_names, cases, "all-1")
    report["pass_to_pass"] = {n: verdict_of([first[n]]) for n in p2p_names}
    # Recheck: a pass-to-pass test that reported FAILED runs once more by exact name and that second
    # run decides it (a test with no result was already rerun by settle()).
    recheck = [n for n in p2p_names if first[n]["outcome"] == "failure" and not first[n].get("rerun")]
    if recheck:
        log_path = os.path.join(args.logs, "cargo-test-p2p-recheck.log")
        code, secs, again = run_named(tree, env, recheck, RERUN_LIMIT, log_path)
        log(f"pass-to-pass recheck: exit={code} {secs}s rechecked={len(recheck)} "
            f"still_not_passed={sorted(n for n in recheck if again.get(n) != 'pass')}")
        report["runs"].append({"label": "p2p-recheck", "exit": code, "seconds": secs,
                               "killed": code is None, "cases": dict(sorted(again.items()))})
        for n in recheck:
            second = ({"outcome": again[n], "recheck": True} if n in again
                      else {"outcome": "failure", "recheck": True, "reason": "no_result"})
            report["pass_to_pass"][n] = {**verdict_of([second]), "runs": [first[n], second]}
    f2p = {n: [first[n]] for n in f2p_names}

    # 5. The fail-to-pass tests, again, until each has FAIL_TO_PASS_RUNS runs.
    for i in range(2, FAIL_TO_PASS_RUNS + 1):
        label = f"f2p-{i}"
        log_path = os.path.join(args.logs, f"cargo-test-{label}.log")
        code, secs, again = run_named(tree, env, f2p_names, F2P_LIMIT, log_path)
        log(f"fail-to-pass run {i}: exit={code} {secs}s "
            f"not_passed={sorted(n for n in f2p if again.get(n) != 'pass')}")
        report["runs"].append({"label": label, "exit": code, "seconds": secs, "killed": code is None,
                               "cases": dict(sorted(again.items()))})
        settled = settle(args, tree, env, report, f2p_names, again, label)
        for n in f2p:
            f2p[n].append(settled[n])
    report["fail_to_pass"] = {n: verdict_of(runs) for n, runs in f2p.items()}
    graded = set(manifest["pass_to_pass"]) | set(manifest["fail_to_pass"])
    report["unexpected_cases"] = sorted(set(cases) - graded)
    shutil.rmtree(tree, ignore_errors=True)
    shutil.rmtree(work / "target", ignore_errors=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--prepare", default="/logs/verifier/prepare.json")
    ap.add_argument("--pristine", default="/opt/statea")
    ap.add_argument("--graded", default="/tests/graded")
    ap.add_argument("--work", default="/work")
    ap.add_argument("--logs", default="/logs/verifier")
    ap.add_argument("--json", default="/logs/verifier/report.json")
    ap.add_argument("--sandbox", default="")
    args, _unknown = ap.parse_known_args()

    global SANDBOX
    report = {"schema": SCHEMA, "prepare": {"ok": False}, "infra_fault": False,
              "build": {"ok": False}, "runs": [], "pass_to_pass": {}, "fail_to_pass": {}}
    try:
        if args.sandbox:
            uid, gid = (int(x) for x in args.sandbox.split(":"))
            if os.geteuid() != 0:
                raise PermissionError(f"--sandbox {args.sandbox} needs root, running as {os.geteuid()}")
            SANDBOX = (uid, gid)
    except (ValueError, PermissionError) as exc:
        report.update(infra_fault=True, detail=f"sandbox: {type(exc).__name__}: {exc}")
        log(report["detail"])
    try:
        try:
            prepare = json.loads(Path(args.prepare).read_text(encoding="utf-8"))
        except (OSError, ValueError):
            prepare = {}
        report["prepare"] = {"ok": prepare.get("ok") is True, "status": prepare.get("status"),
                             "taken": [e.get("path") for e in prepare.get("taken", [])],
                             "refused": prepare.get("refused", []),
                             "deleted_ignored": len(prepare.get("deleted_ignored", [])),
                             "discarded_test_files": prepare.get("discarded_test_files")}
        manifest = json.loads(Path(args.graded, "manifest.json").read_text(encoding="utf-8"))
        if report["prepare"]["ok"] and not report["infra_fault"]:
            grade(args, prepare, manifest, report)
    except Exception as exc:  # never raise: a report is always written
        report["error"] = f"{type(exc).__name__}: {exc}"
        log(f"unexpected error: {report['error']}")
    Path(args.json).parent.mkdir(parents=True, exist_ok=True)
    Path(args.json).write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    p2p_bad = sorted(k for k, v in report["pass_to_pass"].items() if v.get("verdict") != "pass")
    f2p_bad = sorted(k for k, v in report["fail_to_pass"].items() if v.get("verdict") != "pass")
    log(f"build_ok={report['build'].get('ok')} p2p={len(report['pass_to_pass'])} "
        f"p2p_not_passed={p2p_bad} f2p={len(report['fail_to_pass'])} f2p_not_passed={f2p_bad} "
        f"infra_fault={report['infra_fault']}")


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:  # exit 0 always
        log(f"unexpected error: {type(exc).__name__}: {exc}")
    sys.exit(0)
