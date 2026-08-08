#!/usr/bin/env bash
# Container smoke test (roadmap v0.9 + v1.0 exit criteria): build the
# distroless image, run it in a fresh kind cluster under the least-privilege
# RBAC from deploy/rbac.yaml, and assert:
#   - doctor passes and a real investigation completes with findings
#     (no permission gaps in the role)
#   - the remediation actions E2E: preview plans a restart-pod, apply is
#     gated behind --yes, and an approved apply deletes + recreates the pod
#     and appends an audit record.
#
# Incident records persist across jobs via a PVC (KUBETECTIVE_INCIDENTS_DIR).
# The image is shell-free by design (distroless), so the Jobs call the
# binary directly; the host script extracts IDs from the JSON logs.
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

kubectl get storageclass standard >/dev/null 2>&1 \
  || { echo "SMOKE FAIL: kind needs a default storageclass (standard) for the records PVC" >&2; exit 1; }

echo "== record store PVC"
kubectl apply --namespace "$NS" -f - <<'EOF' >/dev/null
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: kt-records
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 1Mi
EOF

echo "== crash-looping workload under investigation"
kubectl create deployment crashy --image=busybox:1.36 --namespace "$WORK_NS" >/dev/null
kubectl patch deployment crashy --namespace "$WORK_NS" -p \
  '{"spec":{"template":{"spec":{"containers":[{"name":"workload","image":"busybox:1.36","command":["/bin/sh","-c","exit 1"]}]}}}}' >/dev/null
kubectl rollout status deployment/crashy --namespace "$WORK_NS" --timeout=90s >/dev/null || true
CRASHY_POD=$(kubectl get pods --namespace "$WORK_NS" -l app=crashy -o jsonpath='{.items[0].metadata.name}')
[ -n "$CRASHY_POD" ] || { echo "SMOKE FAIL: no crashy pod found" >&2; exit 1; }

