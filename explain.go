package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/guettli/kubenorm/normalize"
	"github.com/spf13/cobra"
)

const explainValueLen = 60

func buildExplainCmd() *cobra.Command {
	var flags normFlags

	cmd := &cobra.Command{
		Use:   "explain <file|dir|->...",
		Short: "Show which rule changes or drops what",
		Long: `Normalize like "norm" and print, for every resource, each change and the
rule or option that made it. Useful to write and debug your own config.

  - removed field     + added field     ~ changed value

Example:
  kubenorm explain deployment.yaml --config my-rules.yaml`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := flags.options(cmd.ErrOrStderr())
			if err != nil {
				return err
			}

			resources, err := readInputs(cmd, args...)
			if err != nil {
				return err
			}

			explanations, err := opts.Explain(resources)
			if err != nil {
				return err
			}

			var b strings.Builder

			for _, e := range explanations {
				fmt.Fprintf(&b, "%s (from %s)\n", e.Path, e.Source)

				switch {
				case e.Dropped != "":
					fmt.Fprintf(&b, "  dropped: %s\n", e.Dropped)
				case len(e.Changes) == 0:
					b.WriteString("  unchanged\n")
				}

				for _, c := range e.Changes {
					b.WriteString("  " + formatChange(c) + "\n")
				}
			}

			_, err = fmt.Fprint(cmd.OutOrStdout(), b.String())
			if err != nil {
				return fmt.Errorf("writing output: %w", err)
			}

			return nil
		},
	}

	flags.register(cmd)

	return cmd
}

func formatChange(c normalize.Change) string {
	switch {
	case c.New == nil:
		return fmt.Sprintf("- %s: %s  [%s]", c.Path, formatValue(c.Old, explainValueLen), c.Reason)
	case c.Old == nil:
		return fmt.Sprintf("+ %s: %s  [%s]", c.Path, formatValue(c.New, explainValueLen), c.Reason)
	default:
		return fmt.Sprintf("~ %s: %s -> %s  [%s]", c.Path, formatValue(c.Old, explainValueLen), formatValue(c.New, explainValueLen), c.Reason)
	}
}

// formatValue renders a value compactly on one line, shortened to maxLen
// runes.
func formatValue(v any, maxLen int) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}

	if r := []rune(string(data)); len(r) > maxLen {
		return string(r[:maxLen]) + "..."
	}

	return string(data)
}
