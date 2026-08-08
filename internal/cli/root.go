// Package cli implements the kubedoctor / kubectl-investigate command line.
// The two binaries share this root command; the kubectl plugin name
// (kubectl-investigate) is what makes `kubectl investigate …` work.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"

	"github.com/kubedoctor/kubedoctor/internal/analyze"
	"github.com/kubedoctor/kubedoctor/internal/analyze/configregression"
	"github.com/kubedoctor/kubedoctor/internal/analyze/crashloop"
	"github.com/kubedoctor/kubedoctor/internal/analyze/dns"
	"github.com/kubedoctor/kubedoctor/internal/analyze/hpa"
	"github.com/kubedoctor/kubedoctor/internal/analyze/imagepull"
	"github.com/kubedoctor/kubedoctor/internal/analyze/nodepressure"
	"github.com/kubedoctor/kubedoctor/internal/analyze/oom"
	"github.com/kubedoctor/kubedoctor/internal/analyze/probe"
	"github.com/kubedoctor/kubedoctor/internal/analyze/pvc"
	"github.com/kubedoctor/kubedoctor/internal/analyze/scheduling"
	"github.com/kubedoctor/kubedoctor/internal/analyze/service"
	"github.com/kubedoctor/kubedoctor/internal/benchmark"
	"github.com/kubedoctor/kubedoctor/internal/config"
	"github.com/kubedoctor/kubedoctor/internal/score"
	"k8s.io/client-go/dynamic"

	"github.com/kubedoctor/kubedoctor/internal/collect"
	gitcollect "github.com/kubedoctor/kubedoctor/internal/collect/git"
	gitopscollect "github.com/kubedoctor/kubedoctor/internal/collect/gitops"
	k8scollect "github.com/kubedoctor/kubedoctor/internal/collect/kubernetes"
	promcollect "github.com/kubedoctor/kubedoctor/internal/collect/prometheus"
	"github.com/kubedoctor/kubedoctor/internal/engine"
	"github.com/kubedoctor/kubedoctor/internal/llm"
	"github.com/kubedoctor/kubedoctor/internal/model"
	"github.com/kubedoctor/kubedoctor/internal/record"
	"github.com/kubedoctor/kubedoctor/pkg/api"
)

// Execute runs the root command and exits with a non-zero code on failure.
func Execute() {
	if cfg, err := config.Load(); err == nil && cfg.Temperature > 0 {
		score.SetTemperature(cfg.Temperature)
	}
	root := newRoot()
	// kubectl plugin invocation: `kubectl investigate <target>` runs
	// `kubectl-investigate <target>`, so the first positional argument is the
	// investigation target, not a subcommand.
	if isPluginInvocation() {
		args := os.Args[1:]
		if len(args) > 0 && !isKnownCommand(args[0]) {
			args = append([]string{"investigate"}, args...)
			root.SetArgs(args)
		}
	}
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func isPluginInvocation() bool {
	base := filepath.Base(os.Args[0])
	return base == "kubectl-investigate" || strings.HasPrefix(base, "kubectl-investigate")
}

func isKnownCommand(arg string) bool {
	switch arg {
	case "investigate", "replay", "benchmark", "doctor", "version", "help", "completion":
		return true
	}
	return false
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "kubedoctor",
		Short: "KubeDoctor — Kubernetes incident investigation engine",
		Long: `KubeDoctor investigates Kubernetes incidents: it collects facts, builds a
timeline and evidence graph, generates hypotheses, and renders an
evidence-backed explanation. Deterministic first, AI second.

Install as a kubectl plugin (binary named kubectl-investigate) and run:

  kubectl investigate pod/checkout-7f84c9
  kubectl investigate deployment/checkout --since=2h`,
		SilenceUsage:  true,
		SilenceErrors: true, // Execute() prints the error exactly once
	}
	root.AddCommand(newInvestigateCmd())
	root.AddCommand(newReplayCmd())
	root.AddCommand(newBenchmarkCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newEvaluateCmd())
	root.AddCommand(newServeCmd())
	root.AddCommand(newMCPCmd())
	root.AddCommand(newIncidentsCmd())
	root.AddCommand(newActionCmd())
	return root
}

