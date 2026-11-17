The following text contains a user issue (in <issue/> brackets) posted at a repository. The repository, the NATS server (Go module `github.com/nats-io/nats-server/v2`), is checked out at `/app`, at the commit just before the issue was fixed. Your task is to identify the issue and write test cases that verify a proposed solution to it. More details follow the issue.

<issue>
Literal subjects, like `a*`, `*a` or `**`, would _sometimes_ be treated as containing a wildcard whereas they're not supposed to.
</issue>

The issue is about package `server/stree` (`/app/server/stree`), the subject tree that stores literal subjects and answers `Match` and `MatchUntil` for filters that can contain wildcards. For example, with `KV.a`, `KV.apple`, `KV.avocado` and `KV.banana` stored, `Match([]byte("KV.a*"), cb)` currently calls `cb` for `KV.apple` and `KV.avocado`, although `KV.a*` contains no wildcard and matches nothing in that tree.

Write tests for `server/stree` that a maintainer can use to accept or reject proposed fixes. A proposed fix replaces every non-test `.go` file of `server/stree`; exported identifiers, and the unexported identifiers that the existing tests use, keep their names and signatures, but anything unexported may work differently inside. Some proposed fixes are correct and some are not. Your suite accepts a fix only if every run of your tests passes against it, and rejects it only if every run fails. It must accept every correct fix of this issue, the upstream fix included, and reject every incorrect one: a suite that rejects a correct fix cannot be used to accept fixes. Every incorrect fix breaks at least one rule below; every correct fix keeps all of them.

## Behaviour of `Match` and `MatchUntil` in every correct implementation

1. A subject is a sequence of tokens separated by `.`. In a filter, a token that is exactly `*` matches any one token, and a last token that is exactly `>` matches one or more remaining tokens. Every other byte is literal: a `*` or `>` inside a longer token (`a*`, `*a`, `**`, `>b`, `a>`) matches only itself, in filters and in stored subjects alike. This holds for every shape the tree can take, for filters that mix real wildcards with literal `*` and `>` characters, and for subjects and filters of any length and any number of tokens.
2. `Match` calls the callback exactly once for each stored subject that the filter matches, and never for anything else. `MatchUntil` does the same, except that it stops as soon as the callback returns false (no further calls) and then returns false; it returns true when it finishes without being stopped, including when nothing matched.
3. The callback receives the complete stored subject and a pointer to the stored value, the same pointer `Find` returns, so writes through it change the stored value. The subject slice is only valid during the call.
4. The order in which the callback is called is unspecified: it differs between correct implementations and can differ from one call to the next.
5. Results always reflect the tree's current contents, after any sequence of `Insert`, `Delete` and `Empty`.
6. `Match` and `MatchUntil` only read the tree. They may be called concurrently from many goroutines, on one tree or on different trees, and they may be called again from inside a callback on the same tree. Every call behaves as if it were the only one.
7. A call may take time proportional to the number of stored subjects; do not rely on it being faster than that.
8. Behaviour is unspecified for inputs outside these rules: empty tokens, a `>` token that is not the last token, and stored subjects containing a token that is exactly `*` or `>`.

## Requirements for your tests

- Put your tests in `/app/server/stree`, in files whose names end in `_test.go` (package `stree` or `stree_test`); a `/app/server/stree/testdata/` directory may hold their data. Your tests must not depend on anything else: no other file, and no change you make to non-test files of `/app/server/stree`, is part of your suite. You may change non-test files while you work, for example to try a fix of your own.
- The existing tests in `/app/server/stree` stay part of your suite unless you delete them, so keep them passing.
- Your tests may use the standard library and the packages the existing `server/stree` tests already import. There is no network.
- Your tests must give the same result on every run of the compiled test binary, as with `go test -race -count=1`, whatever the `-shuffle` seed and with GOMAXPROCS set to 1, 2 or 4. They must not read the package's non-test source files, must not write into the package directory (use `t.TempDir()` for any scratch files), and must not need more than 2 CPUs or any privilege. One run must finish within 60 seconds.
- From `/app`, `go test -race ./server/stree/` runs your tests against the code in the tree.
- Make sure to implement only test cases and don't try to fix the issue itself in the files you submit. You are not allowed to read git history.
