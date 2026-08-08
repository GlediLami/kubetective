package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/pkg/api"
)

// DefaultMaxLogLines caps log tail per container (Stage B, on demand only).
const DefaultMaxLogLines = 50

// namespacePodCap bounds namespace-wide investigations (large-cluster guard).
const namespacePodCap = 500

// Collector normalizes Kubernetes API state into Observations.
type Collector struct {
	client kubernetes.Interface
	// svcCache caches a namespace's services within one Collect call (the
	// engine runs collectors sequentially, so this is safe).
	svcCache map[string][]corev1.Service
}

var _ collect.Collector = (*Collector)(nil)

func New(client kubernetes.Interface) *Collector {
	return &Collector{client: client, svcCache: map[string][]corev1.Service{}}
}

func (c *Collector) ID() string { return "kubernetes" }

func (c *Collector) Collect(ctx context.Context, scope *collect.ScopePlan) ([]model.Observation, []model.SourceRef, error) {
	var obs []model.Observation
	var refs []model.SourceRef

	for _, t := range scope.Targets {
		var o []model.Observation
		var r []model.SourceRef
		var err error
		switch strings.ToLower(t.Kind) {
		case "pod":
			o, r, err = c.collectPod(ctx, scope, t.Namespace, t.Name)
		case "deployment", "deploy":
			o, r, err = c.collectDeployment(ctx, scope, t.Namespace, t.Name)
		case "namespace", "ns":
			o, r, err = c.collectNamespace(ctx, scope, t.Namespace)
		case "":
			o, r, err = c.collectNamespace(ctx, scope, t.Namespace)
		default:
			err = fmt.Errorf("kubernetes collector: unsupported target kind %q (v0.1: pod, deployment, namespace)", t.Kind)
		}
		if err != nil {
			return obs, refs, err
		}
		obs = append(obs, o...)
		refs = append(refs, r...)
	}
	// coreDNS availability (DNS-failure evidence): cheap and always fetched
	// when RBAC allows; skipped silently otherwise (like the GitOps CRDs).
	for _, name := range []string{"coredns", "kube-dns"} {
		dep, err := c.client.AppsV1().Deployments("kube-system").Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			continue // not found or no RBAC - the DNS analyzer reports a gap-free lower score
		}
		res := model.ResourceRef{Kind: "deployment", Namespace: "kube-system", Name: dep.Name}
		ref := model.SourceRef{System: "k8s", Query: "GET deployments/" + name}
		obs = append(obs, deploymentStateObservation(dep, res, ref))
	}
	// Dedup at the boundary: overlapping scope expansion (e.g. deployment
	// state fetched both directly and via the pod owner chain) emits the same
	// content-hashed observation twice - collapse before handing over.
	return dedupObservations(obs), refs, nil
}

// dedupObservations keeps the first observation per content-hashed ID.
func dedupObservations(obs []model.Observation) []model.Observation {
	seen := make(map[string]bool, len(obs))
	out := make([]model.Observation, 0, len(obs))
	for _, o := range obs {
		if seen[o.ID] {
			continue
		}
		seen[o.ID] = true
		out = append(out, o)
	}
	return out
}

