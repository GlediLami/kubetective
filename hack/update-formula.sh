#!/usr/bin/env bash
# Updates Formula/kubedoctor.rb with the release tarball URL + sha256.
#
# Usage:   hack/update-formula.sh <git-tag>
# Example: hack/update-formula.sh v0.7.0
#
# Run AFTER tagging the release on GitHub (a release object is not required —
# brew fetches the tag tarball). Then commit and push the formula change.
set -euo pipefail

VERSION="${1:?usage: hack/update-formula.sh <git-tag>}"
REPO="${KUBEDOCTOR_REPO:-kubedoctor/kubedoctor}"
URL="https://github.com/${REPO}/archive/refs/tags/${VERSION}.tar.gz"

echo "fetching ${URL} ..."
SHA="$(curl -sL "${URL}" | shasum -a 256 | awk '{print $1}')"
if [ -z "${SHA}" ]; then
  echo "error: could not fetch release tarball — is the tag pushed?" >&2
  exit 1
fi

FORMULA="Formula/kubedoctor.rb"
sed -i.bak -E "s|url \".*\"|url \"${URL}\"|; s|sha256 \".*\"|sha256 \"${SHA}\"|" "${FORMULA}"
rm -f "${FORMULA}.bak"

echo "Formula/kubedoctor.rb updated:"
grep -E 'url |sha256 ' "${FORMULA}"
