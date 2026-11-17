# Golden trajectory

Objective: Write Go tests in the collected package that reproduce the reference transition and discriminate correct fixes of the pod-cache leak from incorrect ones.

Invocation contract: instruction.md:50 names `go test -count=1 ./internal/kube/...` run from /app/opentelemetry-collector-contrib/processor/k8sattributesprocessor. The grader runs `go test -count=1 -json ./internal/kube/...` from its own copy of that module at /work/grade/module, with `GOFLAGS=-mod=vendor GOPROXY=off GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0`, as uid 20002, twice per candidate, each run limited to 120 seconds.

Collected surface: the package directory's *_test.go files and testdata/** of `/app/opentelemetry-collector-contrib/processor/k8sattributesprocessor/internal/kube`.

Reference test file: `stale_associations_test.go`.

## Checker set

| identifier | obligation | dimension | weight | negate | evaluation target | mode |
| --- | --- | --- | --- | --- | --- | --- |
| R-GOLD | D-1 | outcome | 5 | false | verifier_candidate_results | compiled |
| R-COR-A1 | D-1 | constraints | 5 | false | verifier_candidate_results | compiled |
| R-COR-A3 | D-1 | constraints | 5 | false | verifier_candidate_results | compiled |
| R-COR-A4 | D-1 | constraints | 5 | false | verifier_candidate_results | compiled |
| R-COR-A5 | D-1 | constraints | 5 | false | verifier_candidate_results | compiled |
| R-COR-R3e1f | D-1 | constraints | 5 | false | verifier_candidate_results | compiled |
| R-INC-I2 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I3a | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I4 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I7 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I9 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I10 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I11 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I13 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I15 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-R8f2c | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-R51da | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I3br | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I3cr | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I14r | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I8r | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I16 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I17 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I18 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I19 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I20 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I21 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I22 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I23 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I24 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-INC-I25 | D-1 | coverage | 5 | false | verifier_candidate_results | compiled |
| R-JUDGE-CUSTOM-STALE | D-1 | coverage | 5 | false | submitted_test_files | judged |
| R-JUDGE-RESTORED | D-1 | coverage | 5 | false | submitted_test_files | judged |
| R-JUDGE-RESTORED-CUSTOM | D-1 | coverage | 3 | false | submitted_test_files | judged |
| R-JUDGE-COME-AND-GO | D-1 | coverage | 3 | false | submitted_test_files | judged |
| R-JUDGE-LIVE-KEYS | D-1 | coverage | 3 | false | submitted_test_files | judged |

Committed identifier set: `R-COR-A1`, `R-COR-A3`, `R-COR-A4`, `R-COR-A5`, `R-COR-R3e1f`, `R-GOLD`, `R-INC-I10`, `R-INC-I11`, `R-INC-I13`, `R-INC-I14r`, `R-INC-I15`, `R-INC-I16`, `R-INC-I17`, `R-INC-I18`, `R-INC-I19`, `R-INC-I2`, `R-INC-I20`, `R-INC-I21`, `R-INC-I22`, `R-INC-I23`, `R-INC-I24`, `R-INC-I25`, `R-INC-I3a`, `R-INC-I3br`, `R-INC-I3cr`, `R-INC-I4`, `R-INC-I7`, `R-INC-I8r`, `R-INC-I9`, `R-INC-R51da`, `R-INC-R8f2c`, `R-JUDGE-COME-AND-GO`, `R-JUDGE-CUSTOM-STALE`, `R-JUDGE-LIVE-KEYS`, `R-JUDGE-RESTORED`, `R-JUDGE-RESTORED-CUSTOM`.

Gate (`gate.holds` in tests/grade.py): at least one executed collected test fails on the buggy tree and passes on the gold fix, the gold fix completes the whole suite successfully with no failing collected test, and no red line fired. This is the usual success rule for generated issue tests: a test must reproduce the issue on the unfixed code, and no test may fail on a correct fix. Every compiled criterion scores 0 when the gate does not hold; the advisory judged criteria do not read it and carry no weight in the reward.

## Judged part (advisory)

