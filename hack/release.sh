#!/usr/bin/env bash
# Cut a release end-to-end: bump the version everywhere, verify, build +
# test, commit, tag, push main + tag, refresh the brew formula sha, and
# sync the Homebrew tap repo. One command:
#
#   hack/release.sh v0.9.0             (or: make release VERSION=v0.9.0)
#   hack/release.sh v0.9.0 --dry-run    (print every step, change nothing)
#
# Remaining manual steps (printed at the end): create the GitHub Release
# and fill the krew manifest sha256 values from the release assets.
set -euo pipefail

TAG="${1:?usage: hack/release.sh <version> [--dry-run]}"
DRY=0
[ "${2:-}" = "--dry-run" ] && DRY=1

run() {
  printf '  %s\n' "$*"
  if [ "$DRY" = 1 ]; then return 0; fi
  "$@"
}

cd "$(git rev-parse --show-toplevel)"

echo "== version"
if [ "$DRY" = 1 ]; then
  echo "  [dry-run] would bump version sinks to ${TAG}"
  ./hack/check-version.sh
else
  ./hack/bump-version.sh "$TAG"
fi

echo "== verify"
go build ./...
go vet ./...
go test ./...

echo "== commit the bump"
if [ "$DRY" = 1 ]; then
  echo "  [dry-run] would commit 'chore: bump version to ${TAG}'"
elif [ -n "$(git status --porcelain internal/engine/engine.go Dockerfile kubectl-investigate.yaml)" ]; then
  git add internal/engine/engine.go Dockerfile kubectl-investigate.yaml
  git commit -m "chore: bump version to ${TAG}"
fi

echo "== tag + push"
run git tag "$TAG"
run git push origin main
run git push origin "$TAG"

echo "== brew formula (fetches the tag tarball sha)"
if [ "$DRY" = 1 ]; then
  echo "  [dry-run] formula url would become refs/tags/${TAG}.tar.gz + new sha"
else
  ./hack/update-formula.sh "$TAG"
  git add Formula/kubetective.rb
  git commit -m "Formula: ${TAG}"
  git push origin main
fi

echo "== homebrew tap sync"
if [ "$DRY" = 1 ]; then
  echo "  [dry-run] would sync ${TAG} to the tap repo"
else
  ./hack/sync-tap.sh "$TAG"
fi

echo
echo "release ${TAG} done${DRY:+, continuing}."
echo "next: create the GitHub Release for ${TAG} with the changelog entry,"
echo "then fill the krew manifest sha256 values (REPLACE_ME) from the"
echo "release assets and submit to the plugin index."