#!/usr/bin/env bash
# Complete the krew index submission for the current release:
#
#   1. verify the release assets exist for the tag (created by a real
#      release: goreleaser binaries + gh release upload)
#   2. compute the per-platform sha256s and fill the REPLACE_ME placeholders
#      in kubectl-investigate.yaml
#   3. commit the filled manifest to this repo's main branch
#   4. copy it to plugins/investigate.yaml of the krew-index fork and open a
#      PR against krew-index/krew-index
#
#   hack/submit-krew.sh [--dry-run] [TAG]
#
# Needs: gh (authenticated), network. --dry-run prints every step and
# changes nothing. TAG defaults to the latest git tag.
set -euo pipefail

DRY=0
[ "${1:-}" = "--dry-run" ] && DRY=1 && shift || true
TAG="${1:-}"
REPO="GlediLami/kubetective"
KREW_INDEX="krew-index/krew-index"
FORK="GlediLami/krew-index"
MANIFEST="kubectl-investigate.yaml"

run() {
  printf '  %s\n' "$*"
  [ "$DRY" = 1 ] && return 0
  "$@"
}

cd "$(git rev-parse --show-toplevel)"

echo "== release assets"
TAG="${TAG:-$(git tag --sort=-version:refname | head -n1)}"
[ -z "$TAG" ] && { echo "no tag: pass TAG or cut a release first (make release VERSION=x.y.z)" >&2; exit 1; }
BARE="${TAG#v}"
PLATFORMS="linux_amd64 linux_arm64 darwin_amd64 darwin_arm64"

if [ "$DRY" != 1 ]; then
  gh release view "$TAG" --repo "$REPO" >/dev/null 2>&1 \
    || { echo "release $TAG does not exist - cut it first (goreleaser + gh release upload); this script only completes krew" >&2; exit 1; }
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo " -> per-platform sha256"
URLS=""
for p in $PLATFORMS; do
  asset="kubectl-investigate_${BARE}_${p}.tar.gz"
  if [ "$DRY" = 1 ]; then
    sha="REPLACE_ME_${p}"
  else
    gh release download "$TAG" --repo "$REPO" --pattern "$asset" --dir "$TMP" --clobber
    sha="$(shasum -a 256 "$TMP/$asset" | cut -d' ' -f1)"
  fi
  echo "    ${p}: ${sha}"
  URLS="$URLS $p:$sha"
done

echo " -> fill kubetective-investigate sha256 values in $MANIFEST"
# krew manifest: each platform block is uri: ...tar.gz immediately followed
# by sha256: REPLACE_ME; replace them in platform order.
idx=0
count=0
TMPM="$TMP/$(basename "$MANIFEST").new"
: > "$TMPM"
while IFS= read -r line; do
  if [[ "$line" =~ "sha256: REPLACE_ME" ]]; then
    entry="$(echo "$URLS" | cut -d' ' -f$((idx+1)))"
    s="$(echo "$entry" | cut -d: -f2-)"
    idx=$((idx+1))
    printf '    sha256: %s\n' "$s" >> "$TMPM"
    count=$((count+1))
  else
    printf '%s\n' "$line" >> "$TMPM"
  fi
done < "$MANIFEST"
[ "$count" -eq 4 ] || { echo "expected 4 sha256 placeholders in $MANIFEST, found $count" >&2; exit 1; }
run cp "$TMPM" "$MANIFEST"

echo " -> commit the filled manifest to this repo (main)"
if [ "$DRY" = 1 ]; then
  echo "  [dry-run] would commit 'krew: fill sha256 for ${TAG}' + push origin main"
else
  git add "$MANIFEST"
  git commit -m "krew: fill sha256 for ${TAG}" --quiet
  git push origin main --quiet
fi

echo " -> krew-index PR"
KREW_DIR="$TMP/krew-index"
if [ "$DRY" = 1 ]; then
  echo "  (dry-run) would fork ${KREW_INDEX} -> ${FORK} if missing, copy ${MANIFEST} to"
  echo "            plugins/investigate.yaml, push branch kubetective-${TAG}, open PR"
else
  if ! gh repo view "$FORK" >/dev/null 2>&1; then
    gh repo fork "$KREW_INDEX" --fork-name krew-index --default-branch-only
  fi
  git clone --quiet --depth=1 "https://github.com/$FORK.git" "$KREW_DIR"
  cp "$MANIFEST" "$KREW_DIR/plugins/investigate.yaml"
  cd "$KREW_DIR"
  git checkout -b "kubetective-${TAG}"
  git add plugins/investigate.yaml
  git commit -m "add kubetective plugin ${TAG}" --quiet
  git push --quiet origin "kubetective-${TAG}"
  gh pr create --repo "$KREW_INDEX" --head "${FORK%%/*}:kubetective-${TAG}" \
    --title "Add kubetective ${TAG} (kubectl investigate plugin)" \
    --body "New release ${TAG} of github.com/${REPO}.

- install with: \`kubectl krew install --manifest=https://raw.githubusercontent.com/${REPO}/main/${MANIFEST} investigate\`
- homepage: https://github.com/${REPO}
- release: https://github.com/${REPO}/releases/tag/${TAG}
- sha256 values filled from the release assets (one per platform tarball)"
  echo "  PR opened against ${KREW_INDEX}"
fi

echo
echo "done${DRY:+, dry-run}. Remember: real releases must exist BEFORE this"
echo "script (make release v1.0.0, then goreleaser or gh release upload)."