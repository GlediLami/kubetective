// Package cli implements the kubetective / kubectl-investigate command line.
// The two binaries share this root command; the kubectl plugin name
// (kubectl-investigate) is what makes `kubectl investigate …` work.
package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"

	"github.com/GlediLami/kubetective/internal/analyze"
	"github.com/GlediLami/kubetective/internal/analyze/configregression"
	"github.com/GlediLami/kubetective/internal/analyze/crashloop"
	"github.com/GlediLami/kubetective/internal/analyze/dns"
	"github.com/GlediLami/kubetective/internal/analyze/hpa"
	"github.com/GlediLami/kubetective/internal/analyze/imagepull"
	"github.com/GlediLami/kubetective/internal/analyze/nodepressure"
	"github.com/GlediLami/kubetective/internal/analyze/oom"
	"github.com/GlediLami/kubetective/internal/analyze/probe"
	"github.com/GlediLami/kubetective/internal/analyze/pvc"
	"github.com/GlediLami/kubetective/internal/analyze/scheduling"
	"github.com/GlediLami/kubetective/internal/analyze/service"
	"github.com/GlediLami/kubetective/internal/benchmark"
	"github.com/GlediLami/kubetective/internal/config"
	"github.com/GlediLami/kubetective/internal/score"
	"k8s.io/client-go/dynamic"

	"github.com/GlediLami/kubetective/internal/collect"
	gitcollect "github.com/GlediLami/kubetective/internal/collect/git"
	gitopscollect "github.com/GlediLami/kubetective/internal/collect/gitops"
	k8scollect "github.com/GlediLami/kubetective/internal/collect/kubernetes"
	lokicollect "github.com/GlediLami/kubetective/internal/collect/loki"
	promcollect "github.com/GlediLami/kubetective/internal/collect/prometheus"
	"github.com/GlediLami/kubetective/internal/diag"
	"github.com/GlediLami/kubetective/internal/engine"
	"github.com/GlediLami/kubetective/internal/llm"
	"github.com/GlediLami/kubetective/internal/logging"
	"github.com/GlediLami/kubetective/internal/memory"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/notify"
	"github.com/GlediLami/kubetective/internal/record"
	"github.com/GlediLami/kubetective/pkg/api"
)

// currentSettings holds the values loaded from kubetective.yaml at startup
// (lowest precedence: flags > env > settings > defaults).
var currentSettings config.Settings

// settingsLoadErr is the parse error from kubetective.yaml ("" = clean);
// settingsMissing marks an absent file (a WARN in doctor, not a FAIL).
var (
	settingsLoadErr error
	settingsMissing bool
)

// Execute runs the root command and exits with a non-zero code on failure.
func Execute() {
	// Load persistent settings (kubetective.yaml, v0.9) + adopted calibration
	// temperature (config.json). Settings are the lowest-precedence default:
	// env vars override them, CLI flags override both.
	var serr error
	currentSettings, serr = config.LoadSettings()
	if serr != nil {
		settingsLoadErr = serr
		fmt.Fprintln(os.Stderr, "note: kubetective.yaml:", serr)
	} else if _, statErr := os.Stat(config.SettingsPath()); os.IsNotExist(statErr) {
		settingsMissing = true
	}
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
	case "investigate", "replay", "benchmark", "doctor", "version", "help", "completion", "alert":
		return true
	}
	return false
}

