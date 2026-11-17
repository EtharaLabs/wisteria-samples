"""The compiled grading channel.

Each criterion is one relation over the per-step reports tests/test.sh wrote,
conjoined with the admission gate GATE names, so that the regression guards
can take credit away from a repair but can never earn any on their own.
No criterion carries prose, a reference value or any part of the repair, and
no criterion observes a fact the judged channel also observes.  Importing
this module registers the criteria and does nothing else.
"""

from __future__ import annotations

import json
from pathlib import Path

from rewardkit import criteria, criterion

REPORTS = Path("/logs/verifier/reports")
FILES = {
    "build": "build.json",
    "graded": "graded.json",
    "passtopass": "passtopass.json",
    "replay": "replay.json",
    "stage": "stage.json",
}
DISCRIMINATORS = {
    "inflight": "TestJetStreamMirrorResetWithDeliveriesInFlight",
    "mirror": "TestJetStreamDurableStreamSourcingStaleDeliveryAfterReset/Mirror",
    "pending": "TestJetStreamConsumerResetPendingHoldsDeliveries",
    "source": "TestJetStreamDurableStreamSourcingStaleDeliveryAfterReset/Source",
}
PASSTOPASS = [
    "TestJetStreamConsumerFlowControlResetOnLeaderChange",
    "TestJetStreamFlowControlRequiresHeartbeats",
    "TestJetStreamSourceStartSeqRespectedOnEmptyOrigin",
    "TestJetStreamSourceBasics",
    "TestJetStreamDurableStreamMirrorAndSourceIncorrectConsumerConfig",
    "TestJetStreamDurableStreamSourcesWithUniqueConsumerNames",
    "TestJetStreamMirrorProcessInboundNilDeref",
    "TestJetStreamMirrorConsumerRetryUsesFreshReplySubject",
    "TestJetStreamSourceConsumerRetryUsesFreshReplySubject",
    "TestJetStreamSourceConsumerUsesScopedAPIReplySubject",
    "TestJetStreamMirrorRetriesOnSeqAlreadySeenMismatch",
]
TOPOLOGIES = ["n3", "n1"]
EXACTLY_ONCE = ["exactly_once_mirror", "exactly_once_source", "mirror_last_seq", "no_deleted"]
CONSTRAINTS = {
    "duplicate_refused": "c3_duplicate_refused",
    "flow_control": "c1_flow_control_active",
    "not_recreated": "c2_not_recreated",
    "stored_unchanged": "c4_stored_unchanged",
}

# The admission gate. Every compiled criterion is conjoined with these
# relations, because the rest are regression guards that already hold on an
# unrepaired tree and so must never earn credit on their own.
GATE = ["discriminator-mirror", "discriminator-source", "discriminator-pending"]

# The anti-cheat declaration tests/cheat-checks.json names, as a map from the
# check name to the kind of evidence that decides it. tests/report.py decides
# each and tests/test.sh writes every one, once, into the CTRF report
# /logs/verifier/ctrf.json. A check reads `failed` only when the submission's
# own bytes show a cheat; none carries reward weight or reads the gate.
CHEAT_CHECKS = {
    "no_symlink_leaves_tree": "stage-symlink",
    "no_test_harness_detection": "stage-harness",
    "reward_files_untouched": "runtime-reward-files",
    "step_reports_untouched": "runtime-step-reports",
}

# The pattern the stage-harness check counts in every non-test .go file: an
# import of the testing package or a lookup that tells code it runs under
# go test. \x22 stands for the double quote each alternative matches.
CHEAT_PATTERN = "\\x22testing\\x22|testing\\.Testing\\(|flag\\.Lookup\\(\\x22test\\.|\\.test\\x22"

# How often each base-commit file already matches that pattern; a submission
# fails the check only where a file holds more matches than this.
CHEAT_BASELINE = {
    "internal/antithesis/noop.go": 1,
    "internal/antithesis/test_assert.go": 1,
    "internal/ocsp/ocsp.go": 1,
    "internal/testhelper/logging.go": 1,
    "test/test.go": 1,
}

# What tests/test.sh must run to produce the reports above. Execution
# parameters only: no criterion reads PLAN, and PLAN names no outcome.
PLAN = {
    "note": "/app/diagnosis.md",
    "package": "./server",
    "raw_root": "/logs/verifier/raw",
    "replay_binary": "/opt/replay/replay",
    "replay_load": 0,
    "replay_seed": 1,
    "report_files": {
        "build": "build.json",
        "graded": "graded.json",
        "passtopass": "passtopass.json",
        "replay": "replay.json",
        "stage": "stage.json",
    },
    "reports_root": "/logs/verifier/reports",
    "topologies": [
        {"id": "n3", "nodes": 3, "timeline": "stale-after-reset"},
        {"id": "n1", "nodes": 1, "timeline": "stale-after-reset-1n"},
    ],
    "tree": "/app/nats-server",
}


