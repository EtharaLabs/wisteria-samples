# Host fonts vanish from fc-list inside a Flatpak sandbox running a newer fontconfig

You maintain fontconfig, whose source tree is checked out at `/app/fontconfig` (2.18.3
development). A complete out-of-tree meson build of it, with the tools and the test suite enabled
(the options are in `/app/fontconfig-build/MESON_OPTIONS`), is already in `/app/fontconfig-build`,
so after an edit `ninja -C /app/fontconfig-build` rebuilds only what changed and
`meson test -C /app/fontconfig-build` runs the project's tests.

## What was reported

Issue 562, "Host fonts under /run/host/fonts not detected by fc-list inside Flatpak (fontconfig
2.18.3)": on a Fedora Silverblue 44 host (44.20260902.0, host fontconfig 2.17.0), an app running in
the Flatpak runtime `org.freedesktop.Sdk//26.08`, whose fontconfig is 2.18.3, gets no host fonts:
`fc-list` inside the sandbox does not list the fonts bind-mounted under `/run/host/fonts`, even
after `fc-cache -fv`. The same host and fonts work under `org.freedesktop.Sdk//25.08`, whose
fontconfig is 2.17.0. It reproduces on a bare-metal Silverblue 44 install and in a fresh Silverblue
44 VM, not on Fedora 44 Workstation or Ubuntu. The host's caches for those fonts were written by
the host's older fontconfig, and Silverblue's read-only font directories have a modification time
of zero. Expected: the host fonts are listed, as they are under 2.17.0.

## Acceptance criteria

1. A font cache written by any earlier fontconfig release from 2.17.0 on is used by this version
   as it is. When such a cache is current for a font directory, `fc-list` lists that directory's
   fonts from it and creates, rewrites or modifies no cache file, in that cache directory or any
   other.
2. In the reporter's situation the host fonts are listed: the font directory's modification time
   is zero, the configuration lists a cache directory first that holds an out-of-date cache in this
   version's own format for that same directory (made from different fonts), and lists last the
   cache directory holding the cache an earlier release (2.17.0 or later) wrote for it. `fc-list`
   lists the fonts the earlier release cached, not the out-of-date ones, and writes no cache file.
3. `fc-match` for `serif`, `sans-serif` and `monospace` gives the same results whether the fonts
   come from a cache an earlier release wrote or from one this version wrote.
4. Earlier releases keep reading the caches this version writes, and the project's existing tests
   keep passing.

Each of these must hold on every run, not only some of the time.

## Root-cause note

Besides the repair, write a root-cause note in Markdown at `/app/diagnosis.md`, as a regular file
(not a symbolic link): what goes wrong in the reported situation and why, and why your repair meets
each acceptance criterion above. Its statements must be correct and complete. The note must be at
most 64 KiB (65,536 bytes).

## Scope of the change

- Make the repair only by changing files that already exist under `/app/fontconfig/src/`, and
  keep them regular files (not symbolic links). Changes anywhere else (new or deleted files,
  `/app/fontconfig/meson.build`, the tools, `/app/fontconfig/test/`) are not part of the repair.
- The tree must build out of tree with the options in `/app/fontconfig-build/MESON_OPTIONS`,
  without network access.

## Rules

- Keep every existing name, tool option, output format and message text, and the cache format
  this version writes.
- Do not exit, abort, kill or signal the process from library or tool code, do not add network
  code (sockets, name resolution, or URLs naming another host), and do not special-case the paths,
  file names, fonts or environment variables of any test.
- Do not open or search test files (anything under `/app/fontconfig/test/`) or build files
  (Dockerfiles, CI scripts); work from this description and the library code.
- The environment has no network access; everything needed to build and test is already in the
  image. Leave the tree buildable.
