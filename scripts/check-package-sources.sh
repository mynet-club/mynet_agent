#!/bin/sh
# Existing archive must carry exactly the same source/asset provenance before
# an immutable version may be treated as already published.
set -eu
archive=${1:?}; name=${2:?}; sources=${3:?}
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
case "$name" in
  *.zip) unzip -p "$archive" "${name%.zip}/SOURCES.txt" > "$tmp" ;;
  *.tgz) tar -xzOf "$archive" "${name%.tgz}/SOURCES.txt" > "$tmp" ;;
  *) echo 'unsupported archive' >&2; exit 1 ;;
esac
if ! cmp -s "$tmp" "$sources"; then
  echo 'published version has different provenance; choose a new version' >&2
  exit 1
fi
