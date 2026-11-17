#!/usr/bin/env bash
# Oracle: apply the non-test part of the upstream fix (databendlabs/openraft PR 2236, commit
# 401ea02d3f10ab405bed509916a808e276743fe9) to /app/openraft and write /app/diagnosis.md.
# Every input and output file is checked by SHA-256.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TREE=/app/openraft
DIFF="$HERE/fix.diff"
DIFF_SHA256=bc4d36b5663f24cc2e556e714fe575593742b72cae16a85a74d51423ef58ac9a
NOTE_SRC="$HERE/diagnosis.md"
NOTE=/app/diagnosis.md
NOTE_SHA256=d261e9a4c11f1d502829f2a0c4143f31c1b3c7f6b7408629107508d0b4465a40

# path, digest at the base revision, digest at the merge commit
FILES=(
  "openraft/src/config/config.rs 3112de34b19a5d31ba2a86e2858e7e7608e1c7ec8ebb993e7d77db53851b9082 83e07f088ca4bc6bd71d04f9f0baa7efd20f17e3d73565c69447c84a63152877"
  "openraft/src/docs/faq/02-core-concepts/01-differences-from-raft.md 596f16d0372c34a6194babd232163460a6e52fdf3dde885f26191de7a60c148d 9ed5ec012055e127bcf714e47ad170d6345fb723a12b5c1a77cc2f114726fec8"
  "openraft/src/docs/faq/05-monitoring/02-valid-leader.md 9c7f437a003acda24b5886b690aaf2f5c723f8bc2b0a518db35387992ee2ac3a f5f28d82bd5172bdfe1de21bc70d51743e16139b9ddcfcd909cf956e4a3917bc"
  "openraft/src/docs/faq/06-operations/01-node-restart.md d4dddd1f2684b64c0971ae4c9816856ed87a44bd4ae450d086762e78d2a35499 70f2dab162d6089674ab4dfe8862dc4d6adf5cf1e2ea7409571b6a580c78903c"
  "openraft/src/docs/faq/faq.md c8c62fcc1ed7848b0f806a64585ce01b2ff045451aedf0afa5ef9f15b8142675 91967b6c0e156e78010bd321fa181999ef29355ce097794832cfb4283c0fb9de"
  "openraft/src/engine/engine_impl.rs 5edd26501445baf67b55932a5980134dbd9b0f2b6e58242a6e98ca0f9bfbb313 08df758c6b035209959d18ca1afb1e237d4fe7d9e9ff822702b2a9993d192af2"
)

digest() { sha256sum "$1" | cut -d' ' -f1; }

if [ "$(digest "$DIFF")" != "$DIFF_SHA256" ]; then
  echo "solve.sh: fix.diff does not match its digest" >&2
  exit 1
fi

# The note first, so it is in place whichever way the tree part ends.
if [ "$(digest "$NOTE_SRC")" != "$NOTE_SHA256" ]; then
  echo "solve.sh: diagnosis.md does not match its digest" >&2
  exit 1
fi
cp "$NOTE_SRC" "$NOTE"
if [ "$(digest "$NOTE")" != "$NOTE_SHA256" ]; then
  echo "solve.sh: $NOTE does not match its digest after the copy" >&2
  exit 1
fi
echo "solve.sh: $NOTE written"

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
    echo "solve.sh: patched $path does not match the merge commit" >&2
    exit 1
  fi
  echo "solve.sh: $path fixed"
done