def _report(stem: str) -> dict[str, object]:
    """Return one step report, or an empty mapping when it is missing or not
    JSON.  A report that cannot be read is a defined absence, never a crash."""
    path = REPORTS / FILES[stem]
    if not path.is_file():
        return {}
    raw = path.read_text(encoding="utf-8", errors="replace")
    try:
        loaded = json.loads(raw)
    except json.JSONDecodeError:
        return {}
    return loaded if isinstance(loaded, dict) else {}


def _ok(report: dict[str, object]) -> bool:
    return report.get("ok") is True


def _leaf(stem: str, name: str) -> bool:
    report = _report(stem)
    if not _ok(report):
        return False
    tests = report.get("tests")
    if not isinstance(tests, dict):
        return False
    return tests.get(name) == "pass"


def _verdicts() -> dict[str, dict[str, object]]:
    report = _report("replay")
    if not _ok(report):
        return {}
    found = report.get("topologies")
    if not isinstance(found, dict):
        return {}
    if sorted(found) != sorted(TOPOLOGIES):
        return {}
    out: dict[str, dict[str, object]] = {}
    for name, body in found.items():
        if not isinstance(body, dict):
            return {}
        out[name] = body
    return out


def _checks(keys: list[str]) -> bool:
    verdicts = _verdicts()
    if not verdicts:
        return False
    for body in verdicts.values():
        checks = body.get("checks")
        if not isinstance(checks, dict):
            return False
        for key in keys:
            if checks.get(key) is not True:
                return False
    return True


def discriminator(which: str) -> bool:
    return _leaf("graded", DISCRIMINATORS[which])


def all_passtopass() -> bool:
    return all(_leaf("passtopass", name) for name in PASSTOPASS)


def exactly_once_both_topologies() -> bool:
    return _checks(EXACTLY_ONCE)


def constraint(which: str) -> bool:
    return _checks([CONSTRAINTS[which]])


_RELATIONS = {
    "all-passtopass": all_passtopass,
    "exactly-once-both-topologies": exactly_once_both_topologies,
}


def relation_holds(relation: str) -> bool:
    """Hold one named relation against the step reports.  A relation no
    criterion defines is a KeyError, never a silent false."""
    if relation in _RELATIONS:
        return _RELATIONS[relation]()
    kind, _, which = relation.partition("-")
    if kind == "discriminator":
        return discriminator(which)
    if kind == "constraint":
        return constraint(which)
    raise KeyError(relation)


def gate_holds() -> bool:
    """Hold every relation GATE names.

    GATE names the relations only a real repair can satisfy.  Every other
    compiled relation is a regression guard that already holds on an
    unrepaired tree, so without this gate a guard would pay a submission that
    changed nothing.  Conjoining each criterion with the gate leaves a guard
    able to take credit away from a repair and unable to earn any by itself.
    """
    return all(relation_holds(name) for name in GATE)


@criterion(description="The compiled relation {relation} holds and the repair gate holds.")
def compiled(workspace: Path, relation: str) -> bool:
    """Hold the relation named by `relation` against the step reports, and hold
    the admission gate.  The workspace is not read: every graded fact was
    already reduced to a report by tests/test.sh, in the one staged tree the
    verifier built and ran.

    The relation is evaluated before the gate, so an unknown relation is an
    error whether the gate holds or not.
    """
    del workspace
    holds = relation_holds(relation)
    return holds and gate_holds()


# Channel weights:
#   the compiled DIMENSION: positive weights sum to 50; aggregation weighted-sum; weight
#   1/(2*50) = 0.01; a perfect compiled run contributes 50 * 0.01 = 0.5.
#   the judged DIMENSION: positive weights sum to 52; aggregation weighted-mean, which divides
#   by 52 inside the channel, so 1/(2*52) * 52 = 0.5; a perfect judged run contributes 0.5.
#   Overall perfect reward 1.0.
#   the compiled SCORE: the same compiled dimension alone on a 0-1 scale, the judge-free number
#   published when no judge key is set; weight 1/50 = 0.02 and the judged dimension pinned to 0.0, so a
#   perfect compiled run scores 50 * 0.02 = 1.0 and an unrepaired tree scores 0.0.
#   It decides no pass.

criteria.compiled("discriminator-mirror", weight=5, name="R01")
criteria.compiled("discriminator-source", weight=5, name="R02")
criteria.compiled("discriminator-pending", weight=5, name="R03")
criteria.compiled("all-passtopass", weight=5, name="R04")
criteria.compiled("exactly-once-both-topologies", weight=5, name="R05")
criteria.compiled("constraint-flow_control", weight=5, name="R10")
criteria.compiled("constraint-not_recreated", weight=5, name="R11")
criteria.compiled("constraint-duplicate_refused", weight=5, name="R12")
criteria.compiled("constraint-stored_unchanged", weight=5, name="R13")
criteria.compiled("discriminator-inflight", weight=5, name="R14")