// newEngine wires the v0.2 analyzer set with the given collectors.
func newEngine(collectors ...collect.Collector) *engine.Engine {
	reg := collect.NewRegistry()
	for _, c := range collectors {
		reg.Register(c)
	}
	ar := analyze.NewRegistry()
	ar.Register(oom.New())
	ar.Register(crashloop.New())
	ar.Register(imagepull.New())
	ar.Register(scheduling.New())
	ar.Register(nodepressure.New())
	ar.Register(probe.New())
	ar.Register(dns.New())
	ar.Register(pvc.New())
	ar.Register(service.New())
	ar.Register(hpa.New())
	ar.Register(configregression.New())
	return engine.New(reg, ar)
}

// buildLiveCollectors wires the optional telemetry + gitops collectors on
// top of the Kubernetes collector: k8s first (staged collection), then
// Prometheus (--prometheus-url / env), git (--git-repo / env), and GitOps
// CRDs via a dynamic client when available.
func buildLiveCollectors(kubeconfig, context, prometheusURL, gitRepo string) ([]collect.Collector, kubernetes.Interface, error) {
	client, cfg, err := k8scollect.Client(kubeconfig, context)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to cluster: %w", err)
	}
	collectors := []collect.Collector{k8scollect.New(client)}
	if prometheusURL == "" {
		prometheusURL = os.Getenv("KUBEDOCTOR_PROMETHEUS")
	}
	if prometheusURL != "" {
		collectors = append(collectors, promcollect.New(prometheusURL))
	}
	if gitRepo == "" {
		gitRepo = os.Getenv("KUBEDOCTOR_GIT_REPO")
	}
	if gitRepo != "" {
		collectors = append(collectors, gitcollect.New(gitRepo))
	}
	if dyn, derr := dynamic.NewForConfig(cfg); derr == nil {
		collectors = append(collectors, gitopscollect.New(dyn))
	}
	return collectors, client, nil
}

func newInvestigateCmd() *cobra.Command {
	var (
		namespace     string
		since         time.Duration
		noLogs        bool
		format        string
		kubeconfig    string
		context       string
		prometheusURL string
		gitRepo       string
		llmEnabled    bool
		llmModel      string
		llmBaseURL    string
		llmAPIKey     string
	)
	cmd := &cobra.Command{
		Use:   "investigate <resource>",
		Short: "Investigate a resource or incident",
		Example: `  kubectl investigate pod/checkout-7f84c9
  kubectl investigate deployment/checkout --since=2h
  kubectl investigate --namespace production --since=30m --prometheus-url=http://localhost:9090`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := parseTarget(args, namespace)
			if err != nil {
				return err
			}
			collectors, _, err := buildLiveCollectors(kubeconfig, context, prometheusURL, gitRepo)
			if err != nil {
				return err
			}
			eng := newEngine(collectors...)
			req := &api.InvestigationRequest{
				Target: target,
				Window: api.Since(since),
				Scope:  api.ScopeOptions{Logs: !noLogs},
			}
			res, err := eng.Investigate(cmd.Context(), req)
			if err != nil {
				return err
			}
			// Record every investigation (replay substrate, docs/DESIGN.md §7.6).
			store := record.NewDefaultStore()
			inc := record.BuildIncident(engine.Version, req, res)
			if path, serr := store.Save(inc); serr == nil {
				res.Meta.RecordID = path
			}
			if format == "json" {
				return renderJSON(res)
			}
			// Optional AI synthesis: digest-only, validated, never
			// authoritative — the engine's verdicts stand alone.
			var explanation *llm.Explanation
			if llmEnabled {
				model := llmModel
				if model == "" {
					model = os.Getenv("KUBEDOCTOR_LLM_MODEL")
				}
				base := llmBaseURL
				if base == "" {
					base = os.Getenv("KUBEDOCTOR_LLM_BASE_URL")
				}
				if base == "" {
					base = "https://api.openai.com/v1"
				}
				key := llmAPIKey
				if key == "" {
					key = os.Getenv("KUBEDOCTOR_LLM_API_KEY")
				}
				if model == "" {
					return fmt.Errorf("--llm requires a model (--llm-model or KUBEDOCTOR_LLM_MODEL)")
				}
				digest := llm.BuildDigest(res, 3, 12, 5)
				exp, xerr := llm.NewExplainer(llm.NewOpenAICompatible(base, model, key)).Explain(cmd.Context(), digest)
				if xerr != nil {
					// Graceful degradation: the deterministic verdict stands
					// alone; the AI layer is optional.
					fmt.Fprintln(os.Stderr, "note: AI synthesis unavailable:", xerr)
				} else {
					explanation = exp
				}
			}
			return renderText(res, explanation)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "kubernetes namespace")
	cmd.Flags().DurationVar(&since, "since", 30*time.Minute, "investigation window: how far back to look")
	cmd.Flags().BoolVar(&noLogs, "no-logs", false, "skip container log collection")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text | json")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (default: KUBECONFIG or ~/.kube/config)")
	cmd.Flags().StringVar(&context, "context", "", "kubeconfig context to use")
	cmd.Flags().StringVar(&prometheusURL, "prometheus-url", "", "Prometheus base URL (or KUBEDOCTOR_PROMETHEUS env) for metric evidence")
	cmd.Flags().StringVar(&gitRepo, "git-repo", "", "path to the manifests git checkout (or KUBEDOCTOR_GIT_REPO env) for commit evidence")
	cmd.Flags().BoolVar(&llmEnabled, "llm", false, "enable the optional AI synthesis layer (digest-only, never authoritative)")
	cmd.Flags().StringVar(&llmModel, "llm-model", "", "LLM model name (or KUBEDOCTOR_LLM_MODEL env)")
	cmd.Flags().StringVar(&llmBaseURL, "llm-base-url", "", "OpenAI-compatible API base URL (or KUBEDOCTOR_LLM_BASE_URL env; default https://api.openai.com/v1)")
	cmd.Flags().StringVar(&llmAPIKey, "llm-api-key", "", "API key (or KUBEDOCTOR_LLM_API_KEY env; not needed for local servers)")
	return cmd
}

func newReplayCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "replay <incident-id>",
		Short: "Replay a recorded investigation",
		Long: `Replays a recorded investigation through the current engine version:
loads the JSONL incident record, serves the recorded observations back to the
pipeline, and re-runs analysis. Deterministic: same record + same engine →
same result.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store := record.NewDefaultStore()
			inc, err := store.Load(args[0])
			if err != nil {
				return fmt.Errorf("load incident %q: %w", args[0], err)
			}
			target, err := parseResourceRef(inc.Meta.Target, "")
			if err != nil {
				target = model.ResourceRef{Kind: "pod", Name: "scenario"}
			}
			eng := newEngine(record.NewReplayCollector(inc.Observations))
			res, err := eng.Investigate(cmd.Context(), &api.InvestigationRequest{Target: target})
			if err != nil {
				return err
			}
			res.Meta.RecordID = args[0]
			return renderText(res, nil)
		},
	}
}

func newBenchmarkCmd() *cobra.Command {
	var scenariosDir string
	cmd := &cobra.Command{
		Use:   "benchmark [suite]",
		Short: "Run the scenario benchmark gate",
		Long: `Replays every scenario in scenarios/ through the engine and asserts the
ground truth (top hypothesis category, minimum score, expected findings).
Exit code 1 if any scenario fails — this is the analyzer contribution gate.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				scenariosDir = args[0]
			}
			suite, err := benchmark.RunSuite(cmd.Context(), scenariosDir, func(cs ...collect.Collector) api.Investigator {
				return newEngine(cs...)
			})
			if err != nil {
				return err
			}
			return renderBenchmark(suite)
		},
	}
	cmd.Flags().StringVar(&scenariosDir, "scenarios", "scenarios", "directory containing benchmark scenarios")
	return cmd
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Quick cluster health scan (static analyzers only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("doctor lands in a later v0.1 milestone")
		},
	}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), engine.Version)
			return nil
		},
	}
}

// parseTarget turns "deployment/checkout", "pod/checkout-7f84c9", or a bare
// name (defaults to pod) into a ResourceRef.
func parseTarget(args []string, namespace string) (model.ResourceRef, error) {
	if len(args) == 0 {
		return model.ResourceRef{Kind: "namespace", Namespace: namespace, Name: namespace}, nil
	}
	return parseResourceRef(args[0], namespace)
}

func parseResourceRef(raw, namespace string) (model.ResourceRef, error) {
	ref := model.ResourceRef{Namespace: namespace}
	if strings.Contains(raw, "/") {
		parts := strings.SplitN(raw, "/", 2)
		ref.Kind, ref.Name = parts[0], parts[1]
	} else {
		ref.Kind, ref.Name = "pod", raw
	}
	if ref.Kind == "" || ref.Name == "" {
		return model.ResourceRef{}, fmt.Errorf("invalid target %q — use kind/name (e.g. deployment/checkout)", raw)
	}
	return ref, nil
}
