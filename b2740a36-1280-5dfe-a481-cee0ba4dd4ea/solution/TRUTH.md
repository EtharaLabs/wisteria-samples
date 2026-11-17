# Ground truth: fontconfig older-cache repair (task b2740a36)

## Source

- Repository: gitlab.freedesktop.org/fontconfig/fontconfig (GitLab project 890, not a fork;
  permissive HPND-style COPYING).
- Report: issue 562, opened 2026-09-10, "Host fonts under /run/host/fonts not detected by fc-list
  inside Flatpak (fontconfig 2.18.3)". Host Fedora Silverblue 44.20260902.0 with fontconfig 2.17.0;
  Flatpak runtime org.freedesktop.Sdk//26.08 with fontconfig 2.18.3; works under Sdk//25.08
  (2.17.0). Maintainer diagnosis (Akira TAGOH, 2026-09-16): the host's older fontconfig wrote only
  `.cache-9`; 2.18.3 (cache format 12) has no read path for older caches; on OSTree the font
  directories have mtime 0, so a colliding zero-mtime cache shipped by the SDK wins once the host
  cache is unreadable.
- Fix: merge request 581, "Read caches written by older fontconfig versions", merge commit
  f477dc2185135a0c0eae3f56f17d9dd08d0c3afc, committed 2026-10-05T08:05:42Z; MR head
  b89bb50413ee91c28424e7739b55333be4928d14.
  - State A (base, shipped in the environment): c8b9654feda3612f65eb70c4f8db8910d1a7daf1, the merge
    commit's first parent.
  - State F: the merge commit.
- Transport check: solution/fix.diff is `git diff A F -- src meson.build` (sha256
  85596de950004854b503a1400e9d40703a23a0942e2121f63b478aa3434a39bc). Applied to A it makes
  meson.build, src/fccache.c, src/fcgenericalias.c, src/fcint.h and src/fcmatch.c byte-identical
  to F. The merge's other files are .gitlab-ci/test-cache-compat.sh and
  test/test_cache_cross_version.py.

## Mechanism

The cache reader only looked for `<md5>-<arch>.cache-12` and rejected any cache whose header
version was not the current one. The fix makes the directory-cache lookup also probe
`.cache-11` down to `.cache-<FC_CACHE_MIN_COMPAT_VERSION>` (9, from meson.build's
`cachemincompat`, already present on A), accepts headers with a version at or above that minimum,
handles the absent fc_version field of formats below 10, and never regenerates or rewrites an
acceptable older cache. Because caches before format 11 carry no `genericfamily` property, the
matcher backfills it from the family name at match time (FcGenericFamilyGetValues in
fcgenericalias.c, FcCompareGenericFamilyBackfill in fcmatch.c), so `fc-match serif` and friends
give the same result from a format 9 cache as from a format 12 cache.

The reference solution changes a file outside the repair's stated scope. solution/fix.diff's
first hunk edits /app/fontconfig/meson.build: it adds a comment above `cachemincompat = 9` and
changes no value. instruction.md:48-50 says changes outside /app/fontconfig/src/ (meson.build
named among them) are not part of the repair. The hunk has no effect on grading:
tests/.verifier/prepare.py takes only files under src/, so the verifier builds the pristine
meson.build whether or not a solver changes it. fix.diff is kept exactly as upstream wrote it
because solution/solve.sh checks the before and after digest of every file it patches, meson.build
included.

## Code states

| State | Commit | F2P (4) | P2P (83) |
|---|---|---|---|
| A | c8b9654feda3 | 0 of 4; each failed in all 12 of its runs (4 grader passes x 3) | passed in all 4 grader passes |
| F | f477dc218513 | 4 of 4; each passed in all 16 of its runs (2 passes x 5, 2 passes x 3) | passed in all 4 grader passes |

