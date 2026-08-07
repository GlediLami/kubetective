// Package cli implements the kubedoctor / kubectl-investigate command line.
// The two binaries share this root command; the kubectl plugin name
// (kubectl-investigate) is what makes `kubectl investigate …` work.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kubedoctor/kubedoctor/internal/analyze"
	"github.com/kubedoctor/kubedoctor/internal/collect"
	"github.com/kubedoctor/kubedoctor/internal/engine"
	"github.com/kubedoctor/kubedoctor/internal/model"
	"github.com/kubedoctor/kubedoctor/pkg/api"
)

// Execute runs the root command and exits with a non-zero code on failure.
func Execute() {
	if err := newRoot().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
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
	return root
}

// buildEngine wires the registry skeletons. The v0.1 milestone registers the
// Kubernetes collector and the OOM/CrashLoop/ImagePull analyzers here.
func buildEngine() (*engine.Engine, error) {
	return engine.New(collect.NewRegistry(), analyze.NewRegistry()), nil
}

func newInvestigateCmd() *cobra.Command {
	var (
		namespace string
		since     time.Duration
		noLogs    bool
		format    string
	)
	cmd := &cobra.Command{
		Use:   "investigate <resource>",
		Short: "Investigate a resource or incident",
		Example: `  kubectl investigate pod/checkout-7f84c9
  kubectl investigate deployment/checkout --since=2h
  kubectl investigate --namespace production --since=30m`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := parseTarget(args, namespace)
			if err != nil {
				return err
			}
			eng, err := buildEngine()
			if err != nil {
				return err
			}
			req := &api.InvestigationRequest{
				Target: target,
				Window: api.Since(since),
				Scope:  api.ScopeOptions{Logs: !noLogs},
			}
			res, err := eng.Investigate(cmd.Context(), req)
			if err != nil {
				if errors.Is(err, engine.ErrNoCollectors) {
					return fmt.Errorf("%w — the Kubernetes collector lands in the v0.1 milestone (see docs/DESIGN.md)", err)
				}
				return err
			}
			switch format {
			case "json":
				return renderJSON(res)
			default:
				return renderText(res)
			}
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "kubernetes namespace")
	cmd.Flags().DurationVar(&since, "since", 30*time.Minute, "investigation window: how far back to look")
	cmd.Flags().BoolVar(&noLogs, "no-logs", false, "skip container log collection")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text | json")
	return cmd
}

func newReplayCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "replay <incident-id>",
		Short: "Replay a recorded investigation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("replay lands in the v0.1 milestone (incident records are JSONL)")
		},
	}
}

func newBenchmarkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "benchmark [suite]",
		Short: "Run the scenario benchmark gate",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("benchmark lands in the v0.1 milestone (scenarios/)")
		},
	}
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Quick cluster health scan (static analyzers only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("doctor lands in the v0.1 milestone")
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
		return model.ResourceRef{}, nil // namespace-wide investigation
	}
	raw := args[0]
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

var _ = context.Background // placeholder until collectors are wired
