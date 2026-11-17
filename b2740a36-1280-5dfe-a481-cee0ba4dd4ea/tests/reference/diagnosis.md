# Root cause: host fonts missing from fc-list in a Flatpak with a newer fontconfig (issue 562)

## What goes wrong, and where

On an ostree-based host (Fedora Silverblue 44) whose fontconfig is 2.17.0, an app in the
`org.freedesktop.Sdk//26.08` runtime, whose fontconfig is 2.18.3, sees none of the host fonts
bind-mounted under `/run/host/fonts`: `fc-list` does not list them, even after `fc-cache -fv`.
The same host works with `org.freedesktop.Sdk//25.08` (fontconfig 2.17.0). The host's older
fontconfig wrote the caches for those fonts in cache format 9 (`.cache-9`); the newer reader in
the sandbox rejected them and fell back to a stale runtime cache.

## Why the host's caches are not used

2.18.3 writes `.cache-12` and, next to it, symlinks named `.cache-9`, `.cache-10` and `.cache-11`
pointing to it, so an older fontconfig can read a cache a newer one wrote. That covers only that
direction. Here the host has the older fontconfig: it wrote only `.cache-9`, with no symlinks,
while 2.18.3 looks for `.cache-12` and treats any cache whose version is below its own as unusable.
2.18.3 had no read-side way to discover or read the older `.cache-9`.

## Why fc-cache -fv in the sandbox does not help

Two things combine. First, on ostree-based systems font directories have a modification time of
zero, and fontconfig treats zero-mtime caches as always up to date and prefers them over caches
with a non-zero mtime; so even after `fc-cache -f` writes a correct cache with a current mtime,
the zero-mtime one wins. Second, the `org.freedesktop.Sdk` runtime accidentally ships a cache file
for `/usr/share/fonts` under the un-salted name, which is the same name `/run/host/fonts` maps to,
so it collides with the host directory's cache. That colliding, stale cache is the zero-mtime cache
fontconfig prefers.

## Why only some hosts

It needs `/run/host/fonts` to have a zero modification time, which happens on ostree-based hosts
such as Silverblue. Elsewhere (Fedora Workstation, Ubuntu) the directory's mtime is non-zero, so
fontconfig checks the cache's checksum, and the colliding un-salted cache fails that check and is
rejected.

## The repair, and why it holds

When the current-version cache for a font directory is not found in a cache directory, the reader
now probes that same cache directory for the older names, from the version just below the current
one down to `FC_CACHE_MIN_COMPAT_VERSION` (9), before moving on to the next cache directory. A cache
an older fontconfig wrote (the host's `.cache-9`) is therefore found instead of being shadowed by a
stale current-version cache in another directory. Mapping and validating a cache now reject only
versions below `FC_CACHE_MIN_COMPAT_VERSION` instead of below the current version, so such a cache
is used as it is rather than rejected and regenerated. The `fc_version` field does not exist before
cache version 10; in an older cache those bytes are payload, so the reader does not interpret them.

A format-9 cache (as 2.17.x writes) holds no `genericfamily` object; a format-11 cache (2.18.1)
already carries it. A font from a format-9 cache would skip that
dimension in matching and receive an unearned perfect score for it, so `fc-match` for `serif`,
`sans-serif` or `monospace` could pick differently. The repair reconstructs the generic-family values
at match time from the font's family name, using only fontconfig's curated family classification
(no substring guessing, which gives false positives); an unclassifiable family counts as unknown.
An old-cache font then matches like a freshly scanned one. Reading back to format 9 is safe only
while every object added since then can be rebuilt from what old caches hold, which today is only
`genericfamily`; a future cache-format change that breaks this must raise the minimum compatible
version.
