"""Compiled criteria over the verdict matrix. Each registration carries one item
identifier and one item weight; the relation alone lives here.
"""

from __future__ import annotations

import json
import os
from pathlib import Path

import rewardkit as rk
from rewardkit import criterion

from outcomes import accepts_correct_candidate, rejects_incorrect_candidate

_OUT = Path(os.environ.get("GRADE_OUT_DIR", "/logs/verifier"))
_MATRIX = _OUT / "verdict-matrix.json"

_ALL_PASS = "all-pass"
_ANY_FAIL = "any-fail"


def _matrix() -> dict | None:
    try:
        data = json.loads(_MATRIX.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return None
    return data if isinstance(data, dict) else None


def _blocked(matrix: dict | None) -> str:
    if matrix is None:
        return "no-verdict-matrix"
    fired = matrix.get("red_lines", {}).get("fired") or []
    if fired:
        return "red-line:" + ",".join(sorted(str(item) for item in fired))
    if not matrix.get("gate", {}).get("holds"):
        return "gate-not-held"
    if not matrix.get("collected_tests"):
        return "no-collected-test"
    return ""


@criterion(description="{item_id}")
def reward_item(workspace: Path, item_id: str, candidate: str, rule: str) -> dict:
    try:
        matrix = _matrix()
        reason = _blocked(matrix)
        if reason:
            return {"score": 0.0, "reasoning": reason}
        entry = (matrix.get("candidates") or {}).get(candidate)
        if not isinstance(entry, dict):
            return {"score": 0.0, "reasoning": "no-candidate:" + candidate}
        results = entry.get("results") or {}
        names = list(matrix["collected_tests"])
        verdicts = [results.get(name) for name in names]
        if any(verdict is None for verdict in verdicts):
            return {"score": 0.0, "reasoning": "incomplete-candidate:" + candidate}
        if rule == _ALL_PASS:
            ok = accepts_correct_candidate(entry, names)
        elif rule == _ANY_FAIL:
            ok = rejects_incorrect_candidate(entry, names)
        else:
            return {"score": 0.0, "reasoning": "unknown-rule:" + rule}
        return {
            "score": 1.0 if ok else 0.0,
            "reasoning": "{0}:{1}:{2}".format(item_id, candidate, rule),
        }
    except Exception as exc:  # a criterion that raises would void the whole reward
        return {"score": 0.0, "reasoning": "criterion-error:" + type(exc).__name__}


rk.reward_item("R-GOLD", "gold", "all-pass", weight=5, name="R-GOLD")
rk.reward_item("R-COR-A1", "cor-A1", "all-pass", weight=5, name="R-COR-A1")
rk.reward_item("R-COR-A3", "cor-A3", "all-pass", weight=5, name="R-COR-A3")
rk.reward_item("R-COR-A4", "cor-A4", "all-pass", weight=5, name="R-COR-A4")
rk.reward_item("R-COR-A5", "cor-A5", "all-pass", weight=5, name="R-COR-A5")
rk.reward_item("R-COR-R3e1f", "cor-R3e1f", "all-pass", weight=5, name="R-COR-R3e1f")
rk.reward_item("R-INC-I2", "inc-I2", "any-fail", weight=5, name="R-INC-I2")
rk.reward_item("R-INC-I3a", "inc-I3a", "any-fail", weight=5, name="R-INC-I3a")
rk.reward_item("R-INC-I4", "inc-I4", "any-fail", weight=5, name="R-INC-I4")
rk.reward_item("R-INC-I7", "inc-I7", "any-fail", weight=5, name="R-INC-I7")
rk.reward_item("R-INC-I9", "inc-I9", "any-fail", weight=5, name="R-INC-I9")
rk.reward_item("R-INC-I10", "inc-I10", "any-fail", weight=5, name="R-INC-I10")
rk.reward_item("R-INC-I11", "inc-I11", "any-fail", weight=5, name="R-INC-I11")
rk.reward_item("R-INC-I13", "inc-I13", "any-fail", weight=5, name="R-INC-I13")
rk.reward_item("R-INC-I15", "inc-I15", "any-fail", weight=5, name="R-INC-I15")
rk.reward_item("R-INC-R8f2c", "inc-R8f2c", "any-fail", weight=5, name="R-INC-R8f2c")
rk.reward_item("R-INC-R51da", "inc-R51da", "any-fail", weight=5, name="R-INC-R51da")
rk.reward_item("R-INC-I3br", "inc-I3br", "any-fail", weight=5, name="R-INC-I3br")
rk.reward_item("R-INC-I3cr", "inc-I3cr", "any-fail", weight=5, name="R-INC-I3cr")
rk.reward_item("R-INC-I14r", "inc-I14r", "any-fail", weight=5, name="R-INC-I14r")
rk.reward_item("R-INC-I8r", "inc-I8r", "any-fail", weight=5, name="R-INC-I8r")
rk.reward_item("R-INC-I16", "inc-I16", "any-fail", weight=5, name="R-INC-I16")
rk.reward_item("R-INC-I17", "inc-I17", "any-fail", weight=5, name="R-INC-I17")
rk.reward_item("R-INC-I18", "inc-I18", "any-fail", weight=5, name="R-INC-I18")
rk.reward_item("R-INC-I19", "inc-I19", "any-fail", weight=5, name="R-INC-I19")
rk.reward_item("R-INC-I20", "inc-I20", "any-fail", weight=5, name="R-INC-I20")
rk.reward_item("R-INC-I21", "inc-I21", "any-fail", weight=5, name="R-INC-I21")
rk.reward_item("R-INC-I22", "inc-I22", "any-fail", weight=5, name="R-INC-I22")
rk.reward_item("R-INC-I23", "inc-I23", "any-fail", weight=5, name="R-INC-I23")
rk.reward_item("R-INC-I24", "inc-I24", "any-fail", weight=5, name="R-INC-I24")
rk.reward_item("R-INC-I25", "inc-I25", "any-fail", weight=5, name="R-INC-I25")