func newRoot() *cobra.Command {
	var logFormat, logLevel string
	root := &cobra.Command{
		Use:   "kubetective",
		Short: "KubeTective - Kubernetes incident investigation engine",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// Structured logs (v1.0 ops): opt-in, env or flag; text default.
			logFormat = firstNonEmptyFlag(cmd, "log-format", os.Getenv("KUBETECTIVE_LOG_FORMAT"))
			logLevel = firstNonEmptyFlag(cmd, "log-level", os.Getenv("KUBETECTIVE_LOG_LEVEL"))
			logging.Configure(logFormat, logLevel)
		},
		Long: `KubeTective investigates Kubernetes incidents: it collects facts, builds a
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
	root.AddCommand(newAlertCmd())
	root.AddCommand(newSanitizeCmd())
	root.PersistentFlags().StringVar(&logFormat, "log-format", "", "structured logging: json | text (off unless set; KUBETECTIVE_LOG_FORMAT)")
	root.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level: debug | info | warn (KUBETECTIVE_LOG_LEVEL)")
	return root
}

// firstNonEmptyFlag favors the CLI flag when the user changed it, else env.
func firstNonEmptyFlag(cmd *cobra.Command, flag, env string) string {
	if cmd.Flags().Changed(flag) {
		v, _ := cmd.Flags().GetString(flag)
		return v
	}
	return env
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
// Prometheus (--prometheus-url / env), Loki (--loki-url / env, log
// evidence), git (--git-repo / env), and GitOps CRDs via a dynamic client
// when available.
//
// Resolution order per source: CLI flag > env var > kubetective.yaml
// (per-context profile, then top-level) > off. The returned clusterID tags
// incident records for memory scoping.
func buildLiveCollectors(kubeconfig, context, prometheusURL, gitRepo, lokiURL string) ([]collect.Collector, kubernetes.Interface, string, error) {
	effective := currentSettings.ForContext(config.FirstNonEmpty(context, os.Getenv("KUBETECTIVE_CONTEXT"), currentSettings.Context))
	kubeconfig = config.FirstNonEmpty(kubeconfig, os.Getenv("KUBECONFIG"), effective.Kubeconfig)
	client, cfg, err := k8scollect.Client(kubeconfig, context)
	if err != nil {
		return nil, nil, "", fmt.Errorf("connect to cluster: %w", err)
	}
	clusterID := config.FirstNonEmpty(os.Getenv("KUBETECTIVE_CLUSTER_ID"), effective.ClusterID)
	if clusterID == "" {
		clusterID = k8scollect.ClusterID(cfg)
	}

	collectors := []collect.Collector{k8scollect.New(client)}
	prometheusURL = config.FirstNonEmpty(prometheusURL, os.Getenv("KUBETECTIVE_PROMETHEUS"), effective.PrometheusURL)
	if prometheusURL != "" {
		collectors = append(collectors, promcollect.New(prometheusURL))
	}
	lokiURL = config.FirstNonEmpty(lokiURL, os.Getenv("KUBETECTIVE_LOKI_URL"), effective.LokiURL)
	if lokiURL != "" {
		collectors = append(collectors, lokicollect.New(lokiURL))
	}
	gitRepo = config.FirstNonEmpty(gitRepo, os.Getenv("KUBETECTIVE_GIT_REPO"), effective.GitRepo)
	if gitRepo != "" {
		collectors = append(collectors, gitcollect.New(gitRepo))
	}
	if dyn, derr := dynamic.NewForConfig(cfg); derr == nil {
		collectors = append(collectors, gitopscollect.New(dyn))
	}
	return collectors, client, clusterID, nil
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
		lokiURL       string
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
			// Settings defaults (lowest precedence): per-context profile,
			// then top-level, for namespace + window.
			st := currentSettings.ForContext(config.FirstNonEmpty(context, os.Getenv("KUBETECTIVE_CONTEXT"), currentSettings.Context))
			if !cmd.Flags().Changed("namespace") && st.Namespace != "" {
				namespace = st.Namespace
			}
			if !cmd.Flags().Changed("since") && st.Since != "" {
				if d, derr := time.ParseDuration(st.Since); derr == nil {
					since = d
				} else {
					return fmt.Errorf("invalid since %q in kubetective.yaml: %v", st.Since, derr)
				}
			}
			target, err := parseTarget(args, namespace)
			if err != nil {
				return err
			}
			opts := investigateOptions{
				noLogs:        noLogs,
				kubeconfig:    kubeconfig,
				context:       context,
				prometheusURL: prometheusURL,
				lokiURL:       lokiURL,
				gitRepo:       gitRepo,
			}
			res, err := runInvestigationPipeline(cmd, target, since, opts)
			if err != nil {
				return err
			}
			if format == "json" {
				return renderJSON(res)
			}
			// Optional AI synthesis: digest-only, validated, never
			// authoritative - the engine's verdicts stand alone.
			var explanation *llm.Explanation
			if llmEnabled {
				model := llmModel
				if model == "" {
					model = config.FirstNonEmpty(os.Getenv("KUBETECTIVE_LLM_MODEL"), st.LLM.Model)
				}
				base := llmBaseURL
				if base == "" {
					base = config.FirstNonEmpty(os.Getenv("KUBETECTIVE_LLM_BASE_URL"), st.LLM.BaseURL)
				}
				if base == "" {
					base = "https://api.openai.com/v1"
				}
				key := llmAPIKey
				if key == "" {
					key = config.FirstNonEmpty(os.Getenv("KUBETECTIVE_LLM_API_KEY"), st.LLM.APIKey)
				}
				if model == "" {
					return fmt.Errorf("--llm requires a model (--llm-model or KUBETECTIVE_LLM_MODEL)")
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
	cmd.Flags().StringVar(&prometheusURL, "prometheus-url", "", "Prometheus base URL (or KUBETECTIVE_PROMETHEUS env) for metric evidence")
	cmd.Flags().StringVar(&lokiURL, "loki-url", "", "Grafana Loki base URL (or KUBETECTIVE_LOKI_URL env) for log evidence")
	cmd.Flags().StringVar(&gitRepo, "git-repo", "", "path to the manifests git checkout (or KUBETECTIVE_GIT_REPO env) for commit evidence")
	cmd.Flags().BoolVar(&llmEnabled, "llm", false, "enable the optional AI synthesis layer (digest-only, never authoritative)")
	cmd.Flags().StringVar(&llmModel, "llm-model", "", "LLM model name (or KUBETECTIVE_LLM_MODEL env)")
	cmd.Flags().StringVar(&llmBaseURL, "llm-base-url", "", "OpenAI-compatible API base URL (or KUBETECTIVE_LLM_BASE_URL env; default https://api.openai.com/v1)")
	cmd.Flags().StringVar(&llmAPIKey, "llm-api-key", "", "API key (or KUBETECTIVE_LLM_API_KEY env; not needed for local servers)")
	return cmd
}

// investigateOptions carries the collector/scope settings shared by every
// entry path that runs a live investigation.
type investigateOptions struct {
	noLogs        bool
	kubeconfig    string
	context       string
	prometheusURL string
	lokiURL       string
	gitRepo       string
}

// runInvestigationPipeline is the one live investigation path, shared by
// `investigate` and `alert`: wire collectors -> engine -> record -> webhook
// -> memory note. The caller renders the result (text/JSON) and may add the
// optional AI layer on top.
func runInvestigationPipeline(cmd *cobra.Command, target model.ResourceRef, since time.Duration, opts investigateOptions) (*api.InvestigationResult, error) {
	st := currentSettings.ForContext(config.FirstNonEmpty(opts.context, os.Getenv("KUBETECTIVE_CONTEXT"), currentSettings.Context))
	collectors, _, clusterID, err := buildLiveCollectors(opts.kubeconfig, opts.context, opts.prometheusURL, opts.gitRepo, opts.lokiURL)
	if err != nil {
		return nil, err
	}
	eng := newEngine(collectors...)
	req := &api.InvestigationRequest{
		Target: target,
		Window: api.Since(since),
		Scope:  api.ScopeOptions{Logs: !opts.noLogs},
	}
	res, err := eng.Investigate(cmd.Context(), req)
	if err != nil {
		return nil, err
	}
	// Record every investigation (replay substrate).
	store := record.NewDefaultStore()
	inc := record.BuildIncident(engine.Version, req, res, clusterID)
	if path, serr := store.Save(inc); serr == nil {
		res.Meta.RecordID = path
	}
	slog.Info("investigation completed",
		"target", target.String(),
		"incident_id", filepath.Base(strings.TrimSuffix(res.Meta.RecordID, ".jsonl")),
		"cluster_id", clusterID,
		"duration_ms", res.Meta.Duration.Milliseconds(),
		"findings", len(res.Findings),
		"record_id", res.Meta.RecordID,
	)
	// Opt-in completion webhook (v1.0 integration surface): fires after the
	// investigation completes; HMAC-signed when a secret is configured.
	// Never fails the investigation.
	if url := config.FirstNonEmpty(os.Getenv("KUBETECTIVE_WEBHOOK_URL"), st.WebhookURL); url != "" {
		n := notify.Notification{
			IncidentID:    filepath.Base(strings.TrimSuffix(res.Meta.RecordID, ".jsonl")),
			Target:        target.String(),
			ClusterID:     clusterID,
			EngineVersion: engine.Version,
			RecordID:      res.Meta.RecordID,
			DurationMs:    res.Meta.Duration.Milliseconds(),
		}
		if res.Incident != nil {
			n.Status = res.Incident.Status
		}
		for _, f := range res.Findings {
			n.Findings = append(n.Findings, notify.Finding{Analyzer: f.Analyzer, Severity: string(f.Severity), Title: f.Title})
		}
		secret := config.FirstNonEmpty(os.Getenv("KUBETECTIVE_WEBHOOK_SECRET"), st.WebhookSecret)
		if werr := notify.Send(cmd.Context(), url, secret, n); werr != nil {
			fmt.Fprintf(os.Stderr, "note: webhook notification failed: %v\n", werr)
		}
	}
	// Incident memory: "seen this before?" (v0.8) — surface similar past
	// incidents before the main output.
	if id := res.Meta.RecordID; id != "" {
		if matches, merr := memory.Similar(store, filepath.Base(id), 3, clusterID); merr == nil && len(matches) > 0 {
			fmt.Fprintf(os.Stderr, "note: similar past incident(s):\n")
			for _, m := range matches {
				fmt.Fprintf(os.Stderr, "  %s (overlap %.0f%%, %s) target=%s cluster=%s\n", m.IncidentID, m.Overlap*100, memory.Describe(m.Overlap), m.Target, clusterID)
			}
		}
	}
	return res, nil
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
Exit code 1 if any scenario fails - this is the analyzer contribution gate.`,
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
	var (
		kubeconfig string
		kubeCtx    string
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the environment: kubeconfig, store, config, evidence sources",
		Long: `Read-only preflight of everything kubetective depends on:
  settings   kubetective.yaml parses
  calibration adopted temperature (config.json)
  cluster    kubeconfig resolves and the API is reachable
  store      incident store readable
  prometheus / loki  optional evidence sources reachable

Exits 1 when anything is broken; warnings never fail the command.
`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			kubeCtx = config.FirstNonEmpty(kubeCtx, os.Getenv("KUBETECTIVE_CONTEXT"), currentSettings.Context)
			effective := currentSettings.ForContext(kubeCtx)
			kubeconfig = config.FirstNonEmpty(kubeconfig, effective.Kubeconfig)
			rep := diag.Run(cmd.Context(), diag.Options{
				Settings:        effective,
				SettingsErr:     settingsLoadErr,
				SettingsMissing: settingsMissing,
				StateDir:        config.DefaultDir(),
				ConfigPath:      config.Path(),
				PrometheusURL:   config.FirstNonEmpty(os.Getenv("KUBETECTIVE_PROMETHEUS"), effective.PrometheusURL),
				LokiURL:         config.FirstNonEmpty(os.Getenv("KUBETECTIVE_LOKI_URL"), effective.LokiURL),
				HubCheck: func(hubCtx context.Context) error {
					client, _, err := k8scollect.Client(kubeconfig, kubeCtx)
					if err != nil {
						return fmt.Errorf("kubeconfig: %w", err)
					}
					if _, err := client.Discovery().ServerVersion(); err != nil {
						return fmt.Errorf("cluster API unreachable: %w", err)
					}
					return nil
				},
			})
			return renderDoctor(&rep)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "kubeconfig path to check (default: KUBECONFIG or ~/.kube/config)")
	cmd.Flags().StringVar(&kubeCtx, "context", "", "kubeconfig context to check")
	return cmd
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

// parseResourceRef accepts "kind/name", fully-qualified "kind/ns/name"
// (the form incident Meta.Target is stored in), or a bare name (pod).
func parseResourceRef(raw, namespace string) (model.ResourceRef, error) {
	ref := model.ResourceRef{Namespace: namespace}
	parts := strings.Split(raw, "/")
	switch len(parts) {
	case 1:
		ref.Kind, ref.Name = "pod", parts[0]
	case 2:
		ref.Kind, ref.Name = parts[0], parts[1]
	case 3:
		ref.Kind, ref.Namespace, ref.Name = parts[0], parts[1], parts[2]
	default:
		return model.ResourceRef{}, fmt.Errorf("invalid target %q - use kind/name or kind/namespace/name", raw)
	}
	if ref.Kind == "" || ref.Name == "" {
		return model.ResourceRef{}, fmt.Errorf("invalid target %q - use kind/name (e.g. deployment/checkout)", raw)
	}
	return ref, nil
}
