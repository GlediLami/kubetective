package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/GlediLami/kubetective/internal/record"
	"github.com/GlediLami/kubetective/internal/redact"
)

// newSanitizeCmd exposes the redaction pass. A recording is safe to keep and
// unsafe to send: this command is the boundary between the two, and it writes
// to a new file rather than in place so the original is never destroyed by a
// command whose whole purpose is to be run before sharing.
func newSanitizeCmd() *cobra.Command {
	var (
		outFile      string
		keepImages   bool
		keepMessages bool
		force        bool
	)
	cmd := &cobra.Command{
		Use:   "sanitize <incident-id|path>",
		Short: "Redact a recorded incident so it can be shared",
		Long: `Produces a shareable copy of an incident record.

Identifiers (namespace, workload, node, container, image) are replaced with
sequential pseudonyms assigned in first-appearance order, so the record still
replays to the same verdict while naming nothing real. Free text - event
messages, log snippets, commit subjects - is scrubbed for emails, IP addresses,
URLs, tokens and keys.

The original file is never modified. The summary reports what changed; read it
before sharing, because free-text scrubbing is pattern-based and cannot be
complete by construction.

  kubetective sanitize incident-1754575200-checkout
  kubetective sanitize ./record.jsonl --out ./shareable.jsonl`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src := args[0]
			store := record.NewDefaultStore()
			if strings.ContainsAny(src, "/\\") {
				store = record.NewStore(filepath.Dir(src))
				src = strings.TrimSuffix(filepath.Base(src), ".jsonl")
			}
			inc, err := store.Load(src)
			if err != nil {
				return fmt.Errorf("load %s: %w", args[0], err)
			}

			clean, report := redact.New(redact.Options{
				KeepImages:   keepImages,
				KeepMessages: keepMessages,
			}).Incident(inc)

			dest := outFile
			if dest == "" {
				dest = filepath.Join(filepath.Dir(args[0]), inc.ID+".sanitized.jsonl")
				if !strings.ContainsAny(args[0], "/\\") {
					dest = inc.ID + ".sanitized.jsonl"
				}
			}
			if _, err := os.Stat(dest); err == nil && !force {
				return fmt.Errorf("%s already exists (pass --force to overwrite)", dest)
			}

			// Save() writes into a store directory keyed by incident ID, so
			// write through a store rooted at the destination directory and
			// then move the result to the requested filename.
			clean.ID = strings.TrimSuffix(filepath.Base(dest), ".jsonl")
			written, err := record.NewStore(filepath.Dir(dest)).Save(clean)
			if err != nil {
				return fmt.Errorf("write %s: %w", dest, err)
			}
			if written != dest {
				if err := os.Rename(written, dest); err != nil {
					return fmt.Errorf("write %s: %w", dest, err)
				}
			}

			fmt.Fprint(cmd.OutOrStdout(), report.String())
			fmt.Fprintf(cmd.OutOrStdout(), "\nwrote %s\n", dest)
			fmt.Fprintf(cmd.OutOrStdout(),
				"review it before sharing: free-text scrubbing is pattern-based, not exhaustive.\n")
			if keepMessages {
				fmt.Fprintf(cmd.OutOrStdout(),
					"warning: --keep-messages left every message intact; secrets in log or event text were NOT removed.\n")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outFile, "out", "", "destination file (default: <incident>.sanitized.jsonl)")
	cmd.Flags().BoolVar(&keepImages, "keep-images", false, "keep container image references (registry hostnames identify the org)")
	cmd.Flags().BoolVar(&keepMessages, "keep-messages", false, "keep event and log text verbatim (disables secret scrubbing)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite the destination if it exists")
	return cmd
}
