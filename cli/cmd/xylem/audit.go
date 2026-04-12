// audit.go — "xylem audit" command and subcommands.
// Reads the JSONL intermediary audit log line-by-line without loading the
// whole file. Output is plain lines suitable for piping to jq, rg, or shell
// filters. Not related to builtin_audit.go (which handles scheduled vessel
// workflows).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
)

func newAuditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Inspect the intermediary audit log",
	}
	cmd.AddCommand(
		newAuditTailCmd(),
		newAuditDeniedCmd(),
		newAuditCountsCmd(),
		newAuditRuleCmd(),
	)
	return cmd
}

func newAuditTailCmd() *cobra.Command {
	var n int
	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Print the last N entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.RuntimePath(deps.cfg.StateDir, deps.cfg.EffectiveAuditLogPath())
			return cmdAuditTail(os.Stdout, path, n)
		},
	}
	cmd.Flags().IntVarP(&n, "lines", "n", 20, "Number of entries to print")
	return cmd
}

func newAuditDeniedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "denied",
		Short: "List entries where decision=deny",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.RuntimePath(deps.cfg.StateDir, deps.cfg.EffectiveAuditLogPath())
			return cmdAuditDenied(os.Stdout, path)
		},
	}
}

func newAuditCountsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "counts",
		Short: "Print per-action operation counts",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.RuntimePath(deps.cfg.StateDir, deps.cfg.EffectiveAuditLogPath())
			return cmdAuditCounts(os.Stdout, path)
		},
	}
}

func newAuditRuleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rule <rule-name>",
		Short: "Show violation rate for a policy rule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.RuntimePath(deps.cfg.StateDir, deps.cfg.EffectiveAuditLogPath())
			return cmdAuditRule(os.Stdout, path, args[0])
		},
	}
}

// cmdAuditTail prints the last n entries from the audit log using a ring buffer.
func cmdAuditTail(w io.Writer, path string, n int) error {
	if n <= 0 {
		return nil
	}
	ring := make([]string, n)
	idx := 0
	total := 0
	err := scanAuditLines(path, func(rawLine string) {
		ring[idx%n] = rawLine
		idx++
		total++
	})
	if err != nil {
		return err
	}
	if total == 0 {
		return nil
	}
	// Determine start position in ring buffer.
	count := total
	if count > n {
		count = n
	}
	// When we've seen more entries than the ring size, idx%n points to the
	// oldest kept entry. When total <= n all used slots start at index 0.
	start := 0
	if total > n {
		start = idx % n
	}
	for i := 0; i < count; i++ {
		pos := (start + i) % n
		fmt.Fprintln(w, ring[pos])
	}
	return nil
}

// cmdAuditDenied streams the audit log and prints lines where decision=deny.
func cmdAuditDenied(w io.Writer, path string) error {
	return scanAuditEntries(path, func(entry intermediary.AuditEntry, rawLine string) error {
		if entry.Decision == intermediary.Deny {
			fmt.Fprintln(w, rawLine)
		}
		return nil
	})
}

// cmdAuditCounts accumulates per-action counts and prints them sorted.
func cmdAuditCounts(w io.Writer, path string) error {
	counts := make(map[string]int)
	err := scanAuditEntries(path, func(entry intermediary.AuditEntry, _ string) error {
		action := entry.Intent.Action
		if action == "" {
			action = "-"
		}
		counts[action]++
		return nil
	})
	if err != nil {
		return err
	}
	if len(counts) == 0 {
		return nil
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "%s\t%d\n", k, counts[k])
	}
	return nil
}

// cmdAuditRule prints the violation rate for a named policy rule.
func cmdAuditRule(w io.Writer, path string, ruleName string) error {
	var total, matched int
	err := scanAuditEntries(path, func(entry intermediary.AuditEntry, _ string) error {
		total++
		if entry.RuleMatched == ruleName {
			matched++
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "%d/%d violations for rule %s\n", matched, total, ruleName)
	return nil
}

// scanAuditEntries opens path, decodes each JSONL line into an AuditEntry,
// and calls fn. Malformed lines are skipped silently. A missing file returns nil.
func scanAuditEntries(path string, fn func(entry intermediary.AuditEntry, rawLine string) error) error {
	return scanAuditLines(path, func(rawLine string) {
		var entry intermediary.AuditEntry
		if err := json.Unmarshal([]byte(rawLine), &entry); err != nil {
			return // skip malformed lines
		}
		fn(entry, rawLine) //nolint:errcheck
	})
}

// scanAuditLines opens path and calls fn for each raw line. Missing file
// returns nil. Uses a 1 MB scanner buffer to handle large JSON lines.
func scanAuditLines(path string, fn func(rawLine string)) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		fn(line)
	}
	return scanner.Err()
}
