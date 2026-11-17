#!/usr/bin/env python3
"""Build the solver's fontconfig sources over the pristine tree and run the graded tests.

Steps, in order:
  1. copy the pristine tree (/opt/statea, State A) to a scratch directory and lay the taken sources
     (prepare.json, /work/submission) over it;
  2. place the verifier's own test file, the upstream fix's test/test_cache_cross_version.py,
     checked by SHA-256 against graded/manifest.json before and after it is placed;
  3. configure out of tree with meson (the same options as the agent's image) and build with ninja;
  4. pass-to-pass: every test of the project's meson test suite except the one named "pytest", once,
     one unit per meson test ("meson/<name>");
  5. pass-to-pass: the project's pytest suite, once, run exactly as the meson "pytest" test runs it
     (its working directory and environment come from `meson introspect --tests`), with the
     cross-version file left to step 6; one unit per test case ("pytest/<file>::<case>");
  6. the cross-version file against each prebuilt older release in the manifest
     (FC_BASELINE_BUILDDIR and FC_BASELINE_CACHE_VERSION set to that release's build and cache
     version), XV_RUNS times; one unit per release and case ("cross-version/<release>/<case>").
     The manifest names which of these are fail-to-pass and which are pass-to-pass; each counts
     only when it passes in every run.

A suite run that is killed at its limit or leaves no JUnit result is rerun once; a unit still
without a result is "missing" with a reason, and a skipped unit is "skipped" (neither passes). A
build killed at its limit, or a graded test file whose digest does not match, is an infrastructure
fault: the report says so and test.sh ends without a reward. Every outcome seen for every case,
graded or not, is kept under "observed". Always exits 0 and always writes --json. Standard library
only.

With --sandbox UID:GID (test.sh passes it; this script then runs as root), the scratch tree is handed
to that uid and every process that builds or runs the submitted sources (meson, ninja, the meson
tests, pytest) runs as it, with no supplementary groups. Their output goes to log files this script
opened, the report is written by this script alone, and the files they leave behind (meson's test
log, the JUnit files) are read without following symbolic links, so the sandbox can write neither
--logs, --json nor anything this script reads as a path of its choosing. Run as any other user with
--sandbox, the report is an infrastructure fault.

    graded.py --prepare /logs/verifier/prepare.json --pristine /opt/statea --graded /tests/graded \
              --work /work --logs /var/lib/grade/private/logs \
              --json /var/lib/grade/private/report.json --sandbox 20002:20002
"""

import argparse
import hashlib
import json
import os
import shutil
import signal
import stat
import subprocess
import sys
import time
import xml.etree.ElementTree as ET
from pathlib import Path

SCHEMA = "fontconfig-grader.test-report/v1"
MESON_OPTIONS = ["-Ddoc=disabled", "-Dnls=disabled", "-Dtests=enabled", "-Dtools=enabled",
                 "-Dtests-external-fonts=disabled", "-Dtests-bwrap=disabled",
                 "-Dcache-build=disabled", "-Dfontations=disabled", "-Dxml-backend=expat"]
JOBS = "4"
BUILD_LIMIT = 1200
MESON_TEST_LIMIT = 600
PYTEST_LIMIT = 600
XV_LIMIT = 240
XV_FILE = "test_cache_cross_version.py"
RESULT_CAP = 64 << 20  # bytes read from a result file the sandbox wrote
SANDBOX = None  # (uid, gid) from --sandbox: the build and the tests run as this uid, no extra groups


def log(msg):
    print(f"graded: {msg}", file=sys.stderr, flush=True)


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def base_env():
    keep = ("HOME", "PATH", "TMPDIR", "USER", "LOGNAME")
    env = {k: v for k, v in os.environ.items() if k in keep}
    env.setdefault("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
    env.setdefault("HOME", "/home/verifier")
    env["LC_ALL"] = "C.UTF-8"
    env["LANG"] = "C.UTF-8"
    env["PYTHONDONTWRITEBYTECODE"] = "1"
    return env


def sandbox_ids():
    """Popen keyword arguments that drop a child to the sandbox uid (none without --sandbox)."""
    return {} if SANDBOX is None else {"user": SANDBOX[0], "group": SANDBOX[1], "extra_groups": []}


def read_untrusted(path, cap=RESULT_CAP):
    """The bytes of a regular file the sandbox may control, or None. No component of the absolute
    path may be a symbolic link, a non-regular file (a FIFO, a device) is refused without blocking,
    and a file larger than cap is refused."""
    parts = [p for p in os.path.normpath(path).split(os.sep) if p]
    if not os.path.isabs(path) or not parts:
        return None
    fds = []
    try:
        fds.append(os.open("/", os.O_RDONLY | os.O_DIRECTORY))
        for part in parts[:-1]:
            fds.append(os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fds[-1]))
        fd = os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fds[-1])
    except OSError:
        return None
    finally:
        for d in fds:
            os.close(d)
    with os.fdopen(fd, "rb") as handle:
        st = os.fstat(handle.fileno())
        if not stat.S_ISREG(st.st_mode) or st.st_size > cap:
            return None
        data = handle.read(cap + 1)
    return data if len(data) <= cap else None


