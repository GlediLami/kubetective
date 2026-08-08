package cli

import (
	"bufio"
	contextPkg "context"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"

	"github.com/GlediLami/kubetective/internal/action"
	k8scollect "github.com/GlediLami/kubetective/internal/collect/kubernetes"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/record"
	"github.com/GlediLami/kubetective/internal/server"
	"github.com/GlediLami/kubetective/pkg/api"
)

// newServeCmd runs the REST API (v0.6: server mode).
func newServeCmd() *cobra.Command {
	var (
		listen     string
		kubeconfig string
		kubeCtx    string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the REST API server",
		Long: `Runs the REST API:

  POST /v1/investigate   run an investigation
  GET  /v1/incidents     list recorded incidents
  GET  /v1/incidents/{id} read a full incident record
  GET  /healthz          liveness`,
		RunE: func(cmd *cobra.Command, args []string) error {
			collectors, _, err := buildLiveCollectors(kubeconfig, kubeCtx, "", "")
			if err != nil {
				return err
			}
			rest := &server.REST{Inv: newEngine(collectors...), Store: record.NewDefaultStore()}
			fmt.Printf("kubetective serve listening on %s\n", listen)
			return http.ListenAndServe(listen, rest.Handler())
		},
	}
	cmd.Flags().StringVar(&listen, "listen", ":8080", "listen address")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "kubeconfig path")
	cmd.Flags().StringVar(&kubeCtx, "context", "", "kubeconfig context")
	return cmd
}

