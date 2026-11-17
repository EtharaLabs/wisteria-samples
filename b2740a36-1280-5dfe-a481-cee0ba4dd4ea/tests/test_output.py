"""The test_output dimension: one criterion scored from /logs/verifier/report.json and ctrf.json.

The score is 0 when any cheat check failed or the cheat report is missing, when the solver's sources
could not be taken, when the build failed, or when any pass-to-pass test did not pass (fontconfig's
meson test suite, its pytest suite, and the cross-version cases that already pass on the base
revision). Otherwise it is the fraction of the graded cross-version cases that passed in every one
of their three runs (a task passes only at 1.0). A missing or unparsable report scores 0 with a machine-readable reason; nothing here
raises.
"""

import json
from pathlib import Path

import rewardkit as rk

REPORT_PATH = "/logs/verifier/report.json"
CTRF_PATH = "/logs/verifier/ctrf.json"
MANIFEST_PATH = "/tests/graded/manifest.json"
REPORT_SCHEMA = "fontconfig-grader.test-report/v1"
CHEAT_IDS = (
    "cheat_test_files_untouched",
    "cheat_no_process_exit",
    "cheat_no_external_network",
)
F2P_TOTAL = 4
P2P_TOTAL = 83


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
        want_p2p = set(manifest["pass_to_pass"]) | set(manifest.get("f2p_scored_as_p2p", []))
        want_f2p = set(manifest["fail_to_pass"])
    except (OSError, UnicodeDecodeError, ValueError, KeyError, TypeError):
        return 0.0, _reason("manifest_unreadable")
    if (len(want_p2p) != P2P_TOTAL or len(want_f2p) != F2P_TOTAL
            or set(p2p) != want_p2p or set(f2p) != want_f2p):
        return 0.0, _reason("graded_set_size_mismatch", p2p=len(p2p), f2p=len(f2p))
    broken = sorted(k for k, v in p2p.items()
                    if not isinstance(v, dict) or v.get("verdict") != "pass")
    if broken:
        return 0.0, _reason("pass_to_pass_failed", failing=broken)
    passed = sorted(k for k, v in f2p.items() if isinstance(v, dict) and v.get("verdict") == "pass")
    failing = sorted(set(f2p) - set(passed))
    return len(passed) / F2P_TOTAL, _reason("graded", f2p_passed=len(passed), f2p_total=F2P_TOTAL,
                                            f2p_failing=failing, p2p_passed=len(p2p))


@rk.criterion(description="0 on a failed build, any failed pass-to-pass test or any failed cheat check; otherwise the fraction of graded cross-version cases that pass in all three runs")
def graded_tests(workspace):
    try:
        score, reasoning = _score()
    except Exception as exc:  # never raise: the verifier must always reach the reward write
        score, reasoning = 0.0, _reason("internal_error", error=type(exc).__name__)
    return {"score": score, "reasoning": reasoning}


rk.graded_tests(weight=1.0, name="graded_tests")
