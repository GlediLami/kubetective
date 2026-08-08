#!/usr/bin/env bash
# Version consistency gate (v0.9): every hardcoded version sink in the repo
# must agree with the canonical version in internal/engine/engine.go.
#
#   hack/check-version.sh [--quiet]
#
# Sinks checked:
#   engine   internal/engine/engine.go      -- canonical (single source)
#   docker   Dockerfile ldflags Version     -- must equal engine exactly
#   krew     kubectl-investigate.yaml        -- spec.version, every platform
#                                              uri, sha256 shape
#   formula  Formula/kubetective.rb          -- url must pin the LATEST git
#                                              tag (release-pinned by design),
#                                              sha256 64-hex
#   docs     CONTRIBUTING.md                 -- no literal "git tag v<num>"
#                                              release example (script-driven)
#
# Exits 1 when anything is stale. Runs in CI and as a pre-commit hook
# (.githooks/pre-commit); local only, no network.
set -euo pipefail

QUIET=0
[ "${1:-}" = "--quiet" ] && QUIET=1

cd "$(git rev-parse --show-toplevel)"

fail=0
nwarn=0
note() { [ "$QUIET" = 1 ] || printf '  %s\n' "$*"; }
ok()   { note "OK    $*"; }
wrn()  { note "WARN  $*"; nwarn=$((nwarn+1)); }
bad()  { note "FAIL  $*"; fail=$((fail+1)); }

# --- canonical -----------------------------------------------------------------
ENGINE_VERSION="$(sed -n 's/^var Version = "\([^"]*\)"/\1/p' internal/engine/engine.go)"
if [ -z "$ENGINE_VERSION" ]; then
  echo "check-version: cannot read internal/engine/engine.go" >&2
  exit 1
fi
[ "$QUIET" = 1 ] || printf 'engine canonical version: %s\n\n' "$ENGINE_VERSION"
KREW_VERSION="${ENGINE_VERSION%-dev}"
KREW_BARE="${KREW_VERSION#v}"

# --- Dockerfile ------------------------------------------------------------------------
[ "$QUIET" = 1 ] || echo "== Dockerfile"
if grep -q -- "engine.Version=${ENGINE_VERSION}" Dockerfile; then
  ok "ldflags Version=${ENGINE_VERSION}"
else
  bad "Dockerfile ldflags Version must be exactly ${ENGINE_VERSION}"
fi

# --- krew manifest --------------------------------------------------------------------
[ "$QUIET" = 1 ] || echo "== kubectl-investigate.yaml (krew)"
KREW_SPEC="$(sed -n 's/^  version: "\(.*\)".*/\1/p' kubectl-investigate.yaml)"
if [ "$KREW_SPEC" = "$KREW_VERSION" ]; then
  ok "spec.version ${KREW_SPEC}"
else
  bad "krew spec.version=${KREW_SPEC:-<missing>} want ${KREW_VERSION} (engine minus -dev)"
fi
if [ "$(grep -c "releases/download/${KREW_VERSION}/kubectl-investigate_${KREW_BARE}_" kubectl-investigate.yaml)" -eq 4 ]; then
  ok "4 platform uris point at ${KREW_VERSION}"
else
  bad "krew platform uris must reference ${KREW_VERSION} (4 of them)"
fi
SHAS="$(grep -n 'sha256:' kubectl-investigate.yaml)"
while read -r line; do
  sha="$(printf '%s' "$line" | sed 's/.*sha256: *//; s/"//g; s/  *$//')"
  if [ "$sha" = "REPLACE_ME" ]; then
    wrn "krew sha256 still REPLACE_ME (fill from release assets before submitting the plugin)"
  elif ! printf '%s' "$sha" | grep -qE '^[0-9a-f]{64}$'; then
    bad "krew sha256 malformed: $sha"
  fi
done <<< "$(printf '%s\n' "$SHAS" | grep -v '^$' || true)"

# --- Formula (release-pinned to the latest tag) -----------------------------------------
[ "$QUIET" = 1 ] || printf "\n== Formula/kubetective.rb\n"
LATEST_TAG="$(git tag --sort=-version:refname | head -n1 || true)"
if [ -z "$LATEST_TAG" ]; then
  wrn "no git tags found: skipping formula tag check (CI fetches tags)"
else
  # A release in flight is a LEGITIMATE one-gap state: the release flow
  # pushes (bump commit, tag, formula) in sequence, so a CI run may start
  # between the tag and the formula commits. Tolerate the gap ONLY while
  # HEAD is the bump commit itself and the formula still pins the previous
  # released version; any older drift is still a hard failure.
  RELEASE_IN_FLIGHT=0
  if ! grep -q "archive/refs/tags/${LATEST_TAG}.tar.gz" Formula/kubetective.rb; then
    HEAD_MSG="$(git log -1 --format=%s 2>/dev/null || true)"
    PREV_TAG="$(git tag --sort=-version:refname | sed -n '2p' || true)"
    if [[ "$HEAD_MSG" == "chore: bump version to "${LATEST_TAG}"" ]] \
       && [ -n "$PREV_TAG" ] \
       && grep -q "archive/refs/tags/${PREV_TAG}.tar.gz" Formula/kubetective.rb; then
      RELEASE_IN_FLIGHT=1
      note "      (release in flight: HEAD is the ${LATEST_TAG} bump, formula pins ${PREV_TAG})"
    fi
  fi
  if grep -q "archive/refs/tags/${LATEST_TAG}.tar.gz" Formula/kubetective.rb; then
    ok "url pins latest tag ${LATEST_TAG}"
  elif [ "$RELEASE_IN_FLIGHT" = 1 ]; then
    ok "url pins previous tag ${PREV_TAG} while ${LATEST_TAG} bumps on HEAD (release in flight)"
  else
    bad "formula url != latest tag ${LATEST_TAG} (run hack/update-formula.sh ${LATEST_TAG})"
  fi
  FORMULA_SHA="$(sed -n 's/.*sha256 "\([0-9a-f]*\)".*/\1/p' Formula/kubetective.rb)"
  if printf '%s' "$FORMULA_SHA" | grep -qE '^[0-9a-f]{64}$'; then
    ok "sha256 ${FORMULA_SHA}"
  else
    bad "formula sha256 missing/malformed (${FORMULA_SHA:-empty})"
  fi
fi

# --- docs freshness ----------------------------------------------------------------------
[ "$QUIET" = 1 ] || printf "== CONTRIBUTING.md\n"
if grep -nE 'git tag v[0-9]' CONTRIBUTING.md; then
  bad "CONTRIBUTING.md contains a literal-release example; use make release VERSION=..."
else
  ok "no pinned tag example in CONTRIBUTING.md"
fi

# --- report --------------------------------------------------------------------------------
printf '\n'
if [ "$fail" -gt 0 ]; then
  printf 'Version check FAILED: %d problem(s), %d warning(s)\n' "$fail" "$nwarn" >&2
  exit 1
fi
printf 'Version check passed (engine %s, %d warning(s))\n' "$ENGINE_VERSION" "$nwarn"