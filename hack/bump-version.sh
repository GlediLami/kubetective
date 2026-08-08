#!/usr/bin/env bash
# Bump the canonical version across every version sink and prove consistency.
#
#   hack/bump-version.sh v0.10.0        # release version
#   hack/bump-version.sh v0.10.0-dev    # inter-release dev version
#
# Updates:
#   internal/engine/engine.go   canonical Version var
#   Dockerfile                  ldflags stamp
#   kubectl-investigate.yaml    krew spec.version + platform uris
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

K="${V%-dev}"        # krew manifest only carries released versions
B="${K#v}"           # archive name uses the bare version (0.9.0)

sed -i.bak "s|^var Version = .*|var Version = \"${V}\"|" internal/engine/engine.go
sed -i.bak "s|engine.Version=[^\"]*|engine.Version=${V}|" Dockerfile
sed -i.bak -E \
  -e "s|^  version: \".*\"|  version: \"${K}\"|" \
  -e "s|/releases/download/v[0-9][0-9a-z.-]*|/releases/download/${K}|g" \
  -e "s|kubectl-investigate_[0-9]+[0-9a-z.-]*_|kubectl-investigate_${B}_|g" \
  kubectl-investigate.yaml
rm -f internal/engine/engine.go.bak Dockerfile.bak kubectl-investigate.yaml.bak

./hack/check-version.sh
echo "version bumped to ${V}"