def remove_as_sandbox(path):
    """Removes a file in a directory the sandbox owns, as the sandbox, so that a symbolic link it
    planted on the way never makes this script delete anything else."""
    if SANDBOX is None:
        try:
            os.unlink(path)
        except FileNotFoundError:
            pass
        return
    subprocess.run(["rm", "-f", "--", path], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                   stderr=subprocess.DEVNULL, timeout=60, check=False, env=base_env(),
                   **sandbox_ids())


def hand_over(root, ids):
    """chown root and everything below it to ids, without following symlinks."""
    os.lchown(root, *ids)
    for dirpath, dirnames, filenames in os.walk(root):
        for name in dirnames + filenames:
            os.lchown(os.path.join(dirpath, name), *ids)


def run(cmd, cwd, env, limit, log_path):
    """Runs cmd in its own process group, as the sandbox uid when there is one. Returns (exit code
    or None when killed, seconds)."""
    start = time.monotonic()
    with open(log_path, "wb") as out:
        proc = subprocess.Popen(cmd, cwd=cwd, env=env, stdout=out, stderr=subprocess.STDOUT,
                                stdin=subprocess.DEVNULL, start_new_session=True, **sandbox_ids())
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


def read_junit(path):
    """{"<file>::<case>": "pass" | "failure" | "skipped"} from a pytest JUnit XML file."""
    cases = {}
    data = read_untrusted(path)
    if data is None:
        return cases
    try:
        root = ET.fromstring(data)
    except ET.ParseError:
        return cases
    for tc in root.iter("testcase"):
        cls = tc.get("classname", "")
        name = tc.get("name", "")
        parts = cls.split(".") if cls else []
        key = "::".join([f"{parts[0]}.py", *parts[1:], name]) if parts else name
        if tc.find("failure") is not None or tc.find("error") is not None:
            outcome = "failure"
        elif tc.find("skipped") is not None:
            outcome = "skipped"
        else:
            outcome = "pass"
        if cases.get(key, "pass") == "pass" or outcome == "failure":
            cases[key] = outcome
    return cases


def read_meson_log(path):
    """{"<test>": "pass" | "failure" | "skipped"} from meson-logs/testlog.json (JSON lines)."""
    cases = {}
    data = read_untrusted(path)
    if data is None:
        return cases
    lines = data.decode("utf-8", errors="replace").splitlines()
    for line in lines:
        try:
            rec = json.loads(line)
        except ValueError:
            continue
        result = str(rec.get("result", ""))
        name = rec.get("name")
        if not name:
            continue
        if result in ("OK", "EXPECTEDFAIL"):
            cases[name] = "pass"
        elif result == "SKIP":
            cases[name] = "skipped"
        else:
            cases[name] = "failure"
    return cases


def attempts(name, make_cmd, read, cwd, env, limit, logs, report):
    """Runs a suite, rerunning once when it is killed or leaves no result. Returns (cases, killed)."""
    cases, killed = {}, False
    for attempt in (1, 2):
        log_path = os.path.join(logs, f"{name}-{attempt}.log")
        cmd, result_path = make_cmd(attempt)
        code, secs = run(cmd, cwd, env, limit, log_path)
        cases = read(result_path)
        killed = code is None
        rec = {"suite": name, "attempt": attempt, "exit": code, "seconds": secs, "killed": killed,
               "cases": len(cases),
               "not_passed": sorted(k for k, v in cases.items() if v != "pass")}
        report["runs"].append(rec)
        log(f"{name} attempt {attempt}: exit={code} {secs}s cases={len(cases)} "
            f"not_passed={rec['not_passed']}")
        if not killed and cases:
            break
    return cases, killed


