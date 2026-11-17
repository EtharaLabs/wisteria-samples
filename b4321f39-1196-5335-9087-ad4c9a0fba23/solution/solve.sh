#!/bin/bash
# Reference solution: add the regression suite to server/stree, then build and run it on this tree.
# The tree is the one the issue describes, so the package's existing tests must still pass and the
# added suite must fail (it checks the behaviour the issue asks for).
set -euo pipefail
install -m 0644 /solution/wildcard_literal_test.go /app/server/stree/wildcard_literal_test.go
cd /app
gofmt -l server/stree/wildcard_literal_test.go | grep -q . && { echo "added suite is not gofmt-clean" >&2; exit 1; }
go vet ./server/stree/
log="$(mktemp)"
trap 'rm -f "$log"' EXIT
if go test -race -count=1 ./server/stree/ > "$log" 2>&1; then
  echo "the added suite passes on the tree the issue describes; it does not check the issue" >&2
  exit 1
fi
grep -q -- '^--- FAIL: ' "$log" || { cat "$log" >&2; echo "the suite did not run to a test failure" >&2; exit 1; }
if grep -- '^--- FAIL: ' "$log" | grep -v -q -- '^--- FAIL: TestWildcardCharInsideToken'; then
  cat "$log" >&2; echo "a test outside the added suite fails on this tree" >&2; exit 1
fi
echo "added suite: $(grep -c -- '^--- FAIL: ' "$log") test(s) fail on this tree, as the issue describes"