func (c *Collector) collectPod(ctx context.Context, scope *collect.ScopePlan, ns, name string) ([]model.Observation, []model.SourceRef, error) {
	pod, err := c.client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("get pod %s/%s: %w", ns, name, err)
	}
	refs := []model.SourceRef{{
		System: "k8s",
		Query:  fmt.Sprintf("GET /api/v1/namespaces/%s/pods/%s", ns, name),
	}}
	res := model.ResourceRef{Kind: "pod", Namespace: ns, Name: name}
	obs := []model.Observation{podStateObservation(pod, res, refs[0])}
	// Owner chain: emit one resource.owner observation per hop so the graph
	// builder can link Pod ← ReplicaSet ← Deployment (or Pod ← Deployment).
	obs = append(obs, c.ownerChainObservations(ctx, pod, res, refs[0])...)
	// Scope expansion: fetch the top-level controller's state (Deployment /
	// StatefulSet / DaemonSet) so a pod-target investigation can surface
	// "the owning workload changed".
	obs = append(obs, c.controllerStateObservation(ctx, pod, res, refs[0])...)
	// Storage + routing context: PVCs backing the pod's volumes, services
	// selecting the pod, and the HPA that manages it (v0.3 analyzers).
	obs = append(obs, c.pvcObservations(ctx, pod, res, refs[0])...)
	obs = append(obs, c.serviceObservations(ctx, pod, res, refs[0])...)
	obs = append(obs, c.hpaObservations(ctx, pod, res, refs[0])...)

	// Container specs + states.
	for i := range pod.Spec.Containers {
		ctr := &pod.Spec.Containers[i]
		obs = append(obs, containerSpecObservation(pod, ctr, res, refs[0]))
		obs = append(obs, containerStateObservation(pod, ctr, res, refs[0]))
	}
	for i := range pod.Spec.InitContainers {
		ctr := &pod.Spec.InitContainers[i]
		obs = append(obs, containerSpecObservation(pod, ctr, res, refs[0]))
		obs = append(obs, containerStateObservation(pod, ctr, res, refs[0]))
	}

	// Events touching this pod (window-filtered).
	evs, err := c.eventsFor(ctx, ns, "Pod", name, scope.Window)
	if err == nil {
		obs = append(obs, evs...)
	} else {
		refs = append(refs, model.SourceRef{System: "k8s", Query: "list events", RawRef: err.Error()})
	}

	// Node state (RUNS_ON edge source + node-pressure contradiction evidence).
	if pod.Spec.NodeName != "" {
		node, nerr := c.client.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
		if nerr == nil {
			obs = append(obs, nodeObservations(node, refs[0])...)
		}
	}

	// Logs: Stage B, on demand, capped.
	if scope.Logs {
		obs = append(obs, c.logSnippets(ctx, scope, pod, res, refs[0])...)
	}
	return obs, refs, nil
}

func (c *Collector) collectDeployment(ctx context.Context, scope *collect.ScopePlan, ns, name string) ([]model.Observation, []model.SourceRef, error) {
	dep, err := c.client.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("get deployment %s/%s: %w", ns, name, err)
	}
	refs := []model.SourceRef{{
		System: "k8s",
		Query:  fmt.Sprintf("GET /apis/apps/v1/namespaces/%s/deployments/%s", ns, name),
	}}
	depRes := model.ResourceRef{Kind: "deployment", Namespace: ns, Name: name}
	obs := []model.Observation{deploymentStateObservation(dep, depRes, refs[0])}

	// Events on the deployment itself.
	evs, err := c.eventsFor(ctx, ns, "Deployment", name, scope.Window)
	if err == nil {
		obs = append(obs, evs...)
	}

	// Owner chain: Deployment → ReplicaSets → Pods.
	rsList, err := c.client.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{})
	if err == nil {
		for i := range rsList.Items {
			rs := &rsList.Items[i]
			if !ownedBy(rs.OwnerReferences, "Deployment", dep.UID) {
				continue
			}
			pods, perr := c.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
			if perr != nil {
				continue
			}
			for j := range pods.Items {
				p := &pods.Items[j]
				if !ownedBy(p.OwnerReferences, "ReplicaSet", rs.UID) {
					continue
				}
				podObs, podRefs, perr := c.collectPod(ctx, scope, ns, p.Name)
				if perr != nil {
					continue // pod may have been deleted mid-investigation
				}
				obs = append(obs, podObs...)
				refs = append(refs, podRefs...)
			}
		}
	}
	return obs, refs, nil
}

func (c *Collector) collectNamespace(ctx context.Context, scope *collect.ScopePlan, ns string) ([]model.Observation, []model.SourceRef, error) {
	pods, err := c.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{Limit: namespacePodCap})
	if err != nil {
		return nil, nil, fmt.Errorf("list pods in namespace %s: %w", ns, err)
	}
	var obs []model.Observation
	var refs []model.SourceRef
	for i := range pods.Items {
		p := &pods.Items[i]
		o, r, err := c.collectPod(ctx, scope, ns, p.Name)
		if err != nil {
			continue
		}
		obs = append(obs, o...)
		refs = append(refs, r...)
	}
	return obs, refs, nil
}