These grader passes ran on an arm64 host with the verifier image, `--cpus 4 --memory 8g --network
none`. tests/test.sh runs as root and builds and tests the submission as the sandbox uid 20002
(user `verifier` in tests/Dockerfile); the agent works as uid 10001 (user `dev` in
environment/Dockerfile). The last two passes on each state were the full tests/test.sh over an
artifact copied out of the agent image (A as shipped, F after solution/solve.sh): reward 0.0 on A,
1.0 on F. Full trials of this version on amd64 and arm64 hosts gave the same: the reference solution scored
1.0 with the Anthropic judge and with no judge key, the unchanged tree 0.0, and the reference
solution with a key set and an unreachable judge 0.0. One verifier run without the
judge takes about 35 to 60 s (build about 20 s, meson suite 8 s, pytest suite 5 s, each
cross-version run under 1 s). What A prints: "Current fc-list regenerated cache instead of reusing
the baseline's .cache-9" (and -11), and "host font 8x16.pcf hidden by the shadow cache; fc-list saw
['4x6.pcf'] (issue #562)".

## Graded tests

- Fail to pass (4), from F's test/test_cache_cross_version.py run against prebuilt 2.17.1
  (cache format 9, the newest release at the minimum compatible format, as upstream CI picks it)
  and 2.18.1 (format 11, the newest older format), 3 runs each, a unit counts only if it passes in
  all 3:
  cross-version/2.17.1/test_discovery_backward_compat,
  cross-version/2.17.1/test_shadow_cache_precedence_562,
  cross-version/2.18.1/test_discovery_backward_compat,
  cross-version/2.18.1/test_shadow_cache_precedence_562.
- Pass to pass (83): 7 cross-version cases that already pass on A (binary_format_forward_compat,
  discovery_forward_compat and genericfamily_backfill_contract for both baselines;
  match_equivalence_backward_compat for 2.17.1), 25 meson tests (all but `pytest`, which is run
  case by case, and test_mt_fcinit), and 51 cases of the pytest suite (cross-version file
  excluded, run separately). Full list in tests/graded/manifest.json.
- The baselines are given to the test file with FC_BASELINE_BUILDDIR and
  FC_BASELINE_CACHE_VERSION; it reads cachemincompat (9) from the tree's meson.build, so no graded
  test skips on A. Generic-family match equivalence is exercised only against 2.17.1: its 2.18.1
  variant skips by design (a format-11 cache already carries the generic family), never runs, and
  is not graded (below).
- The rule at instruction.md:61-62 (do not open or search test files or build files) is a working
  constraint on the solver. No test checks it; it is not part of the reward.
- Instruction to test: acceptance criterion 1 (instruction.md:23-26) is
  test_discovery_backward_compat for both baselines; criterion 2 (instruction.md:27-31) is
  test_shadow_cache_precedence_562 for both; criterion 3 (instruction.md:32-33) is the pass-to-pass 2.17.1
  test_match_equivalence_backward_compat; criterion 4 (instruction.md:34-35) is the
  forward-compatibility cases and the project's own suites; "on every run" (instruction.md:37) is
  the 3 runs per cross-version case.

## Left out, with reasons

- cross-version/2.18.1/test_match_equivalence_backward_compat: skips by design for a baseline at
  cache format 11 or newer, on A and F.
- meson test fontconfig:test_mt_fcinit: exits 77 (skip) without the test-hooks option, on A and F.
- pytest test_fontations_ft_query [None] (3), test_genconf [None], test_sandbox (5): skipped on
  both states, fontations and bwrap are disabled for an offline build.
- pytest test_basic.py::test_subdir_with_out_of_date_cache and
  test_new_file_with_out_of_date_cache: timestamp-dependent by their own comment (sleep 1,
  SOURCE_DATE_EPOCH removed); the first failed once on State A under host load, so neither can
  hold 2 of 2 deterministically.
- No graded test needs a design choice that the instruction could not state: the minimum readable
  release (2.17.0 on, i.e. formats 9 to 11, instruction.md:23) is stated as an observable
  requirement, and the knob is not named.

## Grading

- Taken from the solver: regular files that already exist under src/ and differ from State A
  (instruction.md:48-50); laid over a pristine A tree with the verifier's own test file, built out
  of tree with the environment's meson options (instruction.md:51-52).
- Two independent halves (tests/reward.toml, weighted-sum): reward = 0.5 x test_output +
  0.5 x judge; compiled = test_output alone. No gate between the halves: each graded unit costs
  only its own share, and a zero in one half never zeroes the other. The one path on which the
  whole reward is 0 whatever the tests gave is a judge key that yields no clean verdict (Judge
  errors, below).
- test_output is 0 on build failure, any failed cheat check (cheat_test_files_untouched,
  cheat_no_process_exit, cheat_no_external_network; they enforce instruction.md:48-50 and
  instruction.md:58-60), a missing report or any P2P failure. Otherwise it is the share of the 4
  F2P units that pass in all 3 runs. A failed cheat check zeroes test_output only; the judge half
  is scored as usual.
- judge is the weighted mean of the 11 binary criteria in tests/judge.toml (weights 5/3/1, total
  39) over /app/diagnosis.md. Root takes the note before any sandbox process runs: the first of
  /logs/artifacts/app/diagnosis.md and /app/diagnosis.md that is a regular file (not a symbolic
  link), at most 65,536 bytes (instruction.md:41-44 states the limit); a missing, linked or larger
  note is replaced by an empty file and scores 0. The reference note and judge.toml are readable by
  root only (0700 directory, 0600 files), set in tests/Dockerfile and again by tests/test.sh.
- A build killed at its limit or a graded-file digest mismatch is an infra fault: test.sh exits
  non-zero with the fail-closed 0. A suite or cross-version run killed at its limit, or leaving no
  result, is rerun once; if it still gives none, its tests fail with a reason in report.json.
- Limits (tests/.verifier/graded.py): build 1200 s for setup and compile together, meson
  introspect 120 s, meson suite 600 s plus 60 s to remove its previous log, pytest suite 600 s,
  each cross-version run 240 s (2 releases x 3 runs). Worst case with one rerun of every suite
  run: 1200 + 120 + 2 x (60 + 600) + 2 x 600 + 2 x 3 x 2 x 240 = 1200 + 120 + 1320 + 1200 + 2880
  = 6720 s. The judge (Reward Kit 0.2.4, individual mode) runs the 12 calls of a sample (11
  criteria + guard) concurrently and the 3 samples concurrently (up to 8 samples run at once);
  one request is capped at judge.toml's timeout of 300 s and Reward Kit makes at most 3 requests
  per call (a timed-out request ends the call), so one Reward Kit run adds at most 3 x 300 =
  900 s. tests/test.sh runs Reward Kit up to 3 times (60 s, then 120 s apart) until one gives a
  clean verdict, so the judge phase is bounded by 3 x 900 + 60 + 120 = 2880 s. Theoretical worst
  case with an Anthropic key: 6720 + 2880 = 9600 s. With only an OpenAI key, litellm's OpenAI client
  may also retry each request up to 2 times (litellm DEFAULT_MAX_RETRIES = 2), waiting at most
  120 s between tries, so one Reward Kit run is bounded by about 3420 s and the judge phase by
  3 x 3420 + 180 = 10440 s: 6720 + 10440 = 17160 s. [verifier].timeout_sec = 21600 covers both,
  leaving over 4000 s for the steps with no limit of their own (taking the note, prepare,
  collect, cheat checks, and, with no judge key, the compiled-only Reward Kit run), which take
  seconds.
- Judge errors: Reward Kit 0.2.4 records a call that timed out as an errored 0 (it keeps the error
  only where no sample voted) and still writes a reward; any other judge error (an HTTP error such
  as 401/403 or 429, a network failure, a reply still unparseable after 3 attempts) propagates,
  and Reward Kit writes no reward.json. With a judge key, tests/test.sh accepts an attempt only
  when it wrote a reward and its reward-details.json holds a complete, clean verdict: at least one
  judged component, each listing exactly the criterion ids of tests/judge.toml, and no criterion
  row and no guard with an error. Otherwise it reruns Reward Kit, up to 3 runs (60 s, then 120 s
  apart), each into its own root-only directory. If no run is accepted, whatever the cause, the
  reward is 0: /logs/verifier/reward.json is `{"reward": 0.0, "compiled": 0.0}` (the keys Reward
  Kit writes there on the other paths), /logs/verifier/reward.txt is 0.0,
  /logs/verifier/judge_unavailable gives each attempt's reason, no reward-details.json of any
  attempt is published, and test.sh exits 0. With no judge key at all (ANTHROPIC_API_KEY and
  OPENAI_API_KEY both empty) nothing is sent, and the reward is the compiled score (test_output
  alone, at full scale) from a Reward Kit run without the judge, with judge_unavailable giving the
  reason.

## Judged note (D2)

### Reference note sources

tests/reference/diagnosis.md (sha256
2bcebef640dc1ad308ad7fd9f10968129a28bb8429a34a99daf48d408d77dbee, copied to
solution/diagnosis.md) is written only from the upstream record. By paragraph:

1. "What goes wrong, and where": issue 562 description (cidra, 2026-09-10: Silverblue 44, host
   fontconfig 2.17.0, Sdk//26.08 with 2.18.3 vs Sdk//25.08 with 2.17.0, `fc-cache -fv` does not
   help); last sentence from the MR 581 description ("the host wrote a .cache-9 that the newer
   sandbox reader rejected, falling back to a stale runtime cache").
2. "Why the host's caches are not used": Akira TAGOH's analysis in issue 562 (2026-09-16), the
   symlink workaround paragraph; "treats any cache whose version is below its own as unusable"
   from the MR 581 diff of src/fccache.c (FcDirCacheMapFd and FcDirCacheValidateHelper rejected
   `version < FC_CACHE_VERSION_NUMBER`).
3. "Why fc-cache -fv in the sandbox does not help": TAGOH 2026-09-16, points (1) zero-mtime
   preference on ostree and (2) the SDK's un-salted `/usr/share/fonts` cache colliding with
   `/run/host/fonts`.
4. "Why only some hosts": TAGOH 2026-09-16, "Why only Silverblue and not Fedora Workstation or
   Ubuntu?" paragraph.
5. "The repair" (first paragraph): MR 581 diff of src/fccache.c (probe loop and its comment
   naming issue 562, version checks lowered to FC_CACHE_MIN_COMPAT_VERSION, the fc_version
   comment in FcCacheIsNewVersion); MR description ("Probe and accept caches down to
   FC_CACHE_MIN_COMPAT_VERSION"); `cachemincompat = 9` in meson.build; TAGOH 2026-09-17.
6. "The repair" (second paragraph): MR description (reconstruct genericfamily at match time from
   the family name); commit b89bb504 message (a v11 cache already carries genericfamily); TAGOH
   2026-10-05 (2.17.1 writes .cache-9 without genericfamily support); MR diff of src/fcmatch.c
   ("unearned perfect score" comment), src/fcgenericalias.c (curated classification, no substring
   guessing, false positives, unknown) and meson.build (safe only while every newer object can be
   rebuilt; a future format change must raise cachemincompat); the serif / sans-serif / monospace
   queries from the MR's test/test_cache_cross_version.py.

The issue thread holds no other proposed causes (the 2026-09-11 notes only concern installing
the SDK), so the rubric has no look-alike criteria.

### Rubric

tests/judge.toml (Reward Kit 0.2.4): judge anthropic/claude-sonnet-5-5 (openai/gpt-6-astra when
only OPENAI_API_KEY is set), individual mode, timeout 300 s, files ["/app/diagnosis.md"]
(tests/test.sh places the solver's note there inside the verifier container), reference
/tests/reference/diagnosis.md, weight 1.0, guard "penalize", samples 3, weighted-mean
aggregation. Each criterion's verdict is the lower median of the votes of its samples that did
not error and the guard's the higher median (`_median_scores` in Reward Kit 0.2.4's
rewardkit/reward.py); with three votes both are the middle vote.

11 binary criteria: C1-C5 the failure and its cause (paragraphs 1-4), C6-C11 why the repair holds
(paragraphs 5-6), answering the note's second question (instruction.md:41-43). Every criterion
names /app/diagnosis.md, has at most 40 words and weight 1, 3 or 5. The SDK's shipped un-salted
`/usr/share/fonts` cache (paragraph 3) comes only from the issue thread, so no criterion grades
it.

C6 and C8-C11 ask about the reference repair's own mechanism: C6 the probe of older cache file
names from the version below the current one down to the minimum compatible version 9; C8 cache
mapping and validation rejecting only versions below the minimum compatible version; C9 the
fc_version field that does not exist before cache version 10; C10 the generic-family values
rebuilt from the family name for a format-9 cache, which carries no genericfamily object; C11 why
that rebuild keeps fc-match results unchanged. Each of the five ends with "An equivalent mechanism
of the submitted repair counts.", so a note that explains a different, correct mechanism of its
own repair can earn them. Paragraphs 5-6 of the reference note state each of these facts.

### Verifier compatibility fix (litellm model map)

Anthropic's API rejects the request litellm 1.108.0 (pinned in tests/.build/rewardkit.lock)
builds for claude-sonnet-5-5 with HTTP 400 "output_format: This field is deprecated. Use
'output_config.format' instead". Reward Kit 0.2.4 always passes a json_schema response_format
(rewardkit/judges.py), and litellm sends it as the old `output_format` field whenever its model
map marks the model `supports_native_structured_output: true`, which its packaged map does for
claude-sonnet-5-5. With the flag false, litellm uses a forced tool call, which the API accepts.
tests/.build/litellm_model_map.py, run by tests/Dockerfile right after the hash-locked install,
opens litellm's packaged model_prices_and_context_window_backup.json, asserts the flag is true,
sets it false, writes the file and asserts the change; the same Dockerfile step checks the result
again. LITELLM_LOCAL_MODEL_COST_MAP (True unless the runner sets it, task.toml [verifier.env])
makes litellm read only that local map. No dependency changes: litellm and its dependencies come
only from tests/.build/rewardkit.lock. The judged trials of the reference solution above went
through this route.