Judge: `anthropic/claude-sonnet-5-5` (with only an OpenAI key set, tests/test.sh has Reward Kit judge the same rubric with `openai/gpt-6-astra`) in individual mode, 3 samples, guard penalize, aggregation weighted-mean, rubric `tests/judge.toml` (installed at `/opt/judge/judge.toml`). Input: tests/test.sh copies, before any submitted code runs, each regular *_test.go file directly in the collected package directory whose name does not start with . or _ and that the solver added (whole) or changed (as a unified diff against the unchanged upstream file of the same name in tests/base-module.tar.gz) into the root-only directory /work/grade/out/judge/input; unchanged upstream test files are left out. With none, that directory holds one empty marker file and every criterion scores 0.

Weight in the reward: 0. The reward is the compiled score of tests/test_output.py alone, computed by the Reward Kit run over /tests, which finishes before the judge starts. reward.json always holds exactly that one key, {"reward": x}. When the judge gives a usable result, tests/test.sh writes the side file judged.json under /logs/verifier with compiled, judged (this part's weighted mean), the guard flag and the per-criterion votes; with no judge key, a judge failure, a judge time-out or an unusable result it writes judge-skipped.json and the judge_unavailable marker instead. Neither file is read as the reward, so the reward is the same in every judge case.

Judged items: `R-JUDGE-CUSTOM-STALE`, `R-JUDGE-RESTORED`, `R-JUDGE-RESTORED-CUSTOM`, `R-JUDGE-COME-AND-GO`, `R-JUDGE-LIVE-KEYS`.

## Ordered path

### S-1

- Action: Read the issue in instruction.md and the frozen handler and sweep identifiers it names.
- Established state: The environment holds the buggy tree at the base commit with vendored modules and a pre-warmed build cache.
- Mutation or silent drift to survive: none
- Deliverable: D-1
- Satisfied checkers: `R-GOLD`

### S-2

- Action: Drive handlePodAdd, handlePodUpdate, handlePodDelete and deleteLoopProcessing(gracePeriod), observing only through GetPod and the Pods map, so each test states one observable behaviour.
- Established state: The client is constructed with New(...) and a zero-valued InformersFactoryList, with no cluster and no network.
- Mutation or silent drift to survive: none
- Deliverable: D-1
- Satisfied checkers: `R-GOLD`

### S-3

- Action: Make at least one test fail on the buggy tree and pass on the gold fix, which is the reference transition the gate reads.
- Established state: The buggy sweep checks only the pod UID, so a stale identifier of a reactivated key is evicted while live.
- Mutation or silent drift to survive: none
- Deliverable: D-1
- Satisfied checkers: `R-GOLD`

### S-4

- Action: Assert only through the exported surface, so every independent correct design also passes.
- Established state: The correct pool holds five designs that reach the same observable behaviour by different internal means.
- Mutation or silent drift to survive: none
- Deliverable: D-1
- Satisfied checkers: `R-COR-A1`, `R-COR-A3`, `R-COR-A4`, `R-COR-A5`, `R-COR-R3e1f`

### S-5

- Action: Cover every behaviour D-1 to D-8, so a fix that is partial along any one axis is rejected.
- Established state: The incorrect pool holds twenty-five designs, each violating one named defined behaviour.
- Mutation or silent drift to survive: none
- Deliverable: D-1
- Satisfied checkers: `R-INC-I2`, `R-INC-I3a`, `R-INC-I4`, `R-INC-I7`, `R-INC-I9`, `R-INC-I10`, `R-INC-I11`, `R-INC-I13`, `R-INC-I15`, `R-INC-R8f2c`, `R-INC-R51da`, `R-INC-I3br`, `R-INC-I3cr`, `R-INC-I14r`, `R-INC-I8r`, `R-INC-I16`, `R-INC-I17`, `R-INC-I18`, `R-INC-I19`, `R-INC-I20`, `R-INC-I21`, `R-INC-I22`, `R-INC-I23`, `R-INC-I24`, `R-INC-I25`, `R-JUDGE-CUSTOM-STALE`, `R-JUDGE-RESTORED`, `R-JUDGE-RESTORED-CUSTOM`, `R-JUDGE-COME-AND-GO`, `R-JUDGE-LIVE-KEYS`

### S-6

- Action: Submit only *_test.go files and testdata inside the collected package directory, with no repository metadata, no prebuilt artifact, and no write to a verifier path.
- Established state: The grader collects that directory alone and re-hashes every sealed candidate tree after grading.
- Mutation or silent drift to survive: none
- Deliverable: D-1, within the red lines below
- Satisfied checkers: `R-GOLD`

## Behaviours a fix must meet, and where the instruction states them

The issue text is instruction.md:2-43, and the rules paragraph is instruction.md:50. The instruction states no behaviour beyond the issue text: D-3, D-4 and the concurrent case of D-8 are read from the repository, as each entry says. Each entry names where the instruction states the behaviour and where the repository establishes it. Anchors are paths under processor/k8sattributesprocessor/ in the checkout at the base commit.

- `D-1` Stale identifiers survive the pod deletion grace period and are evicted by the delete sweep once it elapses. Instruction: the issue, :11 (old entries "are never cleared from memory"), :16 (the same leak for custom association sources) and :21 (a dropped identifier queues a delayed delete that runs "after the grace period expires"). Repository: pod_delete_grace_period, config.go:57, default 120 seconds, factory.go:55; deleteLoopProcessing(gracePeriod) in internal/kube/client.go, run every 30 seconds.
- `D-2` Cleanup covers every identifier an association produces, including the connection and k8s.pod.ip keys of a changed pod IP. Instruction: the issue, :21, names "a `container.id` or pod IP" among the identifiers that disappear, with :11 and :16 for the leak. That one pod address yields two keys, the connection key and the k8s.pod.ip key, is not stated in the instruction; it is read from the repository, internal/kube/client.go:1602-1609, and the delete path's IP keys in forgetPod.
- `D-3` An update older than the cached pod (an older StartTime) triggers no cleanup. Instruction: not stated; read from the repository. Repository: addOrUpdatePod's StartTime comparison, internal/kube/client.go:1629-1637, with the comment at 1642-1645.
- `D-4` A key now owned by another pod is never deleted. Instruction: not stated beyond the issue, :21 (the sweep "only checks that the cached pod still has the same UID"); read from the repository. Repository: the UID ownership checks, internal/kube/client.go:786 and :1683, and the existing TestDeleteQueue, internal/kube/client_test.go:1587.
- `D-5` A value change makes the old value stale, not only a removal. Instruction: the issue, :16 and :23 (a custom source on a mutable label or annotation whose value comes and goes). Repository: identifiers are source and value pairs, getIdentifiersFromAssoc, internal/kube/client.go:1520.
- `D-6` An update identical to the current pod does not rescue a stale key. Instruction: the issue, :21, scopes cancellation to an update that restores the same identifier.
- `D-7` The pod UID key and the pod's other live keys stay. Instruction: the issue, :21 (removing an active association "breaks lookups until another update repopulates it"). Repository: getIdentifiersFromAssoc always appends the k8s.pod.uid key, internal/kube/client.go:1594-1598.
- `D-8` An identifier restored before cleanup is not removed, including a restore that arrives while a cleanup pass is already running and waiting for the cache lock. Instruction: the issue, :21 (a restore "before cleanup"); that this includes a restore arriving while a cleanup pass is running is read from the repository's locking, not stated.

## Criterion trace

Each criterion traces to a sentence of instruction.md (the issue text or the rules paragraph at :50) and, where useful, to the repository code that defines the behaviour (D-1 to D-8 above). Anchors are paths under processor/k8sattributesprocessor/ in the checkout at the base commit; witnesses are reference tests from the oracle run.

- `R-GOLD` instruction.md:1 and :44: the tests verify a proposed solution; the gate rule: at least one test fails on the buggy tree and passes on the fix, and no test fails on the fix; instruction.md:50: every test must give the same result each time it runs (the grader counts a test whose two runs disagree as failing). Witness: the gate transition of the reference tests TestReactivatedContainerIDSurvivesSweep, TestReactivatedLabelSurvivesSweepThenEvictedWhenRemovedAgain, TestResyncAfterRemovalDoesNotRescueStaleKey, TestStaleAnnotationKeyEvictedAfterGracePeriod, TestStaleLabelChangeThenDeleteLeavesNoKey, TestStaleLabelKeyEvictedAfterGracePeriod, TestStaleLabelValueChangeEvictsOldValue, TestStalePodIPKeysEvictedAfterGracePeriod; every reference test passes on the gold fix in both runs.
- `R-COR-A1` instruction.md:44: the tests check whether an implemented solution resolves the issue, so a correct fix by another design passes every collected test; instruction.md:50: every test must give the same result each time it runs (the grader counts a test whose two runs disagree as failing); the gate rule: no test fails on a correct patch. Witness: every reference test passes on this candidate in both runs.
- `R-COR-A3` instruction.md:44: the tests check whether an implemented solution resolves the issue, so a correct fix by another design passes every collected test; instruction.md:50: every test must give the same result each time it runs (the grader counts a test whose two runs disagree as failing); the gate rule: no test fails on a correct patch. Witness: every reference test passes on this candidate in both runs.
- `R-COR-A4` instruction.md:44: the tests check whether an implemented solution resolves the issue, so a correct fix by another design passes every collected test; instruction.md:50: every test must give the same result each time it runs (the grader counts a test whose two runs disagree as failing); the gate rule: no test fails on a correct patch. Witness: every reference test passes on this candidate in both runs.
- `R-COR-A5` instruction.md:44: the tests check whether an implemented solution resolves the issue, so a correct fix by another design passes every collected test; instruction.md:50: every test must give the same result each time it runs (the grader counts a test whose two runs disagree as failing); the gate rule: no test fails on a correct patch. Witness: every reference test passes on this candidate in both runs.
- `R-COR-R3e1f` instruction.md:44: the tests check whether an implemented solution resolves the issue, so a correct fix by another design passes every collected test; instruction.md:50: every test must give the same result each time it runs (the grader counts a test whose two runs disagree as failing); the gate rule: no test fails on a correct patch. Witness: every reference test passes on this candidate in both runs.
- `R-INC-I2` Issue, instruction.md:16: custom association sources such as a label or an annotation leak like container.id; D-1. Witness: TestReactivatedLabelSurvivesSweepThenEvictedWhenRemovedAgain, TestResyncAfterRemovalDoesNotRescueStaleKey, TestStaleAnnotationKeyEvictedAfterGracePeriod, TestStaleLabelChangeThenDeleteLeavesNoKey, TestStaleLabelKeyEvictedAfterGracePeriod, TestStaleLabelValueChangeEvictsOldValue, TestStalePodIPKeysEvictedAfterGracePeriod fail on this candidate in both runs.
- `R-INC-I3a` Issue, instruction.md:16: annotation-backed association sources leak like container.id; D-1. Witness: TestStaleAnnotationKeyEvictedAfterGracePeriod fail on this candidate in both runs.
- `R-INC-I4` D-5, repository: identifiers are source and value pairs, internal/kube/client.go:1520; the issue, instruction.md:23, has the value of a mutable field come and go. Witness: TestStaleLabelValueChangeEvictsOldValue fail on this candidate in both runs.
- `R-INC-I7` D-6: the issue, instruction.md:21, scopes cancellation to an update that restores the same identifier, so an identical resync does not rescue a stale key. Witness: TestResyncAfterRemovalDoesNotRescueStaleKey fail on this candidate in both runs.
- `R-INC-I9` D-4: not stated in the instruction; repository: the sweep deletes only while the cached pod keeps the queued UID, internal/kube/client.go:786, forgetPod queues only keys the pod still owns, internal/kube/client.go:1683, and the existing TestDeleteQueue, internal/kube/client_test.go:1587, so a key now owned by another pod is never deleted. Witness: TestSharedLabelValueKeptForOtherPod fail on this candidate in both runs.
- `R-INC-I10` D-1, repository: pod_delete_grace_period, config.go:57, default 120 seconds, factory.go:55; the issue, instruction.md:21, has the delayed delete run after the grace period expires. Witness: TestStaleLabelKeyResolvesInsideGracePeriod fail on this candidate in both runs.
- `R-INC-I11` D-7: the issue, instruction.md:21, where removing an active association "breaks lookups"; repository: getIdentifiersFromAssoc always appends the k8s.pod.uid key for backward compatibility, internal/kube/client.go:1594-1598, so the pod UID key and the pod's other live keys stay. Witness: TestStaleLabelEvictionKeepsLiveKeys fail on this candidate in both runs.
- `R-INC-I13` Issue, instruction.md:11 and :16: stale entries must be cleared from memory, also when the pod is deleted after the change; D-1. Witness: TestStaleLabelChangeThenDeleteLeavesNoKey fail on this candidate in both runs.
- `R-INC-I15` Issue, instruction.md:16, with D-2: every configured association produces identifiers, internal/kube/client.go:1520, so a later association of the same source kind is cleaned up too. Witness: TestStaleLabelKeyEvictedAfterGracePeriod fail on this candidate in both runs.
- `R-INC-R8f2c` D-8: the issue, instruction.md:21 (the concurrent case is read from the repository, not stated): an identifier restored before cleanup must not be removed, including a restore that arrives while cleanup is already under way (the sweep waits for the cache lock). Witness: TestStaleAnnotationKeyReactivatedDuringConcurrentSweepSurvives fail on this candidate in both runs.
- `R-INC-R51da` D-8: the issue, instruction.md:21 (the concurrent case is read from the repository, not stated): an identifier restored before cleanup must not be removed, including a restore that arrives while cleanup is already under way (the sweep waits for the cache lock). Witness: TestStaleAnnotationKeyReactivatedDuringConcurrentSweepSurvives fail on this candidate in both runs.
- `R-INC-I3br` D-2, repository: the connection and k8s.pod.ip keys come from the pod address, internal/kube/client.go:1602-1609; the issue names the pod IP, instruction.md:21. Witness: TestStalePodIPKeysEvictedAfterGracePeriod fail on this candidate in both runs.
- `R-INC-I3cr` Issue, instruction.md:21: a container.id that disappears in one informer update and reappears before cleanup must survive the sweep. Witness: TestReactivatedContainerIDSurvivesSweep fail on this candidate in both runs.
- `R-INC-I14r` Issue, instruction.md:21 and :23: a label-backed identifier that comes and goes must survive the sweep while it is active. Witness: TestReactivatedLabelSurvivesSweepThenEvictedWhenRemovedAgain fail on this candidate in both runs.
- `R-INC-I8r` D-3: not stated in the instruction; repository: addOrUpdatePod skips an update whose StartTime is older than the cached pod's, internal/kube/client.go:1629-1637 with the comment at 1642-1645, so an out-of-order update triggers no cleanup. Witness: TestOutOfOrderUpdateTriggersNoCleanup fail on this candidate in both runs.
- `R-INC-I16` D-2, repository: the connection and k8s.pod.ip keys come from the pod address, internal/kube/client.go:1602-1609; the issue names the pod IP, instruction.md:21. Witness: TestStalePodIPKeysEvictedAfterGracePeriod fail on this candidate in both runs.
- `R-INC-I17` D-8: the issue, instruction.md:21 (the concurrent case is read from the repository, not stated): an identifier restored before cleanup must not be removed, including a restore that arrives while cleanup is already under way (the sweep waits for the cache lock). Witness: TestStaleAnnotationKeyReactivatedDuringConcurrentSweepSurvives fail on this candidate in both runs.
- `R-INC-I18` D-8: the issue, instruction.md:21 (the concurrent case is read from the repository, not stated): an identifier restored before cleanup must not be removed, including a restore that arrives while cleanup is already under way (the sweep waits for the cache lock). Witness: TestStaleAnnotationKeyReactivatedDuringConcurrentSweepSurvives fail on this candidate in both runs.
- `R-INC-I19` D-8: the issue, instruction.md:21 (the concurrent case is read from the repository, not stated): an identifier restored before cleanup must not be removed, including a restore that arrives while cleanup is already under way (the sweep waits for the cache lock). Witness: TestStaleAnnotationKeyReactivatedDuringConcurrentSweepSurvives fail on this candidate in both runs.
- `R-INC-I20` D-2, repository: the connection and k8s.pod.ip keys come from the pod address, internal/kube/client.go:1602-1609; the issue names the pod IP, instruction.md:21. Witness: TestStalePodIPKeysEvictedAfterGracePeriod fail on this candidate in both runs.
- `R-INC-I21` D-2, repository: the connection and k8s.pod.ip keys come from the pod address, internal/kube/client.go:1602-1609; the issue names the pod IP, instruction.md:21. Witness: TestStalePodIPKeysEvictedAfterGracePeriod fail on this candidate in both runs.
- `R-INC-I22` D-2: the issue names the pod IP, instruction.md:21; repository: the connection key and the k8s.pod.ip key are separate identifiers of one address, internal/kube/client.go:1602-1609, and both are cleaned up. That one address yields two keys is not stated in the instruction. Witness: TestStalePodIPKeysEvictedAfterGracePeriod fail on this candidate in both runs.
- `R-INC-I23` D-3: not stated in the instruction; repository: addOrUpdatePod skips an update whose StartTime is older than the cached pod's, internal/kube/client.go:1629-1637 with the comment at 1642-1645, so an out-of-order update triggers no cleanup. Witness: TestOutOfOrderUpdateTriggersNoCleanup fail on this candidate in both runs.
- `R-INC-I24` D-3: not stated in the instruction; repository: addOrUpdatePod skips an update whose StartTime is older than the cached pod's, internal/kube/client.go:1629-1637 with the comment at 1642-1645, so an out-of-order update triggers no cleanup. Witness: TestOutOfOrderUpdateTriggersNoCleanup fail on this candidate in both runs.
- `R-INC-I25` D-3: not stated in the instruction; repository: addOrUpdatePod skips an update whose StartTime is older than the cached pod's, internal/kube/client.go:1629-1637 with the comment at 1642-1645, so an out-of-order update triggers no cleanup. Witness: TestOutOfOrderUpdateTriggersNoCleanup fail on this candidate in both runs.
- `R-JUDGE-CUSTOM-STALE` instruction.md:16: "The above PR only solves the leak for container.id, but we likely have the same issue for custom association sources (such as a label or an annotation)." and :11: the old, invalid entries "are never cleared from memory"; D-1 and D-5. Witness: the reference tests TestStaleLabelKeyEvictedAfterGracePeriod, TestStaleLabelValueChangeEvictsOldValue, TestStaleAnnotationKeyEvictedAfterGracePeriod.
- `R-JUDGE-RESTORED` instruction.md:21: "if a later update restores the same identifier for the same pod UID, that pending request is never removed ... after the grace period expires, it will remove an active association ... This breaks lookups until another update repopulates it." Witness: the reference tests TestReactivatedContainerIDSurvivesSweep, TestReactivatedLabelSurvivesSweepThenEvictedWhenRemovedAgain, TestStaleAnnotationKeyReactivatedDuringConcurrentSweepSurvives.
- `R-JUDGE-RESTORED-CUSTOM` instruction.md:23: "this can definitely happen for custom association sources. If someone configures one against a mutable pod field (like a label or an annotation), the same value can theoretically come and go over a pod's lifetime." Witness: the reference tests TestReactivatedLabelSurvivesSweepThenEvictedWhenRemovedAgain, TestStaleAnnotationKeyReactivatedDuringConcurrentSweepSurvives.
- `R-JUDGE-COME-AND-GO` instruction.md:23: "the same value can theoretically come and go over a pod's lifetime", with :16 (the leak for custom association sources) for the removal that follows. Witness: the reference tests TestReactivatedLabelSurvivesSweepThenEvictedWhenRemovedAgain.
- `R-JUDGE-LIVE-KEYS` instruction.md:21: the cleanup "will remove an active association ... This breaks lookups until another update repopulates it."; D-7. Witness: the reference tests TestStaleLabelKeyEvictedAfterGracePeriod, TestStaleLabelEvictionKeepsLiveKeys.

## Candidate designs

- `buggy`: the shipped tree at the base commit, no fix applied: the stale diff covers container.id alone and the sweep checks only the pod UID
- `gold`: the merged fix: the stale identifier diff generalized to every identifier, plus a pointer-identity sweep check on the owning cache entry
- `cor-A1`: revalidate at sweep time: the sweep skips a stale request when the owning pod's current version still produces the identifier
- `cor-A3`: pending-stale index guarded by the cache lock, cleared when the identifier is re-added
- `cor-A4`: ownership index of each pod's current identifier set, maintained atomically with the cache and consulted at sweep time
- `cor-A5`: cancel-on-reactivate inside the cache lock, with a tombstone counter the sweep checks
- `cor-R3e1f`: pointer identity, the pull request's real commit 3e1f2f5f: each stale request carries the cache entry it was queued against, and the sweep evicts only while that exact entry still owns the identifier
- `inc-I2`: reactivation fixed properly but cleanup never generalized beyond container.id
- `inc-I3a`: cleanup filtered to label-backed and built-in identifiers only, so annotation-backed keys still leak
- `inc-I4`: removal-only staleness: the diff compares source names rather than full identifiers, so a value change leaks the old value
- `inc-I7`: any update of the pod UID cancels all of its pending stale deletes, so an identical resync rescues a stale key
- `inc-I9`: the pod-UID ownership check dropped on the stale path, so a key now owned by another pod is deleted
- `inc-I10`: mask: a sweep-time cache reconcile that converges without honouring the grace period
- `inc-I11`: over-broad eviction that also re-queues the pod's backward-compatibility keys, including the pod UID key
- `inc-I13`: stale requests purged once the owning pod is deleted, so the leak survives deletion
- `inc-I15`: cleanup derived from only the first configured association of each source kind, so later associations still leak
- `inc-R8f2c`: the same cancel-after-unlock race as the first fix, kept through the pull request's real lint cleanup 8f2cd4e5
- `inc-R51da`: the same cancel-after-unlock race as the first fix, kept through the pull request's real code-assistant autofix 51da3724, which only returns early on an empty queue
- `inc-I3br`: sweep-time revalidation whose stale diff skips every identifier keyed on the pod address, so a changed pod IP still leaks its connection and k8s.pod.ip keys
- `inc-I3cr`: pointer identity for every identifier except those carrying container.id, which stay on the UID-only check, so a container.id that goes active, stale, active is still evicted
- `inc-I14r`: in-lock tombstones for every reactivated identifier except label-backed keys, whose reactivation falls back to the UID-only check
- `inc-I8r`: an ownership diff with the out-of-order gate folded away, so an older update writes nothing and every live key looks dropped
- `inc-I16`: a per-kind owns and carries predicate table whose hand-copied value switch lost its k8s.pod.ip case, so the pod IP attribute never reads as carried by the old pod and its key is never queued
- `inc-I17`: per-key write-sequence stamps approved when the request is dequeued, before the sweep waits for the cache lock, so a key reactivated in that window is still evicted
- `inc-I18`: a lock-free newest-pod side map read before the cache lock is taken, a check-then-act split across the lock boundary that evicts a key reactivated while the sweep waits
- `inc-I19`: a revival mailbox drained once at the start of the sweep, so a revival posted while the sweep waits for the cache lock is only seen by the next pass
- `inc-I20`: a stale diff narrowed to association-derived keys, leaving the backward-compatibility pod IP key out of the diff entirely
- `inc-I21`: changed-source detection over pod fields, where k8s.pod.ip never enters the change set because its value comes from the address rather than the attributes map
- `inc-I22`: stale keys deduplicated by identifier value, so the connection key and the pod IP key that share one address collapse to a single request
- `inc-I23`: per-key registration stamps with the currency gate rebuilt on pod UID values, which always compare equal and so never block an older update
- `inc-I24`: versioned pod objects whose version records arrival order alone, so every late update counts as current and cleans up the live pod
- `inc-I25`: retired pod objects with currency judged by CreationTimestamp, identical for every version of one pod, so a late update retires the live object

## Red lines

- `RL-GIT` An exact .git path component appears in the submission; ordinary .gitignore and .gitkeep files are ignored.
- `RL-PREBUILT` The submission carries a prebuilt artifact: a *.test, *.a, *.o, *.so, *.exe, *.syso or *.wasm file, a file with a binary, archive or compressed-file signature, a Go coverage profile, or a file with an executable bit set.
- `RL-SEAL-TAMPER` A sealed candidate tree or the base module archive has a digest other than the one the manifest binds, before or after grading.
- `RL-REWARD-WRITE` A reward file changes while grading runs; no submitted file is scanned for a reward path.

## Rejected routes

- A test that passes on every candidate. No collected test fails on the buggy tree, so the gate does not hold and every compiled criterion scores 0.
- A test that fails on every candidate. The test fails on the gold fix, so the gate does not hold and every compiled criterion scores 0, even beside a valid buggy-fail/gold-pass witness.
- A suite that recognizes the reference transition on the reported input alone. It passes on every incorrect member whose defect lies off that one input, so each of those items scores 0.
- A suite coupled to the gold fix's internals. It fails on at least one correct member, so that member's item scores 0.
- Submitting no new test. The unchanged starter suite provides no executed buggy-fail/gold-pass witness, so the gate cannot hold and the reward is 0.
- Submitting a prebuilt test binary or a repository checkout. A red line fires and every compiled criterion scores 0, so the reward is 0.
- Submitting a test that exercises none of the issue's behaviours. No collected test fails on the buggy tree, so the gate does not hold and the reward is 0; the advisory judge finds none of its behaviours, so judged is at or near 0.

## Dimensions with no item

- `robustness` for `D-1`: The grader runs every candidate twice and absorbs a disagreement as a failure, so no separate robustness item can fail for this obligation without the outcome item failing first.
- `process` for `D-1`: The submission is a file set with no ordered trajectory the verifier can observe, so no process dimension defect is decidable from the candidate results.
- `craft` for `D-1`: Every property that enters the reward reduces to go test outcomes, and the advisory judged items ask which behaviours the tests cover, not how they are written, so test style carries no independently gradable defect.
