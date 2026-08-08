package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/GlediLami/kubetective/internal/alert"
	"github.com/GlediLami/kubetective/internal/config"
)

// parseAlertPayload loads and parses the payload into an investigation seed,
// turning provider quirks into actionable errors.
func parseAlertPayload(typeStr, file string) (*alert.Payload, error) {
	kind := alertKindFromString(typeStr)
	if kind != alert.PagerDuty && kind != alert.Grafana && kind != alert.Slack {
		return nil, fmt.Errorf("alert requires a provider type: pagerduty, grafana, or slack (got %q)", typeStr)
	}
	body, err := readAlertPayload(file)
	if err != nil {
		return nil, err
	}
	p, err := alert.Parse(kind, body)
	if err != nil {
		if errors.Is(err, alert.ErrNoTarget) {
			return nil, fmt.Errorf("%s alert: no Kubernetes target in the payload - add a kubernetes_* alert label (Grafana), a resource name in the incident title (PagerDuty), or kind/name text (Slack)", typeStr)
		}
		return nil, err
	}
	return p, nil
}

// newAlertCmd is the inbound integration surface (roadmap v1.0): it turns a
// PagerDuty / Grafana / Slack webhook payload into an investigation. Zero
// API keys - the payload is parsed locally, the engine uses its existing
// cluster access, and the opt-in completion webhook reports back out.
func newAlertCmd() *cobra.Command {
var (
	alertType     string
	file          string
	since         time.Duration
	format        string
	noLogs        bool
	namespace     string
	kubeconfig    string
	context       string
	prometheusURL string
	lokiURL       string
	gitRepo       string
)
	cmd := &cobra.Command{
		Use:   "alert [pagerduty|grafana|slack]",
		Short: "Investigate from a webhook alert payload (PagerDuty, Grafana, Slack)",
		Long: `Parses a PagerDuty / Grafana / Slack webhook payload (stdin or --file)
and investigates the Kubernetes target it names - with zero API keys: the
payload is parsed locally, the engine uses its existing cluster access, and
the opt-in completion webhook reports the result back out.

  echo '{...pagerduty...}' | kubetective alert pagerduty
  kubetective alert grafana --file=alert.json --format=json
  echo '{"text":"deployment/checkout since=2h"}' | kubetective alert slack

Supported payload fields (see docs/alerting or the README): Grafana
kubernetes_* alert labels, PagerDuty incident titles carrying a resource,
and Slack slash-command text ("deployment/checkout since=2h"). Alerts
without a Kubernetes target fail with a readable error instead of guessing.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				alertType = args[0]
			}
			p, err := parseAlertPayload(alertType, file)
			if err != nil {
				return err
			}
			// Window precedence: --since flag > payload-derived (Slack
			// command text) > settings > engine default.
			window := since
			if !cmd.Flags().Changed("since") {
				window = p.Window
			}
			if window == 0 {
				if st := currentSettings.ForContext(config.FirstNonEmpty(context, os.Getenv("KUBETECTIVE_CONTEXT"), currentSettings.Context)); st.Since != "" {
					if d, derr := time.ParseDuration(st.Since); derr == nil {
						window = d
					}
				}
			}
			if window == 0 {
				window = 30 * time.Minute
			}
			target := p.Target
			if target.Namespace == "" && namespace != "" {
				target.Namespace = namespace
			}
			opts := investigateOptions{
				noLogs:        noLogs,
				kubeconfig:    kubeconfig,
				context:       context,
				prometheusURL: prometheusURL,
				lokiURL:       lokiURL,
				gitRepo:       gitRepo,
			}
			res, err := runInvestigationPipeline(cmd, target, window, opts)
			if err != nil {
				return err
			}
			if format == "json" {
				return renderJSON(res)
			}
			return renderText(res, nil)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "payload file (default: read stdin; use - for stdin)")
	cmd.Flags().DurationVar(&since, "since", 0, "investigation window (overrides the payload window)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text | json")
	cmd.Flags().BoolVar(&noLogs, "no-logs", false, "skip container log collection")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "kubernetes namespace (scopes a target without one)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (default: KUBECONFIG or ~/.kube/config)")
	cmd.Flags().StringVar(&context, "context", "", "kubeconfig context to use")
	cmd.Flags().StringVar(&prometheusURL, "prometheus-url", "", "Prometheus base URL (or KUBETECTIVE_PROMETHEUS env)")
	cmd.Flags().StringVar(&lokiURL, "loki-url", "", "Grafana Loki base URL (or KUBETECTIVE_LOKI_URL env)")
	cmd.Flags().StringVar(&gitRepo, "git-repo", "", "path to the manifests git checkout (or KUBETECTIVE_GIT_REPO env)")
	return cmd
}

// readPayload loads the webhook body from --file or stdin.
func readAlertPayload(file string) ([]byte, error) {
	if file == "-" {
		file = ""
	}
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read alert payload: %w", err)
		}
		return b, nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil, fmt.Errorf("read alert payload from stdin: %w", err)
	}
	return b, nil
}

func alertKindFromString(s string) alert.Kind {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "pagerduty", "pd":
		return alert.PagerDuty
	case "grafana":
		return alert.Grafana
	case "slack":
		return alert.Slack
	}
	return alert.Kind(s)
}