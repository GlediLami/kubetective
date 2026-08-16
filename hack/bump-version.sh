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

sed -i.bak "s|^var Version = .*|var Version = \"${V}\"|" internal/engine/engine.go
sed -i.bak "s|engine.Version=[^\"]*|engine.Version=${V}|" Dockerfile
rm -f internal/engine/engine.go.bak Dockerfile.bak

./hack/check-version.sh
echo "version bumped to ${V}"