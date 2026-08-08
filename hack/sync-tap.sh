#!/usr/bin/env bash
# Sync Formula/kubetective.rb into the Homebrew tap repo and push it, so
# the one-liner `brew install gledilami/kubetective/kubetective` always
# tracks the released formula.
#
#   hack/sync-tap.sh <tag>             (or: release.sh does this automatically)
#   hack/sync-tap.sh <tag> --dry-run
set -euo pipefail

TAG="${1:?usage: hack/sync-tap.sh <tag> [--dry-run]}"
DRY=0
[ "${2:-}" = "--dry-run" ] && DRY=1

TAP_REPO="${KUBETECTIVE_TAP_REPO:-https://github.com/GlediLami/homebrew-kubetective.git}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "cloning ${TAP_REPO} ..."
git clone --quiet --depth 1 "$TAP_REPO" "$TMP"
cp Formula/kubetective.rb "$TMP/Formula/kubetective.rb"

cd "$TMP"
if git diff --quiet; then
  echo "tap repo already in sync (Formula: ${TAG})"
  exit 0
fi
git add Formula/kubetective.rb
git commit -m "Formula: ${TAG}"
if [ "$DRY" = 1 ]; then
  echo "[dry-run] would push ${TAG} to ${TAP_REPO}"
else
  git push
  echo "tap repo synced to ${TAG}"
fi