def verdict(runs, reasons):
    entry = {"runs": runs}
    if runs and all(v == "pass" for v in runs):
        entry["verdict"] = "pass"
    elif runs and all(v == "missing" for v in runs):
        entry["verdict"] = "missing"
    elif runs and all(v == "skipped" for v in runs):
        entry["verdict"] = "skipped"
    else:
        entry["verdict"] = "failure"
    if reasons:
        entry["reason"] = ",".join(sorted(set(reasons)))
    return entry


def grade(args, prepare, manifest, report):
    work = Path(args.work)
    work.mkdir(parents=True, exist_ok=True)
    tree, build = str(work / "tree"), str(work / "build")
    shutil.rmtree(tree, ignore_errors=True)
    shutil.rmtree(build, ignore_errors=True)
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

    # 2. The verifier's own test file.
    for entry in manifest["test_files"]:
        dst = os.path.join(tree, entry["path"])
        have = sha256(dst) if os.path.isfile(dst) else None
        if have != entry["base_sha256"]:
            report.update(infra_fault=True, detail=f"base digest mismatch: {entry['path']}")
            return
        shutil.copyfile(os.path.join(args.graded, entry["name"]), dst)
        if sha256(dst) != entry["sha256"]:
            report.update(infra_fault=True, detail=f"graded digest mismatch: {entry['path']}")
            return

    # The scratch tree belongs to the uid that builds and tests it; meson creates the build
    # directory as that uid.
    if SANDBOX is not None:
        hand_over(tree, SANDBOX)

    # 3. Configure and build out of tree.
    bstate = {"ok": False, "steps": []}
    report["build"] = bstate
    env = base_env()
    deadline = time.monotonic() + BUILD_LIMIT
    steps = [("setup", ["meson", "setup", build, tree, *MESON_OPTIONS]),
             ("compile", ["ninja", "-C", build, "-j", JOBS])]
    for name, cmd in steps:
        left = deadline - time.monotonic()
        code, secs = (None, 0.0) if left <= 0 else run(
            cmd, tree, env, left, os.path.join(args.logs, f"build-{name}.log"))
        bstate["steps"].append({"step": name, "exit": code, "seconds": secs})
        log(f"build {name}: exit={code} {secs}s")
        if code is None:
            report.update(infra_fault=True, detail=f"build step {name} killed at its limit")
            return
        if code != 0:
            return

    intro = subprocess.run(["meson", "introspect", build, "--tests"], cwd=tree, env=env,
                           stdin=subprocess.DEVNULL, capture_output=True, timeout=120,
                           **sandbox_ids())
    try:
        tests = json.loads(intro.stdout.decode("utf-8"))
    except ValueError:
        tests = []
    pytest_def = next((t for t in tests if t.get("name") == "pytest"), None)
    meson_names = sorted({t["name"] for t in tests if t.get("name") and t["name"] != "pytest"})
    if pytest_def is None or not meson_names:
        bstate["detail"] = "meson introspect found no tests"
        return
    bstate["ok"] = True

    observed = report["observed"]
    p2p, f2p = {}, {}

    # 4. The meson test suite, without its pytest entry.
    mlog = os.path.join(build, "meson-logs", "testlog.json")

    def meson_cmd(_attempt):
        remove_as_sandbox(mlog)
        return (["meson", "test", "-C", build, "--no-rebuild", "--print-errorlogs",
                 "--num-processes", JOBS, *meson_names], mlog)

    cases, killed = attempts("meson-test", meson_cmd, read_meson_log, tree, env,
                             MESON_TEST_LIMIT, args.logs, report)
    for k, v in cases.items():
        observed.setdefault(f"meson/{k}", []).append(v)
    for unit in manifest["pass_to_pass"]:
        if unit.startswith("meson/"):
            v = cases.get(unit.split("/", 1)[1], "missing")
            p2p[unit] = verdict([v], ["suite_killed_at_limit" if killed else "no_result"]
                                if v == "missing" else [])

    # 5. The pytest suite, as the meson "pytest" test runs it, without the cross-version file.
    py_env = dict(env)
    py_env.update(pytest_def.get("env") or {})
    py_cwd = pytest_def.get("workdir") or os.path.join(tree, "test")
    py_base = [sys.executable, "-m", "pytest", "-p", "no:cacheprovider", "-vv", "--assert=plain",
               "-m", "not network"]

    def pytest_cmd(attempt):
        junit = os.path.join(str(work), f"pytest-{attempt}.xml")
        try:
            os.unlink(junit)
        except FileNotFoundError:
            pass
        return py_base + [f"--junitxml={junit}", f"--ignore={XV_FILE}"], junit

    cases, killed = attempts("pytest", pytest_cmd, read_junit, py_cwd, py_env, PYTEST_LIMIT,
                             args.logs, report)
    for k, v in cases.items():
        observed.setdefault(f"pytest/{k}", []).append(v)
    for unit in manifest["pass_to_pass"]:
        if unit.startswith("pytest/"):
            v = cases.get(unit.split("/", 1)[1], "missing")
            p2p[unit] = verdict([v], ["suite_killed_at_limit" if killed else "no_result"]
                                if v == "missing" else [])

    # 6. The cross-version file against each older release, XV_RUNS times.
    xv_runs = int(args.xv_runs or manifest.get("xv_runs", 3))
    per_unit, reasons = {}, {}
    for base in manifest["baselines"]:
        tag = base["tag"]
        xv_env = dict(py_env)
        xv_env["FC_BASELINE_BUILDDIR"] = base["builddir"]
        xv_env["FC_BASELINE_CACHE_VERSION"] = str(base["cache_version"])
        for run_no in range(1, xv_runs + 1):
            def xv_cmd(attempt, tag=tag, run_no=run_no):
                junit = os.path.join(str(work), f"xv-{tag}-{run_no}-{attempt}.xml")
                try:
                    os.unlink(junit)
                except FileNotFoundError:
                    pass
                return py_base + [f"--junitxml={junit}", XV_FILE], junit

            cases, killed = attempts(f"xv-{tag}-run{run_no}", xv_cmd, read_junit, py_cwd, xv_env,
                                     XV_LIMIT, args.logs, report)
            for k, v in cases.items():
                case = k.split("::", 1)[1] if "::" in k else k
                observed.setdefault(f"cross-version/{tag}/{case}", []).append(v)
            for unit in manifest["fail_to_pass"] + manifest["pass_to_pass"]:
                if not unit.startswith(f"cross-version/{tag}/"):
                    continue
                case = unit.split("/", 2)[2]
                v = cases.get(f"{XV_FILE}::{case}", "missing")
                per_unit.setdefault(unit, []).append(v)
                if v == "missing":
                    reasons.setdefault(unit, []).append(
                        "run_killed_at_limit" if killed else "no_result")
    for unit, runs in per_unit.items():
        entry = verdict(runs, reasons.get(unit, []))
        (f2p if unit in manifest["fail_to_pass"] else p2p)[unit] = entry
    for unit in manifest["fail_to_pass"]:
        f2p.setdefault(unit, verdict(["missing"], ["not_run"]))
    for unit in manifest["pass_to_pass"]:
        p2p.setdefault(unit, verdict(["missing"], ["not_run"]))

    report["pass_to_pass"] = dict(sorted(p2p.items()))
    report["fail_to_pass"] = dict(sorted(f2p.items()))
    if not args.keep:
        shutil.rmtree(tree, ignore_errors=True)
        shutil.rmtree(build, ignore_errors=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--prepare", default="/logs/verifier/prepare.json")
    ap.add_argument("--pristine", default="/opt/statea")
    ap.add_argument("--graded", default="/tests/graded")
    ap.add_argument("--work", default="/work")
    ap.add_argument("--logs", default="/logs/verifier")
    ap.add_argument("--json", default="/logs/verifier/report.json")
    ap.add_argument("--xv-runs", default=None)
    ap.add_argument("--keep", action="store_true")
    ap.add_argument("--sandbox", default="")
    args, _unknown = ap.parse_known_args()

    global SANDBOX
    report = {"schema": SCHEMA, "prepare": {"ok": False}, "infra_fault": False,
              "build": {"ok": False}, "runs": [], "pass_to_pass": {}, "fail_to_pass": {},
              "observed": {}}
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
                             "added_ignored": prepare.get("added_ignored")}
        manifest = json.loads(Path(args.graded, "manifest.json").read_text(encoding="utf-8"))
        Path(args.logs).mkdir(parents=True, exist_ok=True)
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
