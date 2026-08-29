#!/usr/bin/env bash
# Bump the canonical version across every version sink and prove consistency.
#
#   hack/bump-version.sh v0.10.0        # release version
#   hack/bump-version.sh v0.10.0-dev    # inter-release dev version
#
# Updates:
#   internal/engine/engine.go   canonical Version var
#   Dockerfile                  ldflags stamp
#
# The brew formula is NOT touched here: it pins the latest git TAG, so it
# only moves when hack/release.sh (or hack/update-formula.sh) runs.
set -euo pipefail

V="${1:?usage: hack/bump-version.sh <version>}"
if ! [[ "$V" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
  echo "invalid version '${V}' - expected vX.Y.Z or vX.Y.Z-<label>" >&2
  exit 1
fi

cd "$(git rev-parse --show-toplevel)"

# Version sits in a `var (...)` block, so the pattern must tolerate leading
# indentation and an absent `var` keyword. \{0,1\} is a POSIX BRE interval and
# works under both GNU and BSD sed.
sed -i.bak "s|^\([ 	]*\)\(var \)\{0,1\}Version\([ 	]*\)=[ 	]*\".*\"|\1\2Version\3= \"${V}\"|" internal/engine/engine.go
sed -i.bak "s|engine.Version=[^\"]*|engine.Version=${V}|" Dockerfile
rm -f internal/engine/engine.go.bak Dockerfile.bak

# A no-op sed is the failure mode that matters here: it leaves the canonical
# version untouched and every other sink bumped. Fail loudly instead.
STAMPED="$(awk -F'"' \
  '/^[[:space:]]*(var[[:space:]]+)?Version[[:space:]]*=[[:space:]]*"/ {print $2; exit}' \
  internal/engine/engine.go)"
if [ "$STAMPED" != "$V" ]; then
  echo "bump-version: engine.go still reads '${STAMPED:-<unreadable>}' after the rewrite" >&2
  exit 1
fi

./hack/check-version.sh
echo "version bumped to ${V}"