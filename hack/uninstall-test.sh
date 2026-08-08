#!/usr/bin/env bash
# The "uninstall test" (roadmap v1.0 #10): a fresh operator, with no
# maintainer in the loop, follows the documented path - fresh clone, build,
# verify - and every documented failure mode produces an actionable message
# instead of a stack trace.
#
#   hack/uninstall-test.sh          # runs against a temp clone of this repo
#   hack/uninstall-test.sh /path    # runs against an existing checkout
#
# Runs without a cluster: the benchmark suite replays scenario records
# offline, and `doctor` is expected to FAIL with a readable message (no
# cluster in the environment) - the test asserts the failure mode, it does
# not require a cluster to pass.
set -euo pipefail

SRC="$(git rev-parse --show-toplevel)"
[ "${1:-}" != "" ] && SRC="$1"

WORK="$(mktemp -d /tmp/kubetective-uninstall.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT
CLONE="$WORK/kubetective"

echo "== fresh clone"
git clone --quiet "$SRC" "$CLONE"
cd "$CLONE"

echo "== documented build path (make build)"
make build

BIN="$CLONE/bin"

echo "== version + help (the two binaries)"
"$BIN/kubetective" version | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$' \
  || { echo "FAIL: kubetective version not vX.Y.Z" >&2; exit 1; }
"$BIN/kubetective" --help | grep -q investigate || { echo "FAIL: --help missing investigate" >&2; exit 1; }
"$BIN/kubectl-investigate" --help >/dev/null || { echo "FAIL: plugin binary --help" >&2; exit 1; }
"$BIN/kubectl-investigate" --help | grep -q kubectl || { echo "FAIL: plugin binary --help unusable" >&2; exit 1; }

echo "== benchmark gate runs offline (no cluster)"
"$BIN/kubetective" benchmark >/dev/null

echo "== doctor failure mode without a cluster is actionable, not a trace"
set +e
OUT="$("$BIN/kubetective" doctor 2>&1)"
RC=$?
set -e
if [ "$RC" -eq 0 ]; then
  echo "FAIL: doctor passed without a cluster (unexpected: kubeconfig present?)" >&2
  exit 1
fi
if ! printf '%s' "$OUT" | grep -qiE 'kubeconfig|cluster|KUBECTL_VENDORED_CONFIG_path {1,2}\[|configuration'; then
  echo "FAIL: doctor failure message is not actionable:" >&2
  printf '%s\n' "$OUT" >&2
  exit 1
fi
 if [[ "$OUT" == *"panic:"* ]] || [[ "$OUT" == *"goroutine"* ]]; then
  echo "FAIL: doctor printed a stack trace" >&2
  exit 1
fi

echo ""
echo "uninstall test PASSED (fresh clone: build, binaries, offline benchmark, actionable doctor failure)"