# Issue 562: host caches in a newer Flatpak runtime

## Failure and contributing problems

The host and sandbox share fonts and cache files, but do not necessarily share a
fontconfig release. In this build the cache filename suffix is `.cache-12` and the
minimum compatible cache version is 9. The older host can supply a compatible
cache under an older suffix. Before the repair, `FcDirCacheProcess` searched only
the current suffix (with an optional UUID-name fallback). It did not discover the
older host file. In addition, both the mapping and header-validation paths
rejected a header version lower than the current version, rather than lower than
the minimum compatible version. Renaming an older file alone therefore would not
repair the reader. These were reader restrictions, not a requirement to convert
the compatible serialized payload.

This prevents the runtime from consuming the host's authoritative cached font
set and sends it down the missing-cache/rescan path. Running `fc-cache -fv` does
not repair compatibility with the cache supplied by the older host. The supplied
report does not establish an additional font-scanning failure, and this repair
does not depend on identifying one. It restores direct use of the host cache.
The distribution difference is consistent with the immutable, epoch-timestamped
font directories on Silverblue; it is not a distribution-name distinction in the
library.

There are also two freshness/selection problems relevant to those directories.
`FcCacheTimeValid` treated a directory mtime of zero as permission to accept any
cached directory checksum, including a nonzero, stale one. Separately,
`FcDirCacheMapHelper` could fall through and replace its selected cache without
unloading it when considering an older candidate for a zero-mtime directory.
Its initial candidate handling also conflated “nothing selected yet” with a
selected cache whose timestamp was zero. Searching more filenames must not make
these selection errors observable. A reproducible host cache with a zero file
timestamp must retain priority over an ordinary locally generated cache for an
immutable directory.

Older headers can end before the appended `fc_version` word. Reading that word
unconditionally would interpret the beginning of the serialized directory name
as a writer version, potentially treating stale older data as newer than this
library and bypassing freshness checks. A reader must distinguish the header
extent from the following payload.

Finally, merely loading the older font set does not give equivalent matching.
Current scanning adds `genericfamily`, which current matching scores. Older
cached patterns can lack that property. Accepting those patterns without
completing the metadata can change generic serif/sans-serif/monospace matches.

## Repair and the acceptance criteria

1. **Read compatible older files without rewriting them.** Cache discovery now
   checks every suffix from the current version down to the existing minimum,
   in every configured cache directory, for MD5 and supported UUID names. Both
   header validation and mapping accept that same compatibility minimum. The
   minimum readable header is the common prefix ending before `fc_version`.
   The writer-version check reads the extension only when the directory offset
   and file size leave room for it in the header. Existing offset and value-type
   validation remains active. Cache processing opens candidates read-only and
   retains successful validation even if a later candidate is absent or invalid.
   A current older cache therefore satisfies the ordinary cache-load path;
   discovery and loading add no writes, migrations, aliases, or regeneration.

2. **Choose the host font set in the reported configuration.** Directory
   freshness now requires the cached seconds and, where supported, nanoseconds
   to match even when the directory timestamp is zero. A stale nonzero directory
   checksum is rejected. Among valid candidates, selection explicitly handles
   an initially empty selection, prefers a zero-file-timestamp cache for a
   zero-mtime directory, and otherwise selects the newest cache file. Equal
   timestamps retain traversal order. Every losing mapping is unloaded, and a
   replacement unloads the previous selection. Thus a stale first directory
   cannot prevent discovery of the valid older-suffix host cache in the last
   directory. Even a first cache claiming the same zero directory checksum loses
   to the reproducible zero-file-timestamp host cache. No cache writes are needed.
   The existing deliberate protection for caches from a newer writer remains;
   bytes belonging to an older cache's directory string cannot activate it.

3. **Keep generic matching equivalent.** The scanner's family classification is
   shared in `FcPatternSetGenericFamily`: it uses the same classification table,
   bit order, family iteration order, name heuristics, and unknown-family
   fallback. When adding a cached font to a configuration, a pattern missing
   `genericfamily` is duplicated and classified in memory before pattern/client
   filtering. Patterns already containing the property are kept. Relocation is
   applied to the appropriate owned or cache-backed pattern, and references and
   rejected patterns are released according to their ownership. Consequently
   generic matching sees the same classification whether it was serialized by
   the current scanner or completed from an older cached family list. This
   requires no font-file rescan and leaves the original mapping/file unchanged.

4. **Preserve older readers and the existing format.** The serialized cache
   structure, current version and suffix, writer-version value, writer layout,
   and backward filename links are unchanged. Extracting classification into a
   shared helper preserves successful scan output. Older readers therefore
   retain their existing ability to discover and read newly written caches.
   No public names, tool options, message text, or output format is changed.

The decisions depend on cache metadata and configuration order, not allocation
addresses, random seeds, or uninitialized optional header bytes. Both mapped
and allocated cache loading follow the same compatibility and freshness rules.

## Verification and limits

- The out-of-tree Ninja build succeeds using the supplied build configuration.
- The native Meson tests pass (25 passed, one skipped).
- The full Meson command encounters an environment problem: pytest does not
  recognize its configured `--tap` argument. Running the Python suite directly
  with the same non-network selection, without that reporting option, passes:
  53 passed, 12 skipped, two deselected.
- A separate temporary harness, outside the source tree, constructs compatible
  older-header fixtures with suffixes/header versions 9, 10 and 11 and no
  generic-family property. It compares them with current-format patterns for
  three generic classes, with and without a stale current-format cache listed
  first. Across 120 `fc-list` and 360 `fc-match` invocations, alternating mmap
  settings and varying allocation perturbation, the font sets and all three
  generic matches agree. Cache file bytes, sizes, mtimes and ctimes are unchanged,
  and an additional configured empty cache directory remains empty. The stale
  fixture deliberately has a matching zero directory checksum but a nonzero
  cache-file timestamp, exercising selection as well as discovery.
- These are constructed compatibility fixtures, not executions of separate
  historical releases. No historical fontconfig binary is available in this
  image, so older-reader execution was not independently tested here. Its
  preservation follows from retaining the writer format and compatibility links.

The repair changes only existing regular source files under `src/`; this note
is a separate regular Markdown file.
