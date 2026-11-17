from __future__ import annotations

import json
from typing import Literal, TypedDict, assert_never

Outcome = Literal["pass", "fail", "skip", "not_run"]


class RawRun(TypedDict):
    timeout: bool
    exit_code: int | None
    stdout: str
    stderr: str
    wall_seconds: float


class ParsedRun(TypedDict):
    verdicts: dict[str, Outcome]
    executed_tests: list[str]
    suite_passed: bool
    suite_error: bool
    build_failed: bool
    timeout: bool
    exit_code: int | None
    wall_seconds: float
    unknown_tests: list[str]


class CandidateResult(TypedDict):
    results: dict[str, Outcome]
    executed_tests: list[str]
    suite_passed: bool
    suite_error: bool


def parse_run(raw: RawRun, tests: list[str]) -> ParsedRun:
    outcomes: dict[str, Outcome] = {}
    executed: set[str] = set()
    packages: dict[str, str] = {}
    build_failed = False
    for line in raw["stdout"].splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        action, name = event.get("Action"), event.get("Test")
        if action == "build-fail":
            build_failed = True
        if not name:
            if action in ("pass", "fail", "skip"):
                packages[event.get("Package", "")] = action
            continue
        if "/" in name:
            continue
        if action == "run":
            executed.add(name)
        if action in ("pass", "fail", "skip"):
            outcomes[name] = action
    build_failed = build_failed or "build failed" in raw["stderr"] or "cannot find package" in raw["stderr"]
    if raw["timeout"]:
        build_failed = False
    verdicts: dict[str, Outcome] = {}
    for name in tests:
        verdicts[name] = "fail" if raw["timeout"] or build_failed else outcomes.get(name, "not_run")
    suite_passed = (
        raw["exit_code"] == 0 and not raw["timeout"] and not build_failed
        and bool(packages) and all(action == "pass" for action in packages.values())
    )
    suite_error = (
        not raw["timeout"] and not build_failed and not suite_passed
        and not any(action == "fail" for action in outcomes.values())
    )
    return {
        "verdicts": verdicts, "executed_tests": sorted(executed),
        "suite_passed": suite_passed, "suite_error": suite_error,
        "build_failed": build_failed, "timeout": raw["timeout"],
        "exit_code": raw["exit_code"], "wall_seconds": raw["wall_seconds"],
        "unknown_tests": sorted(set(outcomes) - set(tests)),
    }


def combine_runs(runs: list[ParsedRun], tests: list[str]) -> CandidateResult:
    first, second = runs
    results: dict[str, Outcome] = {}
    for name in tests:
        a, b = first["verdicts"][name], second["verdicts"][name]
        results[name] = a if a == b else "fail"
    return {
        "results": results,
        "executed_tests": sorted(set(first["executed_tests"]) & set(second["executed_tests"])),
        "suite_passed": all(run["suite_passed"] for run in runs),
        "suite_error": any(run["suite_error"] for run in runs),
    }


def accepts_correct_candidate(entry: CandidateResult, tests: list[str]) -> bool:
    verdicts = [entry["results"][name] for name in tests]
    return (
        entry["suite_passed"]
        and any(entry["results"][name] == "pass" and name in entry["executed_tests"] for name in tests)
        and all(v in ("pass", "skip", "not_run") for v in verdicts)
    )


def rejects_incorrect_candidate(entry: CandidateResult, tests: list[str]) -> bool:
    return any(entry["results"][name] == "fail" for name in tests)


def witness_names(buggy: CandidateResult, gold: CandidateResult, tests: list[str]) -> list[str]:
    if gold["suite_error"]:
        return []
    executed = set(buggy["executed_tests"]) & set(gold["executed_tests"])
    return [name for name in tests if name in executed
            and buggy["results"][name] == "fail" and gold["results"][name] == "pass"]


def ctrf_status(outcome: Outcome) -> str:
    match outcome:
        case "pass":
            return "passed"
        case "fail":
            return "failed"
        case "skip" | "not_run":
            return "skipped"
        case unreachable:
            assert_never(unreachable)
