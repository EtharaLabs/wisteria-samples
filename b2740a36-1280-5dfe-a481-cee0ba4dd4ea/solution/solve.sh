#!/usr/bin/env bash
# Oracle: apply the non-test part of the upstream fix (fontconfig MR 581, merge commit
# f477dc2185135a0c0eae3f56f17d9dd08d0c3afc) to /app/fontconfig and write /app/diagnosis.md.
# Every input and output file is checked by SHA-256.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TREE=/app/fontconfig
DIFF="$HERE/fix.diff"
DIFF_SHA256=85596de950004854b503a1400e9d40703a23a0942e2121f63b478aa3434a39bc
NOTE="$HERE/diagnosis.md"
NOTE_SHA256=2bcebef640dc1ad308ad7fd9f10968129a28bb8429a34a99daf48d408d77dbee

# path, digest at the base revision, digest at the fix commit
FILES=(
  "meson.build ef0499e074e3f8562305c8635ea4e34cca18883f823c9e0abc3e3722aa2e5f63 1a59788f0ce9602a07bf87f21b308fe4d310d87fb8c2753b2abc5438d511ed57"
  "src/fccache.c b76aabfa2e803188cafb5a09222109df2e296aa8df045a54d34f516f863d50b5 75eb0a1f3e96e6edb25c455afcd4f19e58a7f2bee5c1beed879727e146249619"
  "src/fcgenericalias.c 11aecefde833d44700a85b6aec72f42492de1d137f84d0ce8732d128990a0e88 7e91f971ad4ec209a36d5cafc4e6053d352c69905240da5b344e59393979bbf6"
  "src/fcint.h 823d454c84c136be67166ed0edadc516177194f8ab875cdbf2d2921c21ce5a19 2a39ff7eefce3c80134f6557f077526157ef5bd40c2b319f60df2545ae6f73f2"
  "src/fcmatch.c e4b1e110dbbe476a96d2e40b5e37f711fe53a7bfd3d8f66651119483c1cb70fe 5b3ebd1ea8f0945f89e8977a4aa8f3b849a9373a179ae2504c045a3713d17d14"
)

digest() { sha256sum "$1" | cut -d' ' -f1; }

if [ "$(digest "$DIFF")" != "$DIFF_SHA256" ]; then
  echo "solve.sh: fix.diff does not match its digest" >&2
  exit 1
fi

# The root-cause note.
if [ "$(digest "$NOTE")" != "$NOTE_SHA256" ]; then
  echo "solve.sh: diagnosis.md does not match its digest" >&2
  exit 1
fi
rm -f /app/diagnosis.md
install -m 0644 "$NOTE" /app/diagnosis.md
if [ "$(digest /app/diagnosis.md)" != "$NOTE_SHA256" ]; then
  echo "solve.sh: /app/diagnosis.md does not match its digest" >&2
  exit 1
fi
echo "solve.sh: /app/diagnosis.md written"

fixed=0
for entry in "${FILES[@]}"; do
  read -r path base after <<<"$entry"
  have="$(digest "$TREE/$path")"
  if [ "$have" = "$after" ]; then
    fixed=$((fixed + 1))
  elif [ "$have" != "$base" ]; then
    echo "solve.sh: $path is neither at the base revision nor at the fix" >&2
    exit 1
  fi
done

if [ "$fixed" -eq "${#FILES[@]}" ]; then
  echo "solve.sh: already fixed"
  exit 0
fi
if [ "$fixed" -ne 0 ]; then
  echo "solve.sh: the tree is partly fixed; refusing to patch" >&2
  exit 1
fi

patch -d "$TREE" -p1 --forward --batch --no-backup-if-mismatch <"$DIFF"

for entry in "${FILES[@]}"; do
  read -r path _base after <<<"$entry"
  if [ "$(digest "$TREE/$path")" != "$after" ]; then
    echo "solve.sh: patched $path does not match the fix commit" >&2
    exit 1
  fi
  echo "solve.sh: $path fixed"
done

# Keep the prebuilt build in /app/fontconfig-build current; the verifier rebuilds from the sources on its own.
if [ -f /app/fontconfig-build/build.ninja ] && command -v ninja >/dev/null 2>&1; then
  ninja -C /app/fontconfig-build -j4 >/dev/null || echo "solve.sh: incremental rebuild failed" >&2
fi
