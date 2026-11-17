"""The test_output dimension: one criterion scored from /logs/verifier/report.json and ctrf.json.

The score is 0 when any cheat check failed or the cheat report is missing, when the solver's sources
could not be taken, when the build failed, or when any pass-to-pass test failed. Otherwise it is the
share of the fail-to-pass tests that passed in every one of their FAIL_TO_PASS_RUNS runs:
passing_fail_to_pass / F2P_TOTAL. A failing fail-to-pass test costs only its own share.
A task passes only at 1.0. A missing or unparsable report scores 0 with a machine-readable reason;
nothing here raises.
"""

import json
from pathlib import Path

import rewardkit as rk

REPORT_PATH = "/logs/verifier/report.json"
CTRF_PATH = "/logs/verifier/ctrf.json"
MANIFEST_PATH = "/tests/graded/manifest.json"
REPORT_SCHEMA = "openraft.test-report/v1"
CHEAT_IDS = (
    "cheat_test_files_untouched",
    "cheat_no_process_exit",
    "cheat_no_external_network",
)
F2P_TOTAL = 7
FAIL_TO_PASS_RUNS = 3
P2P_TOTAL = 918


def _reason(code, **detail):
    return json.dumps({"reason": code, **detail}, sort_keys=True)


def _load(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def _score():
    try:
        report = _load(REPORT_PATH)
    except FileNotFoundError:
        return 0.0, _reason("report_missing")
    except (OSError, UnicodeDecodeError, ValueError):
        return 0.0, _reason("report_unparsable")
    if not isinstance(report, dict) or report.get("schema") != REPORT_SCHEMA:
        return 0.0, _reason("report_schema_mismatch")
    try:
        tests = _load(CTRF_PATH)["results"]["tests"]
        status = {t["name"]: t["status"] for t in tests}
    except (OSError, UnicodeDecodeError, ValueError, KeyError, TypeError):
        return 0.0, _reason("cheat_report_missing")
    unreported = sorted(set(CHEAT_IDS) - set(status))
    if unreported:
        return 0.0, _reason("cheat_report_incomplete", missing=unreported)
    fired = sorted(k for k, v in status.items() if v != "passed")
    if fired:
        return 0.0, _reason("cheat_check_failed", failed=fired)
    prepare, build = report.get("prepare"), report.get("build")
    if not isinstance(prepare, dict) or prepare.get("ok") is not True:
        return 0.0, _reason("submission_not_taken")
    if not isinstance(build, dict) or build.get("ok") is not True:
        return 0.0, _reason("build_failed")
    p2p, f2p = report.get("pass_to_pass"), report.get("fail_to_pass")
    if not isinstance(p2p, dict) or not isinstance(f2p, dict):
        return 0.0, _reason("report_malformed")
    try:
        manifest = _load(MANIFEST_PATH)
        want_p2p, want_f2p = set(manifest["pass_to_pass"]), set(manifest["fail_to_pass"])
    except (OSError, UnicodeDecodeError, ValueError, KeyError, TypeError):
        return 0.0, _reason("manifest_unreadable")
    if (len(want_p2p) != P2P_TOTAL or len(want_f2p) != F2P_TOTAL
            or set(p2p) != want_p2p or set(f2p) != want_f2p):
        return 0.0, _reason("graded_set_size_mismatch", p2p=len(p2p), f2p=len(f2p))
    broken = sorted(k for k, v in p2p.items()
                    if not isinstance(v, dict) or v.get("verdict") != "pass")
    if broken:
        return 0.0, _reason("pass_to_pass_failed", failing=broken)
    f2p_failing = sorted(k for k, v in f2p.items()
                         if not isinstance(v, dict) or v.get("verdict") != "pass"
                         or not isinstance(v.get("runs"), list)
                         or len(v["runs"]) != FAIL_TO_PASS_RUNS
                         or any(not isinstance(r, dict) or r.get("outcome") != "pass"
                                for r in v["runs"]))
    passed = F2P_TOTAL - len(f2p_failing)
    return passed / F2P_TOTAL, _reason("graded", f2p_passed=passed, f2p_total=F2P_TOTAL,
                                       f2p_failing=f2p_failing, p2p_passed=P2P_TOTAL)


@rk.criterion(description="0 on a failed build, any failed cheat check or any failing pass-to-pass test; otherwise the share of fail-to-pass tests passing in every one of their runs")
def graded_tests(workspace):
    try:
        score, reasoning = _score()
    except Exception as exc:  # never raise: the verifier must always reach the reward write
        score, reasoning = 0.0, _reason("internal_error", error=type(exc).__name__)
    return {"score": score, "reasoning": reasoning}


rk.graded_tests(weight=1.0, name="graded_tests")