# The action E2E below needs a record that already shows the crash loop:
# wait until the pod is restarted at least twice while still failing, so
# the crash-loop evidence cannot race the investigation job.
echo "== wait for crashy pod to reach CrashLoopBackOff (>=2 restarts)"
CRASHY_OK=0
for i in $(seq 1 36); do
  STATE=$(kubectl get pod "$CRASHY_POD" --namespace "$WORK_NS" \
    -o jsonpath='{.status.containerStatuses[0].state.waiting.reason}..{.status.containerStatuses[0].restartCount}' 2>/dev/null || true)
  REASON=${STATE%%..*}
  RESTART=${STATE##*..}
  if [ "$REASON" = "CrashLoopBackOff" ] && [ "${RESTART:-0}" -ge 2 ]; then
    CRASHY_OK=1
    break
  fi
  sleep 5
done
if [ "$CRASHY_OK" != 1 ]; then
  echo "SMOKE FAIL: crashy pod never reached CrashLoopBackOff with 2 restarts" >&2
  kubectl get pods --namespace "$WORK_NS" -l app=crashy -o wide >&2
  exit 1
fi
echo "  pod $CRASHY_POD is in CrashLoopBackOff (restart #$RESTART)"

# Common job template: distroless binary + record store on the PVC.
run_job() {
  local name=$1; shift
  local args=""
  for a in "$@"; do
    args+="            - $a
"
  done
  kubectl apply --namespace "$NS" -f - <<EOF >/dev/null
apiVersion: batch/v1
kind: Job
metadata:
  name: $name
  namespace: $NS
spec:
  backoffLimit: 0
  template:
    spec:
      serviceAccountName: kubetective
      restartPolicy: Never
      containers:
        - name: kt
          image: $IMG
          env:
            - name: KUBETECTIVE_INCIDENTS_DIR
              value: /data
          volumeMounts:
            - name: records
              mountPath: /data
          command:
            - kubetective
$args      volumes:
        - name: records
          persistentVolumeClaim:
            claimName: kt-records
EOF
}

echo "== job: doctor"
run_job kt-doctor doctor
kubectl wait --for=condition=complete job/kt-doctor --namespace "$NS" --timeout=180s
kubectl logs job/kt-doctor --namespace "$NS"

echo "== job: investigate deployment/crashy"
run_job kt-investigate investigate deployment/crashy --namespace "$WORK_NS" --since 10m --format json
kubectl wait --for=condition=complete job/kt-investigate --namespace "$NS" --timeout=300s
kubectl logs job/kt-investigate --namespace "$NS" > /tmp/kt-investigate.out

echo "== job: investigate pod/$CRASHY_POD (actions chain target, retried into the crash state)"
# A crash-looping pod oscillates between Running and CrashLoopBackOff; the
# action planner only recommends restart-pod for a record whose incident
# status shows the bad state. Retry the investigation until the captured
# record shows it (Plan() is pure, so the preview is then deterministic).
ACT_ID=""
POD_INCIDENT_ID=""
for attempt in $(seq 1 5); do
  run_job "kt-investigate-pod-${attempt}" investigate "pod/$CRASHY_POD" \
    --namespace "$WORK_NS" --since 10m --format json
  kubectl wait --for=condition=complete "job/kt-investigate-pod-${attempt}" --namespace "$NS" --timeout=300s
  kubectl logs "job/kt-investigate-pod-${attempt}" --namespace "$NS" > /tmp/kt-investigate-pod.out

  POD_INCIDENT_ID=$(grep -o '"record_id": *"[^"]*"' /tmp/kt-investigate-pod.out \
    | head -1 | sed 's/.*"record_id": *"//;s/"$//' | sed 's#.*/##;s/\.jsonl$//')
  [ -n "$POD_INCIDENT_ID" ] || { echo "SMOKE FAIL: no record_id in pod investigation" >&2; exit 1; }

  run_job "kt-actions-preview-${attempt}" action "$POD_INCIDENT_ID"
  kubectl wait --for=condition=complete "job/kt-actions-preview-${attempt}" --namespace "$NS" --timeout=180s
  kubectl logs "job/kt-actions-preview-${attempt}" --namespace "$NS" > /tmp/kt-actions-preview.out
  ACT_ID=$(awk '/restart-pod/{print $1}' /tmp/kt-actions-preview.out | head -1)
  if [ -n "$ACT_ID" ]; then
    echo "  attempt ${attempt}: record ${POD_INCIDENT_ID} plans restart-pod"
    break
  fi
  echo "  attempt ${attempt}: record showed no crash state yet, retrying in 15s"
  kubectl delete job "kt-investigate-pod-${attempt}" "kt-actions-preview-${attempt}" --namespace "$NS" >/dev/null 2>&1 || true
  sleep 15
done
[ -n "$ACT_ID" ] || { echo "SMOKE FAIL: preview planned no restart-pod action (5 tries)" >&2; exit 1; }

echo "== job: apply without --yes must be rejected (gate)"
run_job kt-actions-gate action "$POD_INCIDENT_ID" --apply "$ACT_ID"
kubectl wait --for=jsonpath='{.status.phase}'=Failed pod --selector=job-name=kt-actions-gate --namespace "$NS" --timeout=180s >/dev/null
GATE_POD=$(kubectl get pods --namespace "$NS" -l job-name=kt-actions-gate -o jsonpath='{.items[0].metadata.name}')
kubectl logs "$GATE_POD" --namespace "$NS" > /tmp/kt-actions-gate.out || true
kubectl delete job kt-actions-gate --namespace "$NS" >/dev/null

echo "== job: apply with --yes"
run_job kt-actions-apply action "$POD_INCIDENT_ID" --apply "$ACT_ID" --yes
kubectl wait --for=condition=complete job/kt-actions-apply --namespace "$NS" --timeout=180s
kubectl logs job/kt-actions-apply --namespace "$NS" > /tmp/kt-actions-apply.out

echo "== assertions"
grep -q "all checks passed" <(kubectl logs job/kt-doctor --namespace "$NS") \
  || { echo "SMOKE FAIL: doctor did not pass" >&2; exit 1; }
grep -q '"crashloop"' /tmp/kt-investigate.out \
  || { echo "SMOKE FAIL: deployment investigation found no crashloop hypothesis" >&2; exit 1; }
grep -q '"crashloop"' /tmp/kt-investigate-pod.out \
  || { echo "SMOKE FAIL: pod investigation found no crashloop hypothesis" >&2; exit 1; }
grep -q 'restart-pod' /tmp/kt-actions-preview.out \
  || { echo "SMOKE FAIL: preview did not plan restart-pod" >&2; exit 1; }
grep -q 'approval required' /tmp/kt-actions-gate.out \
  || { echo "SMOKE FAIL: apply without --yes was not rejected" >&2; exit 1; }
grep -q "✓" /tmp/kt-actions-apply.out \
  || { echo "SMOKE FAIL: approved apply did not report success" >&2; exit 1; }
grep -q "audit appended" /tmp/kt-actions-apply.out \
  || { echo "SMOKE FAIL: approved apply wrote no audit record" >&2; exit 1; }
grep -q "deleted pod/" /tmp/kt-actions-apply.out \
  || { echo "SMOKE FAIL: apply did not execute the restart-pod action" >&2; exit 1; }
# The terminating pod lingers briefly, poll for a pod with a different name.
NEW_POD=""
for _ in $(seq 1 40); do
  for p in $(kubectl get pods --namespace "$WORK_NS" -l app=crashy -o name | sed 's#pod/##'); do
    if [ -n "$p" ] && [ "$p" != "$CRASHY_POD" ]; then
      NEW_POD="$p"
      break 2
    fi
  done
  sleep 2
done
[ -n "$NEW_POD" ] \
  || { echo "SMOKE FAIL: apply did not recreate the pod" >&2; exit 1; }
echo "SMOKE PASS: doctor + investigation + actions E2E green under least-privilege RBAC"