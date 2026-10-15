#!/bin/sh
# build-sources.sh AGENT_REPO GUI_REPO GUI_DIST DESKTOP_REPO_OR_DASH OUTPUT
# Only provenance metadata; never copies local paths or repository credentials.
set -eu
agent=${1:?}; gui=${2:?}; assets=${3:?}; desktop=${4:?}; output=${5:?}
if command -v sha256sum >/dev/null 2>&1; then
  hash() { sha256sum "$@"; }
  list_assets() { find . -type f -exec sha256sum {} +; }
else
  hash() { shasum -a 256 "$@"; }
  list_assets() { find . -type f -exec shasum -a 256 {} +; }
fi
test -f "$assets/index.html" || { echo 'GUI index missing for provenance' >&2; exit 1; }
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
record() {
  name=$1; repo=$2
  echo "${name}_sha=$(git -C "$repo" rev-parse --verify HEAD)"
  dirty=false
  if ! git -C "$repo" diff --quiet HEAD --; then dirty=true; fi
  echo "${name}_tracked_dirty=$dirty"
}
# Do not hide git errors inside echo command substitutions.
git -C "$agent" rev-parse --verify HEAD >/dev/null
git -C "$gui" rev-parse --verify HEAD >/dev/null
if [ "$desktop" != - ]; then git -C "$desktop" rev-parse --verify HEAD >/dev/null; fi
{
  echo 'format=1'
  record agent "$agent"
  record gui "$gui"
  if [ "$desktop" != - ]; then record desktop "$desktop"; fi
} > "$tmp"
# Relative asset names make the digest independent of the workspace path.
listing=$(mktemp)
trap 'rm -f "$tmp" "$listing"' EXIT
(cd "$assets" && list_assets) > "$listing"
LC_ALL=C sort "$listing" > "$listing.sorted"
trap 'rm -f "$tmp" "$listing" "$listing.sorted"' EXIT
digest=$(hash "$listing.sorted")
echo "gui_tree_sha256=${digest%% *}" >> "$tmp"
mv "$tmp" "$output"
