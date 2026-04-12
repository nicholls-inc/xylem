package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
)

// genAuditEntry generates a random AuditEntry.
func genAuditEntry(t *rapid.T) intermediary.AuditEntry {
	action := rapid.StringMatching(`[a-z]{1,10}`).Draw(t, "action")
	effects := []intermediary.Effect{intermediary.Allow, intermediary.Deny, intermediary.RequireApproval}
	effectIdx := rapid.IntRange(0, len(effects)-1).Draw(t, "effect_idx")
	rule := rapid.StringMatching(`[a-z-]{0,15}`).Draw(t, "rule")
	return intermediary.AuditEntry{
		Intent:      intermediary.Intent{Action: action},
		Decision:    effects[effectIdx],
		Timestamp:   time.Now().UTC(),
		RuleMatched: rule,
	}
}

// writeAuditEntriesToPath writes entries as JSONL to path.
func writeAuditEntriesToPath(t *rapid.T, path string, entries []intermediary.AuditEntry) {
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
}

// captureWriter captures output from a function writing to an io.Writer.
func captureWriter(fn func(w io.Writer) error) (string, error) {
	var buf bytes.Buffer
	err := fn(&buf)
	return buf.String(), err
}

// TestPropAuditCountsNeverExceedsTotal verifies that the sum of all action
// counts equals the number of successfully-decoded lines.
func TestPropAuditCountsNeverExceedsTotal(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		entries := rapid.SliceOf(rapid.Custom(genAuditEntry)).Draw(rt, "entries")
		dir, err := os.MkdirTemp("", "xylem-audit-prop-*")
		if err != nil {
			rt.Fatalf("MkdirTemp: %v", err)
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "audit.jsonl")
		writeAuditEntriesToPath(rt, path, entries)

		out, err := captureWriter(func(w io.Writer) error {
			return cmdAuditCounts(w, path)
		})
		if err != nil {
			rt.Fatalf("cmdAuditCounts: %v", err)
		}

		// Sum all counts from output.
		var sumCounts int
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if line == "" {
				continue
			}
			parts := strings.Split(line, "\t")
			if len(parts) != 2 {
				rt.Fatalf("malformed counts line: %q", line)
			}
			var c int
			if _, err := fmt.Sscanf(parts[1], "%d", &c); err != nil {
				rt.Fatalf("parse count %q: %v", parts[1], err)
			}
			sumCounts += c
		}

		if sumCounts != len(entries) {
			rt.Fatalf("sum of counts %d != number of entries %d", sumCounts, len(entries))
		}
	})
}

// TestPropAuditTailNeverExceedsN verifies that tail output lines <= min(n, total).
func TestPropAuditTailNeverExceedsN(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		entries := rapid.SliceOf(rapid.Custom(genAuditEntry)).Draw(rt, "entries")
		n := rapid.IntRange(0, 50).Draw(rt, "n")
		dir, err := os.MkdirTemp("", "xylem-audit-prop-*")
		if err != nil {
			rt.Fatalf("MkdirTemp: %v", err)
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "audit.jsonl")
		writeAuditEntriesToPath(rt, path, entries)

		out, err := captureWriter(func(w io.Writer) error {
			return cmdAuditTail(w, path, n)
		})
		if err != nil {
			rt.Fatalf("cmdAuditTail: %v", err)
		}

		var lineCount int
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) != "" {
				lineCount++
			}
		}

		expected := n
		if len(entries) < n {
			expected = len(entries)
		}
		if lineCount != expected {
			rt.Fatalf("tail returned %d lines, expected exactly %d (n=%d, total=%d)", lineCount, expected, n, len(entries))
		}
	})
}

// TestPropAuditDeniedSubsetOfAll verifies that denied line count <= total line
// count and every denied line decodes with Decision==Deny.
func TestPropAuditDeniedSubsetOfAll(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		entries := rapid.SliceOf(rapid.Custom(genAuditEntry)).Draw(rt, "entries")
		dir, err := os.MkdirTemp("", "xylem-audit-prop-*")
		if err != nil {
			rt.Fatalf("MkdirTemp: %v", err)
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "audit.jsonl")
		writeAuditEntriesToPath(rt, path, entries)

		out, err := captureWriter(func(w io.Writer) error {
			return cmdAuditDenied(w, path)
		})
		if err != nil {
			rt.Fatalf("cmdAuditDenied: %v", err)
		}

		var deniedLines []string
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) != "" {
				deniedLines = append(deniedLines, line)
			}
		}

		// Denied count must not exceed total
		if len(deniedLines) > len(entries) {
			rt.Fatalf("denied lines %d > total entries %d", len(deniedLines), len(entries))
		}

		// Every denied line must decode with Decision==Deny
		for _, line := range deniedLines {
			var e intermediary.AuditEntry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				rt.Fatalf("unmarshal denied line %q: %v", line, err)
			}
			if e.Decision != intermediary.Deny {
				rt.Fatalf("denied output line has decision=%q, want deny", e.Decision)
			}
		}
	})
}
