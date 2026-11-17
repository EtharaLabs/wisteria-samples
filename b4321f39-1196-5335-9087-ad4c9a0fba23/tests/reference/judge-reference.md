# Reference for the judged criteria

## What is being judged

The submission is Go test code for package `stree` of the NATS server (`server/stree`), written to
let a maintainer accept or reject proposed fixes of one issue: literal subjects such as `a*`, `*a`
or `**` were sometimes treated as containing a wildcard. Only the test files that the solver added
or changed are shown; the package's unchanged existing tests are not shown and count for nothing
here. Judge each criterion from the test source alone. The tests are never run for this part.

The package: `NewSubjectTree[T]()` makes a tree; `Insert(subject, value)`, `Find(subject)`
(returns a pointer to the stored value and whether it was found), `Delete(subject)` and `Empty()`
change or read it. `Match(filter, cb)` calls `cb(subject []byte, value *T)` for every stored
subject the filter matches. `MatchUntil(filter, cb)` does the same, except that it stops as soon as
`cb` returns false (no further calls) and then returns false; it returns true when it finishes
without being stopped, including when nothing matched.

The rules the tests are written against:

1. A subject is a sequence of tokens separated by `.`. In a filter, a token that is exactly `*`
   matches any one token, and a last token that is exactly `>` matches one or more remaining
   tokens. Every other byte is literal: a `*` or `>` inside a longer token (`a*`, `*a`, `**`, `>b`,
   `a>`) matches only itself, in filters and in stored subjects alike. This holds for every shape
   the tree can take, for filters that mix real wildcards with literal `*` and `>` characters, and
   for subjects and filters of any length and any number of tokens.
2. `Match` calls the callback exactly once for each stored subject that the filter matches, and
   never for anything else. `MatchUntil` stops as described above.
3. The callback receives the complete stored subject and a pointer to the stored value, the same
   pointer `Find` returns, so writes through it change the stored value.
4. The order of the callback calls is unspecified, so a test must not depend on it.
5. Results always reflect the tree's current contents, after any sequence of `Insert`, `Delete`
   and `Empty`.
6. `Match` and `MatchUntil` only read the tree; they may be called concurrently from many
   goroutines, on one tree or on different trees, and again from inside a callback on the same
   tree.

## How to decide

A criterion is met when the submitted tests themselves do what it asks and check the outcome with
an assertion that would fail if the behaviour were wrong. Any style counts: hand-written cases,
table-driven cases, generated or seeded random cases, and comparisons with a small model of the
rules written in the test file. A helper that is defined but never called by a test does not count.
A test that only prints, or calls a method without checking what it did, does not count. The
submission need not resemble the reference suite described below or be as extensive; it only has to
contain what the criterion asks for.

## How the reference suite meets each criterion

The reference suite is one Go file. Its helpers compare `Match` and `MatchUntil` with a token-level
model of rule 1 (`wlRefMatch`): they collect every reported subject and value into a slice, sort it
and compare it with the sorted expected slice, so duplicates and missing or extra reports fail and
the order of calls does not matter. Each callback also checks that its value pointer equals the
pointer `Find` returns for the reported subject.

- `literal_chars_in_filters`: with `KV.a`, `KV.apple`, `KV.avocado`, `KV.banana` stored it
  checks filters `KV.a*`, `KV.a>`, `KV.ap*`, `KV.a**`, `*.a*` and more, which must match nothing
  (or only a stored subject with exactly those bytes); also `x.>b`, `foo.>ba`, `a.**`, `a.*b`.
- `literal_chars_in_stored_subjects`: it stores `KV.a*`, `KV.a*x`, `KV.a>`, `KV.a>x`, `x.>b`,
  `a.**`, `a.*b`, `a.b*`, `a.>>`, `a.*>` and checks which filters report them.
- `real_and_literal_wildcards_mixed`: filters such as `*.a*`, `KV.a*.>`, `KV.a*b.*`,
  `foo.b*r.>`, `*.*.b>z`, `*.b>r.>`, and randomly generated filters that place `*` and `>` both as
  whole tokens and inside tokens.
- `long_subjects_and_filters`: subjects of over 300 bytes (`abcdefghij.` repeated 30 times plus an ending),
  and subjects and filters of about 40 tokens, some with every other token a `*`.
- `each_match_reported_once`: the sorted-slice comparison above, applied to
  every filter of every test.
- `match_until_stops`: for every k up to the number of matches the callback returns false on
  call k; the test checks that exactly k calls were made and that `MatchUntil` returned false, and
  that it returned true when the callback never returned false.
- `callback_receives_stored_value`: the pointer check against `Find` in every callback, and a test that adds to
  the value through the pointer in `Match` and `MatchUntil` and then reads every stored value back
  with `Find`.
- `results_after_updates`: tests that delete subjects one by one, re-insert some, insert new
  ones and call `Empty`, checking matches after every change against the current set; a seeded
  sequence of 3000 random inserts and deletes with a check after each.
- `overlapping_calls`: six or eight goroutines started together run `Match` and `MatchUntil` on
  one shared tree and on their own trees, checking every result; and callbacks that run another
  `Match` or `MatchUntil` on the same tree, checking the inner results and that the outer subject is
  unchanged.
