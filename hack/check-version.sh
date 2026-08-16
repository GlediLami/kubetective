#!/usr/bin/env bash
# Version consistency gate (v0.9): every hardcoded version sink in the repo
# must agree with the canonical version in internal/engine/engine.go.
#
#   hack/check-version.sh [--quiet]
#
# Sinks checked:
#   engine   internal/engine/engine.go      -- canonical (single source)
#   docker   Dockerfile ldflags Version     -- must equal engine exactly
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
# --- Dockerfile ------------------------------------------------------------------------
[ "$QUIET" = 1 ] || echo "== Dockerfile"
if grep -q -- "engine.Version=${ENGINE_VERSION}" Dockerfile; then
  ok "ldflags Version=${ENGINE_VERSION}"
else
  bad "Dockerfile ldflags Version must be exactly ${ENGINE_VERSION}"
fi

# --- Formula (release-pinned to the latest tag) -----------------------------------------
[ "$QUIET" = 1 ] || printf "\n== Formula/kubetective.rb\n"
LATEST_TAG="$(git tag --sort=-version:refname | head -n1 || true)"
if [ -z "$LATEST_TAG" ]; then
  wrn "no git tags found: skipping formula tag check (CI fetches tags)"
else
  # A release in flight is a LEGITIMATE one-gap state, and for a tagged build
  # it is the ONLY possible state: the formula's sha256 is computed from the
  # tag's own source tarball, so the formula can never pin the tag it ships
  # inside - updating it would change the tree and therefore the sha. The
  # release flow pushes bump -> tag -> formula in sequence for exactly that
  # reason, and .github/workflows/release.yml builds from the tag, where the
  # formula still points at the previous release by construction.
  #
  # Tolerate the gap while the formula pins the previous released version AND
  # either HEAD is the bump commit (a CI run racing the formula commit) or
  # HEAD carries the latest tag (a release build). Any older drift is still a
  # hard failure.
  RELEASE_IN_FLIGHT=0
  if ! grep -q "archive/refs/tags/${LATEST_TAG}.tar.gz" Formula/kubetective.rb; then
    HEAD_MSG="$(git log -1 --format=%s 2>/dev/null || true)"
    PREV_TAG="$(git tag --sort=-version:refname | sed -n '2p' || true)"
    HEAD_TAGS="$(git tag --points-at HEAD 2>/dev/null || true)"
    WHY=""
    if [[ "$HEAD_MSG" == "chore: bump version to "${LATEST_TAG}"" ]]; then
      WHY="HEAD is the ${LATEST_TAG} bump"
    elif printf '%s\n' "$HEAD_TAGS" | grep -qx -- "${LATEST_TAG}"; then
      WHY="HEAD is tagged ${LATEST_TAG} (release build)"
    fi
    if [ -n "$WHY" ] \
       && [ -n "$PREV_TAG" ] \
       && grep -q "archive/refs/tags/${PREV_TAG}.tar.gz" Formula/kubetective.rb; then
      RELEASE_IN_FLIGHT=1
      note "      (release in flight: ${WHY}, formula pins ${PREV_TAG})"
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

# --- brew tap coordinates ---------------------------------------------------------------
# `brew install <owner>/<tap>/<formula>` resolves to the repo
# <owner>/homebrew-<tap>. Ours is GlediLami/homebrew-kubetective, so the tap
# segment must be "kubetective" - writing "tap" points brew at
# GlediLami/homebrew-tap, which does not exist. That shipped in the README once;
# this check is why it will not again.
[ "$QUIET" = 1 ] || printf "\n== brew install one-liner\n"
BREW_EXPECT="gledilami/kubetective/kubetective"
BREW_LINES="$(grep -rhoiE 'brew install [a-z0-9./-]+' README.md site/index.html CONTRIBUTING.md 2>/dev/null | sed 's/^[Bb]rew install //' | sort -u || true)"
if [ -z "$BREW_LINES" ]; then
  wrn "no brew install command found in the docs"
else
  while read -r coord; do
    [ -z "$coord" ] && continue
    if [ "$(printf '%s' "$coord" | tr '[:upper:]' '[:lower:]')" = "$BREW_EXPECT" ]; then
      ok "brew install ${coord}"
    else
      bad "brew install ${coord} -> tap repo does not exist; want ${BREW_EXPECT}"
    fi
  done <<< "$BREW_LINES"
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