// newMCPCmd runs the Model Context Protocol server over stdio (thin
// wrapper over the same pipeline).
func newMCPCmd() *cobra.Command {
	var (
		kubeconfig string
		kubeCtx    string
	)
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP server over stdio (Model Context Protocol)",
		Long: `Runs a minimal MCP server (JSON-RPC 2.0 over stdio) exposing
read-only tools: investigate, replay, list_incidents, read_incident,
action_preview. Remediation stays human-gated in the CLI — there is
deliberately no apply tool.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			collectors, _, err := buildLiveCollectors(kubeconfig, kubeCtx, "", "")
			if err != nil {
				return err
			}
			live := newEngine(collectors...)
			store := record.NewDefaultStore()
			mcp := &server.MCPServer{
				Inv:   live,
				Store: store,
				Replay: func(ctx contextPkg.Context, incidentID string, target model.ResourceRef) (*api.InvestigationResult, error) {
					inc, err := store.Load(incidentID)
					if err != nil {
						return nil, err
					}
					return newEngine(record.NewReplayCollector(inc.Observations)).Investigate(ctx, &api.InvestigationRequest{Target: target})
				},
			}
			sc := bufio.NewScanner(os.Stdin)
			sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
			w := bufio.NewWriter(os.Stdout)
			for sc.Scan() {
				out, err := mcp.HandleMessage(sc.Bytes())
				if err != nil {
					return err
				}
				if out != nil {
					w.Write(append(out, '\n'))
					w.Flush()
				}
			}
			return sc.Err()
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "kubeconfig path")
	cmd.Flags().StringVar(&kubeCtx, "context", "", "kubeconfig context")
	return cmd
}

// newIncidentsCmd lists recorded incident ids.
func newIncidentsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "incidents",
		Short: "List recorded incidents",
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := record.NewDefaultStore().List()
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				fmt.Println("no incidents recorded yet")
				return nil
			}
			for _, id := range ids {
				fmt.Println(id)
			}
			return nil
		},
	}
}

// newActionCmd is the Phase 3/4 remediation surface:
// preview is the default and touches nothing; --apply executes ONE action
// after explicit --yes approval, and appends an audit record.
func newActionCmd() *cobra.Command {
	var (
		applyID    string
		yes        bool
		kubeconfig string
		kubeCtx    string
		userName   string
	)
	cmd := &cobra.Command{
		Use:   "action <incident-id>",
		Short: "Preview or apply remediation actions for an incident",
		Long: `Deterministic, evidence-linked actions for a recorded incident
(phases 3-4 of the remediation model).

Default: PREVIEW — read-only, contacts no cluster, prints the diff-style
plan (kubectl equivalents, risk, evidence).

Apply:  --apply <action-id> --yes
        executes the single action on the cluster and appends an audit
        record (user, timestamp, resource, arguments, evidence, risk,
        approval, result) to the incident file.`,
		Example: `  kubetective action incident-1786145202-checkout
  kubetective action incident-1786145202-checkout --apply act-1a2b3c4d --yes`,
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
			res, err := newEngine(record.NewReplayCollector(inc.Observations)).Investigate(cmd.Context(), &api.InvestigationRequest{Target: target})
			if err != nil {
				return err
			}
			acts := action.Plan(res)

			if applyID == "" {
				return renderActionPreview(acts)
			}

			// Apply path: approval required.
			if !yes {
				return fmt.Errorf("approval required: re-run with --yes to apply %q (preview above shows what it does)", applyID)
			}
			var chosen *action.Action
			for i := range acts {
				if acts[i].ID == applyID {
					chosen = &acts[i]
					break
				}
			}
			if chosen == nil {
				return fmt.Errorf("unknown action %q — preview lists the available action ids", applyID)
			}

			kc, err := buildLiveClient(kubeconfig, kubeCtx)
			if err != nil {
				return err
			}
			if userName == "" {
				if u, uerr := user.Current(); uerr == nil {
					userName = u.Username
				} else {
					userName = "unknown"
				}
			}
			result, aerr := action.NewApplier(kc).Apply(cmd.Context(), *chosen)
			audit := action.NewAudit(userName, inc.Meta.ClusterID, inc.ID, *chosen, "explicit", result, errStr(aerr))
			if serr := (action.FileAuditSink{Dir: record.DefaultDir()}).AppendAudit(inc.ID, audit); serr != nil {
				fmt.Fprintf(os.Stderr, "note: audit record not written: %v\n", serr)
			}
			if aerr != nil {
				return fmt.Errorf("apply %s: %w", chosen.ID, aerr)
			}
			fmt.Printf("✓ %s: %s\n", chosen.ID, result)
			fmt.Printf("audit appended to incident %s\n", inc.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&applyID, "apply", "", "apply this action id (requires --yes)")
	cmd.Flags().BoolVar(&yes, "yes", false, "explicit human approval for --apply")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "kubeconfig path")
	cmd.Flags().StringVar(&kubeCtx, "context", "", "kubeconfig context")
	cmd.Flags().StringVar(&userName, "user", "", "operator identity for the audit record (default: OS user)")
	return cmd
}

// buildLiveClient returns a live cluster client for the applier.
func buildLiveClient(kubeconfig, context string) (kubernetes.Interface, error) {
	kc, _, err := k8scollect.Client(kubeconfig, context)
	return kc, err
}

func renderActionPreview(acts []action.Action) error {
	if len(acts) == 0 {
		fmt.Println("no remediation actions — the investigation found nothing actionable")
		return nil
	}
	fmt.Println("ACTION PREVIEW (read-only — nothing has been applied)")
	for _, a := range acts {
		fmt.Printf("  %s  %s\n", a.ID, a.Type)
		fmt.Printf("    target:  %s\n", a.Target)
		fmt.Printf("    dry run: %s\n", a.DryRun)
		fmt.Printf("    risk:    %s\n", a.Risk)
		fmt.Printf("    reason:  %s\n", a.Reason)
		if len(a.EvidenceIDs) > 0 {
			fmt.Printf("    evidence:%s\n", " "+strings.Join(a.EvidenceIDs, ", "))
		}
		fmt.Println()
	}
	fmt.Println("apply with: kubetective action <incident-id> --apply <id> --yes")
	return nil
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
