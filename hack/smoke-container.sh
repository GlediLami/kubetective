#!/usr/bin/env bash
# Container smoke test (roadmap v0.9 exit criterion): build the distroless
# image, run it in a fresh kind cluster under the least-privilege RBAC from
# deploy/rbac.yaml, and assert that a real investigation completes with
# findings and the doctor passes - i.e. no permission gaps in the role.
#
# The image is shell-free by design (distroless), so the Jobs call the
# binary directly; assertions read the pod logs.
#
#   hack/smoke-container.sh [--keep]   (--keep leaves the kind cluster up)
set -euo pipefail

KIND_NAME="kt-smoke"
IMG="kubetective:smoke"
NS="kubetective"
WORK_NS="prod"

for bin in docker kind kubectl; do
  command -v "$bin" >/dev/null || { echo "missing $bin" >&2; exit 1; }
done

keep="${1:-}"
if [ "$keep" != "--keep" ]; then
  trap 'kind delete cluster --name "$KIND_NAME" >/dev/null 2>&1 || true' EXIT
fi
kind delete cluster --name "$KIND_NAME" >/dev/null 2>&1 || true

echo "== build image"
docker build --quiet -t "$IMG" -f Dockerfile .

echo "== kind cluster $KIND_NAME"
kind create cluster --name "$KIND_NAME" --wait 120s >/dev/null
kind load docker-image "$IMG" --name "$KIND_NAME" >/dev/null

kubectl() { command kubectl --context "kind-$KIND_NAME" "$@"; }

echo "== least-privilege RBAC"
kubectl create namespace "$NS" >/dev/null 2>&1 || true
kubectl create namespace "$WORK_NS" >/dev/null 2>&1 || true
kubectl apply -f deploy/rbac.yaml >/dev/null

echo "== crash-looping workload under investigation"
kubectl create deployment crashy --image=busybox:1.36 --namespace "$WORK_NS" >/dev/null
kubectl patch deployment crashy --namespace "$WORK_NS" -p \
  '{"spec":{"template":{"spec":{"containers":[{"name":"workload","image":"busybox:1.36","command":["/bin/sh","-c","exit 1"]}]}}}}' >/dev/null
kubectl rollout status deployment/crashy --namespace "$WORK_NS" --timeout=90s >/dev/null || true

echo "== job: doctor"
kubectl apply --namespace "$NS" -f - <<EOF >/dev/null
apiVersion: batch/v1
kind: Job
metadata:
  name: kt-doctor
  namespace: $NS
spec:
  backoffLimit: 0
  template:
    spec:
      serviceAccountName: kubetective
      restartPolicy: Never
      containers:
        - name: kt-doctor
          image: $IMG
          command: ["kubetective", "doctor"]
EOF
kubectl wait --for=condition=complete job/kt-doctor --namespace "$NS" --timeout=180s
kubectl logs job/kt-doctor --namespace "$NS"

echo "== job: investigate crashloop"
kubectl delete job --ignore-not-found --namespace "$NS" kt-investigate >/dev/null
kubectl apply --namespace "$NS" -f - <<EOF >/dev/null
apiVersion: batch/v1
kind: Job
metadata:
  name: kt-investigate
  namespace: $NS
spec:
  backoffLimit: 0
  template:
    spec:
      serviceAccountName: kubetective
      restartPolicy: Never
      containers:
        - name: kt-investigate
          image: $IMG
          command:
            - kubetective
            - investigate
            - deployment/crashy
            - --namespace
            - $WORK_NS
            - --since
            - 10m
            - --format
            - json
EOF
kubectl wait --for=condition=complete job/kt-investigate --namespace "$NS" --timeout=300s
kubectl logs job/kt-investigate --namespace "$NS"

echo "== assertions"
kubectl logs job/kt-doctor --namespace "$NS" > /tmp/kt-doctor.out
kubectl logs job/kt-investigate --namespace "$NS" > /tmp/kt-investigate.out
grep -q "all checks passed" /tmp/kt-doctor.out \
  || { echo "SMOKE FAIL: doctor did not pass" >&2; exit 1; }
grep -q '"crashloop"' /tmp/kt-investigate.out \
  || { echo "SMOKE FAIL: investigation found no crashloop hypothesis" >&2; exit 1; }
echo "SMOKE PASS: doctor + investigation green under least-privilege RBAC"