// ownerChainObservations resolves the pod's owner (and the owner's owner, e.g.
// ReplicaSet → Deployment) so the graph can build full OWNS chains.
func (c *Collector) ownerChainObservations(ctx context.Context, pod *corev1.Pod, res model.ResourceRef, ref model.SourceRef) []model.Observation {
	var obs []model.Observation
	for _, owner := range pod.OwnerReferences {
		ownerRes := model.ResourceRef{Kind: owner.Kind, Namespace: pod.Namespace, Name: owner.Name}
		obs = append(obs, collect.NewObservation(
			"resource.owner",
			ref,
			pod.CreationTimestamp.Time,
			res,
			map[string]any{"owner_kind": owner.Kind, "owner_name": owner.Name},
			1.0,
		))
		// One more hop up for ReplicaSet → Deployment (cheap single GET).
		if owner.Kind == "ReplicaSet" {
			rs, err := c.client.AppsV1().ReplicaSets(pod.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
			if err != nil {
				continue
			}
			for _, rsOwner := range rs.OwnerReferences {
				ts := rs.CreationTimestamp.Time
				if ts.IsZero() {
					ts = pod.CreationTimestamp.Time
				}
				obs = append(obs, collect.NewObservation(
					"resource.owner",
					ref,
					ts,
					ownerRes,
					map[string]any{"owner_kind": rsOwner.Kind, "owner_name": rsOwner.Name},
					1.0,
				))
			}
		}
	}
	return obs
}

// pvcObservations fetches the PersistentVolumeClaims backing the pod's
// volumes and normalizes their binding state.
func (c *Collector) pvcObservations(ctx context.Context, pod *corev1.Pod, res model.ResourceRef, ref model.SourceRef) []model.Observation {
	var obs []model.Observation
	for _, vol := range pod.Spec.Volumes {
		if vol.PersistentVolumeClaim == nil {
			continue
		}
		name := vol.PersistentVolumeClaim.ClaimName
		pvc, err := c.client.CoreV1().PersistentVolumeClaims(pod.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			continue
		}
		pvcRes := model.ResourceRef{Kind: "pvc", Namespace: pod.Namespace, Name: name}
		obs = append(obs, collect.NewObservation(
			"pvc.state",
			ref,
			pvc.CreationTimestamp.Time,
			pvcRes,
			map[string]any{
				"phase":       string(pvc.Status.Phase),
				"requested":   pvc.Spec.Resources.Requests.Storage().String(),
				"capacity":    pvc.Status.Capacity.Storage().String(),
				"volume_name": pvc.Spec.VolumeName,
				"pod":         pod.Name,
			},
			1.0,
		))
		// Binding failures arrive as events on the PVC itself.
		if evs, err := c.eventsFor(ctx, pod.Namespace, "PersistentVolumeClaim", name, api.Window{}); err == nil {
			obs = append(obs, evs...)
		}
	}
	return obs
}

// serviceObservations finds Services whose selector matches the pod and
// normalizes their endpoints state (the 503 / selector-mismatch context).
func (c *Collector) serviceObservations(ctx context.Context, pod *corev1.Pod, res model.ResourceRef, ref model.SourceRef) []model.Observation {
	services, err := c.servicesInNamespace(ctx, pod.Namespace)
	if err != nil {
		return nil
	}
	var obs []model.Observation
	for i := range services {
		svc := &services[i]
		if len(svc.Spec.Selector) == 0 {
			continue
		}
		if !labelsMatch(svc.Spec.Selector, pod.Labels) {
			continue
		}
		ready, total := c.endpointsCount(ctx, svc)
		obs = append(obs, collect.NewObservation(
			"service.state",
			ref,
			svc.CreationTimestamp.Time,
			model.ResourceRef{Kind: "service", Namespace: pod.Namespace, Name: svc.Name},
			map[string]any{
				"selector":        svc.Spec.Selector,
				"matching_pods":   1, // the pod we are investigating matches
				"ready_endpoints": ready,
				"total_endpoints": total,
				"ports":           len(svc.Spec.Ports),
			},
			1.0,
		))
	}
	return obs
}

// hpaObservations finds the HorizontalPodAutoscaler managing the pod's
// workload (via owner chain) and normalizes its scaling state.
func (c *Collector) hpaObservations(ctx context.Context, pod *corev1.Pod, res model.ResourceRef, ref model.SourceRef) []model.Observation {
	controller := c.topControllerName(pod)
	if controller == "" {
		return nil
	}
	hpas, err := c.client.AutoscalingV2().HorizontalPodAutoscalers(pod.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	var obs []model.Observation
	for i := range hpas.Items {
		hpa := &hpas.Items[i]
		if hpa.Spec.ScaleTargetRef.Name != controller {
			continue
		}
		current := int64(hpa.Status.CurrentReplicas)
		desired := int64(hpa.Status.DesiredReplicas)
		minReplicas := int64(1)
		if hpa.Spec.MinReplicas != nil {
			minReplicas = int64(*hpa.Spec.MinReplicas)
		}
		obs = append(obs, collect.NewObservation(
			"hpa.state",
			ref,
			hpa.CreationTimestamp.Time,
			model.ResourceRef{Kind: "hpa", Namespace: pod.Namespace, Name: hpa.Name},
			map[string]any{
				"min_replicas":     minReplicas,
				"max_replicas":     int64(hpa.Spec.MaxReplicas),
				"current_replicas": current,
				"desired_replicas": desired,
				"target":           hpa.Spec.Metrics,
				"workload":         controller,
				"at_max":           current >= int64(hpa.Spec.MaxReplicas),
			},
			1.0,
		))
	}
	return obs
}

// servicesInNamespace lists a namespace's services, cached per Collect call.
func (c *Collector) servicesInNamespace(ctx context.Context, ns string) ([]corev1.Service, error) {
	if cached, ok := c.svcCache[ns]; ok {
		return cached, nil
	}
	list, err := c.client.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	c.svcCache[ns] = list.Items
	return list.Items, nil
}

// endpointsCount reads the Endpoints object matching a Service.
func (c *Collector) endpointsCount(ctx context.Context, svc *corev1.Service) (ready, total int) {
	ep, err := c.client.CoreV1().Endpoints(svc.Namespace).Get(ctx, svc.Name, metav1.GetOptions{})
	if err != nil {
		return 0, 0
	}
	for _, subset := range ep.Subsets {
		ready += len(subset.Addresses)
		total += len(subset.Addresses) + len(subset.NotReadyAddresses)
	}
	return ready, total
}

// topControllerName walks the owner chain to the top-level controller name
// (Deployment/StatefulSet/DaemonSet), or "" if none.
func (c *Collector) topControllerName(pod *corev1.Pod) string {
	if len(pod.OwnerReferences) == 0 {
		return ""
	}
	owner := pod.OwnerReferences[0]
	for hops := 0; hops < 3; hops++ {
		switch owner.Kind {
		case "ReplicaSet":
			rs, err := c.client.AppsV1().ReplicaSets(pod.Namespace).Get(context.Background(), owner.Name, metav1.GetOptions{})
			if err != nil || len(rs.OwnerReferences) == 0 {
				return ""
			}
			owner = rs.OwnerReferences[0]
		case "Deployment", "StatefulSet", "DaemonSet":
			return owner.Name
		default:
			return ""
		}
	}
	return ""
}

func labelsMatch(selector, labels map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// controllerStateObservation walks the owner chain to the top-level
// controller and emits its state observation (deployment.state today).
func (c *Collector) controllerStateObservation(ctx context.Context, pod *corev1.Pod, res model.ResourceRef, ref model.SourceRef) []model.Observation {
	if len(pod.OwnerReferences) == 0 {
		return nil
	}
	// Walk up: pod → RS → Deployment (max 3 hops).
	owner := pod.OwnerReferences[0]
	ns := pod.Namespace
	for hops := 0; hops < 3; hops++ {
		switch owner.Kind {
		case "ReplicaSet":
			rs, err := c.client.AppsV1().ReplicaSets(ns).Get(ctx, owner.Name, metav1.GetOptions{})
			if err != nil || len(rs.OwnerReferences) == 0 {
				return nil
			}
			owner = rs.OwnerReferences[0]
		case "Deployment":
			dep, err := c.client.AppsV1().Deployments(ns).Get(ctx, owner.Name, metav1.GetOptions{})
			if err != nil {
				return nil
			}
			depRes := model.ResourceRef{Kind: "deployment", Namespace: ns, Name: owner.Name}
			return []model.Observation{deploymentStateObservation(dep, depRes, ref)}
		default:
			return nil // StatefulSet/DaemonSet/Job state lands in a later milestone
		}
	}
	return nil
}

// eventsFor lists events whose involved object matches, filtered by window.
func (c *Collector) eventsFor(ctx context.Context, ns, kind, name string, w api.Window) ([]model.Observation, error) {
	events, err := c.client.CoreV1().Events(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	ref := model.SourceRef{System: "k8s", Query: fmt.Sprintf("list events (involved=%s/%s)", kind, name)}
	var obs []model.Observation
	for i := range events.Items {
		ev := &events.Items[i]
		if ev.InvolvedObject.Kind != kind || ev.InvolvedObject.Name != name {
			continue
		}
		ts := eventTime(ev)
		if !w.Contains(ts) {
			continue
		}
		obs = append(obs, collect.NewObservation(
			"event.recorded",
			ref,
			ts,
			model.ResourceRef{Kind: strings.ToLower(kind), Namespace: ns, Name: name},
			map[string]any{
				"type":    ev.Type,
				"reason":  ev.Reason,
				"message": ev.Message,
				"count":   ev.Count,
			},
			1.0,
		))
	}
	return obs, nil
}

// logSnippets tails container logs (capped). Collected when the scope asks
// for logs OR an analyzer requested them via the adaptive loop
// (ScopePlan.EvidenceRequests with QueryHint "logs").
func (c *Collector) logSnippets(ctx context.Context, scope *collect.ScopePlan, pod *corev1.Pod, res model.ResourceRef, ref model.SourceRef) []model.Observation {
	logsWanted := scope.Logs || scope.WantsHint("logs")
	if !logsWanted {
		return nil
	}
	tail := scope.MaxLogLines
	if tail <= 0 {
		tail = DefaultMaxLogLines
	}
	var obs []model.Observation
	for _, ctr := range pod.Spec.Containers {
		lines, err := c.client.CoreV1().Pods(pod.Namespace).
			GetLogs(pod.Name, &corev1.PodLogOptions{Container: ctr.Name, TailLines: int64Ptr(int64(tail))}).
			Do(ctx).Raw()
		if err != nil {
			continue // logs unavailable → evidence gap, never fatal
		}
		text := strings.TrimSpace(string(lines))
		if text == "" {
			continue
		}
		split := strings.Split(text, "\n")
		if len(split) > tail {
			split = split[len(split)-tail:]
		}
		start := pod.Status.StartTime
		ts := time.Now().Add(-5 * time.Minute)
		if start != nil {
			ts = start.Time
		}
		obs = append(obs, collect.NewObservation(
			"log.snippet",
			model.SourceRef{System: "k8s", Query: fmt.Sprintf("GET /api/v1/namespaces/%s/pods/%s/log?container=%s&tailLines=%d", pod.Namespace, pod.Name, ctr.Name, tail)},
			ts,
			res,
			map[string]any{"container": ctr.Name, "lines": split, "line_count": len(split), "truncated": false},
			1.0,
		))
	}
	return obs
}

func podStateObservation(pod *corev1.Pod, res model.ResourceRef, ref model.SourceRef) model.Observation {
	ts := pod.CreationTimestamp.Time
	if pod.Status.StartTime != nil {
		ts = pod.Status.StartTime.Time
	}
	ownerKind, ownerName := "", ""
	for _, ref := range pod.OwnerReferences {
		ownerKind, ownerName = ref.Kind, ref.Name
		break
	}
	restarts := int32(0)
	for _, s := range pod.Status.ContainerStatuses {
		restarts += s.RestartCount
	}
	return collect.NewObservation(
		"pod.state",
		ref,
		ts,
		res,
		map[string]any{
			"phase":      string(pod.Status.Phase),
			"reason":     pod.Status.Reason,
			"message":    pod.Status.Message,
			"node":       pod.Spec.NodeName,
			"owner_kind": ownerKind,
			"owner_name": ownerName,
			"restarts":   restarts,
		},
		1.0,
	)
}

func containerSpecObservation(pod *corev1.Pod, ctr *corev1.Container, res model.ResourceRef, ref model.SourceRef) model.Observation {
	ts := pod.CreationTimestamp.Time
	if pod.Status.StartTime != nil {
		ts = pod.Status.StartTime.Time
	}
	return collect.NewObservation(
		"container.spec",
		ref,
		ts,
		res,
		map[string]any{
			"container": ctr.Name,
			"image":     ctr.Image,
			"limits":    resourceMap(ctr.Resources.Limits),
			"requests":  resourceMap(ctr.Resources.Requests),
		},
		1.0,
	)
}

func containerStateObservation(pod *corev1.Pod, ctr *corev1.Container, res model.ResourceRef, ref model.SourceRef) model.Observation {
	base := map[string]any{"container": ctr.Name}
	for _, st := range pod.Status.ContainerStatuses {
		if st.Name != ctr.Name {
			continue
		}
		base["restarts"] = st.RestartCount
		if t := st.State.Terminated; t != nil {
			base["reason"] = t.Reason
			base["exit_code"] = t.ExitCode
			base["message"] = t.Message
			ts := t.FinishedAt.Time
			if ts.IsZero() {
				ts = pod.CreationTimestamp.Time
			}
			return collect.NewObservation("container.terminated", ref, ts, res, base, 1.0)
		}
		if w := st.State.Waiting; w != nil {
			base["reason"] = w.Reason
			base["message"] = w.Message
			ts := pod.CreationTimestamp.Time
			if pod.Status.StartTime != nil {
				ts = pod.Status.StartTime.Time
			}
			return collect.NewObservation("container.waiting", ref, ts, res, base, 1.0)
		}
		if r := st.State.Running; r != nil {
			base["started_at"] = r.StartedAt.Time.Format(time.RFC3339)
			ts := r.StartedAt.Time
			if ts.IsZero() {
				ts = pod.CreationTimestamp.Time
			}
			return collect.NewObservation("container.running", ref, ts, res, base, 1.0)
		}
	}
	// No status yet (ContainerCreating): emit waiting with the phase reason.
	base["reason"] = string(pod.Status.Phase)
	ts := pod.CreationTimestamp.Time
	if pod.Status.StartTime != nil {
		ts = pod.Status.StartTime.Time
	}
	return collect.NewObservation("container.waiting", ref, ts, res, base, 1.0)
}

func deploymentStateObservation(dep *appsv1.Deployment, res model.ResourceRef, ref model.SourceRef) model.Observation {
	return collect.NewObservation("deployment.state", ref, dep.CreationTimestamp.Time, res, map[string]any{
		"replicas":           dep.Status.Replicas,
		"updated_replicas":   dep.Status.UpdatedReplicas,
		"available_replicas": dep.Status.AvailableReplicas,
		"conditions":         deploymentConditions(dep),
	}, 1.0)
}

func deploymentConditions(dep *appsv1.Deployment) []map[string]any {
	var out []map[string]any
	for _, c := range dep.Status.Conditions {
		out = append(out, map[string]any{
			"type":    string(c.Type),
			"status":  string(c.Status),
			"reason":  c.Reason,
			"message": c.Message,
		})
	}
	return out
}

func nodeObservations(node *corev1.Node, ref model.SourceRef) []model.Observation {
	res := model.ResourceRef{Kind: "node", Namespace: "", Name: node.Name}
	var obs []model.Observation
	for _, cond := range node.Status.Conditions {
		obs = append(obs, collect.NewObservation(
			"node.condition",
			ref,
			cond.LastTransitionTime.Time,
			res,
			map[string]any{"type": string(cond.Type), "status": string(cond.Status), "reason": cond.Reason, "message": cond.Message},
			1.0,
		))
	}
	obs = append(obs, collect.NewObservation(
		"node.capacity",
		ref,
		node.CreationTimestamp.Time,
		res,
		map[string]any{
			"memory": node.Status.Capacity.Memory().String(),
			"cpu":    node.Status.Capacity.Cpu().String(),
		},
		1.0,
	))
	return obs
}

func ownedBy(refs []metav1.OwnerReference, kind string, uid types.UID) bool {
	for _, r := range refs {
		if r.Kind == kind && r.UID == uid {
			return true
		}
	}
	return false
}

func eventTime(ev *corev1.Event) time.Time {
	if !ev.LastTimestamp.Time.IsZero() {
		return ev.LastTimestamp.Time
	}
	return ev.CreationTimestamp.Time
}

func resourceMap(list corev1.ResourceList) map[string]string {
	out := make(map[string]string, len(list))
	for k, v := range list {
		out[string(k)] = v.String()
	}
	return out
}

func int64Ptr(v int64) *int64 { return &v }
