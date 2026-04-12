package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
)

// writeAuditFixture marshals each entry as a JSON line and writes to path.
func writeAuditFixture(t *testing.T, path string, entries []intermediary.AuditEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture %q: %v", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			t.Fatalf("encode entry: %v", err)
		}
	}
}

func makeEntry(action string, decision intermediary.Effect, rule string) intermediary.AuditEntry {
	return intermediary.AuditEntry{
		Intent:      intermediary.Intent{Action: action},
		Decision:    decision,
		Timestamp:   time.Now().UTC(),
		RuleMatched: rule,
	}
}

// --- tail ---

func TestAuditTail_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	out := captureStdout(func() {
		if err := cmdAuditTail(os.Stdout, path, 5); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty output for missing file, got %q", out)
	}
}

func TestAuditTail_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	writeAuditFixture(t, path, nil)
	out := captureStdout(func() {
		if err := cmdAuditTail(os.Stdout, path, 5); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty output, got %q", out)
	}
}

func TestAuditTail_LessThanN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	entries := []intermediary.AuditEntry{
		makeEntry("read", intermediary.Allow, ""),
		makeEntry("write", intermediary.Deny, "no-write"),
	}
	writeAuditFixture(t, path, entries)
	out := captureStdout(func() {
		if err := cmdAuditTail(os.Stdout, path, 10); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	lines := nonEmptyLines(out)
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d: %q", len(lines), out)
	}
}

func TestAuditTail_ExactlyN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	entries := []intermediary.AuditEntry{
		makeEntry("a", intermediary.Allow, ""),
		makeEntry("b", intermediary.Allow, ""),
		makeEntry("c", intermediary.Allow, ""),
	}
	writeAuditFixture(t, path, entries)
	out := captureStdout(func() {
		if err := cmdAuditTail(os.Stdout, path, 3); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	lines := nonEmptyLines(out)
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d: %q", len(lines), out)
	}
}

func TestAuditTail_MoreThanN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	entries := []intermediary.AuditEntry{
		makeEntry("a", intermediary.Allow, ""),
		makeEntry("b", intermediary.Allow, ""),
		makeEntry("c", intermediary.Allow, ""),
		makeEntry("d", intermediary.Allow, ""),
		makeEntry("e", intermediary.Allow, ""),
	}
	writeAuditFixture(t, path, entries)
	out := captureStdout(func() {
		if err := cmdAuditTail(os.Stdout, path, 3); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	lines := nonEmptyLines(out)
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
	// Last 3 entries should be c, d, e — verify first and last lines.
	if !strings.Contains(lines[0], `"c"`) {
		t.Errorf("expected first line to contain action 'c' (oldest kept), got: %q", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], `"e"`) {
		t.Errorf("expected last line to contain action 'e' (newest), got: %q", lines[len(lines)-1])
	}
}

func TestAuditTail_ZeroN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	writeAuditFixture(t, path, []intermediary.AuditEntry{makeEntry("x", intermediary.Allow, "")})
	out := captureStdout(func() {
		if err := cmdAuditTail(os.Stdout, path, 0); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty output for n=0, got %q", out)
	}
}

// --- denied ---

func TestAuditDenied_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	out := captureStdout(func() {
		if err := cmdAuditDenied(os.Stdout, path); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty output, got %q", out)
	}
}

func TestAuditDenied_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	writeAuditFixture(t, path, nil)
	out := captureStdout(func() {
		if err := cmdAuditDenied(os.Stdout, path); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty output, got %q", out)
	}
}

func TestAuditDenied_FiltersDeny(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	entries := []intermediary.AuditEntry{
		makeEntry("read", intermediary.Allow, ""),
		makeEntry("write", intermediary.Deny, "no-write"),
		makeEntry("exec", intermediary.Allow, ""),
		makeEntry("delete", intermediary.Deny, "no-delete"),
	}
	writeAuditFixture(t, path, entries)
	out := captureStdout(func() {
		if err := cmdAuditDenied(os.Stdout, path); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	lines := nonEmptyLines(out)
	if len(lines) != 2 {
		t.Errorf("expected 2 denied lines, got %d: %q", len(lines), out)
	}
	for _, line := range lines {
		var e intermediary.AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("unmarshal output line: %v", err)
		}
		if e.Decision != intermediary.Deny {
			t.Errorf("expected deny decision, got %q", e.Decision)
		}
	}
}

// --- counts ---

func TestAuditCounts_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	out := captureStdout(func() {
		if err := cmdAuditCounts(os.Stdout, path); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty output, got %q", out)
	}
}

func TestAuditCounts_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	writeAuditFixture(t, path, nil)
	out := captureStdout(func() {
		if err := cmdAuditCounts(os.Stdout, path); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty output, got %q", out)
	}
}

func TestAuditCounts_MultipleActions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	entries := []intermediary.AuditEntry{
		makeEntry("read", intermediary.Allow, ""),
		makeEntry("write", intermediary.Deny, ""),
		makeEntry("read", intermediary.Allow, ""),
		makeEntry("exec", intermediary.Allow, ""),
	}
	writeAuditFixture(t, path, entries)
	out := captureStdout(func() {
		if err := cmdAuditCounts(os.Stdout, path); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	lines := nonEmptyLines(out)
	if len(lines) != 3 {
		t.Errorf("expected 3 lines (exec, read, write), got %d: %q", len(lines), out)
	}
	if !strings.Contains(out, "read\t2") {
		t.Errorf("expected 'read\\t2' in output, got: %q", out)
	}
	if !strings.Contains(out, "write\t1") {
		t.Errorf("expected 'write\\t1' in output, got: %q", out)
	}
	if !strings.Contains(out, "exec\t1") {
		t.Errorf("expected 'exec\\t1' in output, got: %q", out)
	}
}

func TestAuditCounts_EmptyActionFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	// Entry with no action field (pre-#366 style)
	entries := []intermediary.AuditEntry{
		{Decision: intermediary.Allow, Timestamp: time.Now().UTC()},
	}
	writeAuditFixture(t, path, entries)
	out := captureStdout(func() {
		if err := cmdAuditCounts(os.Stdout, path); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	if !strings.Contains(out, "-\t1") {
		t.Errorf("expected '-\\t1' for empty action, got: %q", out)
	}
}

func TestAuditCounts_SortedOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	entries := []intermediary.AuditEntry{
		makeEntry("zebra", intermediary.Allow, ""),
		makeEntry("alpha", intermediary.Allow, ""),
		makeEntry("mango", intermediary.Allow, ""),
	}
	writeAuditFixture(t, path, entries)
	out := captureStdout(func() {
		if err := cmdAuditCounts(os.Stdout, path); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	lines := nonEmptyLines(out)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if !strings.HasPrefix(lines[0], "alpha") {
		t.Errorf("line[0] should start with 'alpha', got %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "mango") {
		t.Errorf("line[1] should start with 'mango', got %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "zebra") {
		t.Errorf("line[2] should start with 'zebra', got %q", lines[2])
	}
}

// --- rule ---

func TestAuditRule_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	out := captureStdout(func() {
		if err := cmdAuditRule(os.Stdout, path, "my-rule"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	if !strings.Contains(out, "0/0 violations for rule my-rule") {
		t.Errorf("expected 0/0 violations message, got: %q", out)
	}
}

func TestAuditRule_NoEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	writeAuditFixture(t, path, nil)
	out := captureStdout(func() {
		if err := cmdAuditRule(os.Stdout, path, "my-rule"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	if !strings.Contains(out, "0/0 violations for rule my-rule") {
		t.Errorf("expected 0/0 violations message, got: %q", out)
	}
}

func TestAuditRule_MatchesRule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	entries := []intermediary.AuditEntry{
		makeEntry("write", intermediary.Deny, "no-write"),
		makeEntry("read", intermediary.Allow, ""),
		makeEntry("exec", intermediary.Deny, "no-write"),
	}
	writeAuditFixture(t, path, entries)
	out := captureStdout(func() {
		if err := cmdAuditRule(os.Stdout, path, "no-write"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	if !strings.Contains(out, "2/3 violations for rule no-write") {
		t.Errorf("expected '2/3 violations for rule no-write', got: %q", out)
	}
}

func TestAuditRule_NoMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	entries := []intermediary.AuditEntry{
		makeEntry("write", intermediary.Deny, "other-rule"),
	}
	writeAuditFixture(t, path, entries)
	out := captureStdout(func() {
		if err := cmdAuditRule(os.Stdout, path, "no-such-rule"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	if !strings.Contains(out, "0/1 violations for rule no-such-rule") {
		t.Errorf("expected '0/1 violations for rule no-such-rule', got: %q", out)
	}
}

// --- malformed lines ---

// malformedFixture returns a JSONL file path containing 2 valid entries and 1
// malformed line: read/allow, not-valid-json, write/deny.
func malformedFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	content := `{"intent":{"action":"read"},"decision":"allow","timestamp":"2024-01-01T00:00:00Z"}
not-valid-json
{"intent":{"action":"write"},"decision":"deny","timestamp":"2024-01-01T00:00:00Z"}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestAuditMalformedLinesSkipped(t *testing.T) {
	path := malformedFixture(t)
	// denied: only the valid deny line should appear
	out := captureStdout(func() {
		if err := cmdAuditDenied(os.Stdout, path); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	lines := nonEmptyLines(out)
	if len(lines) != 1 {
		t.Errorf("denied: expected 1 line (malformed skipped), got %d: %q", len(lines), out)
	}
}

func TestAuditMalformedLines_Counts(t *testing.T) {
	path := malformedFixture(t)
	// counts: malformed line must not be counted; only read=1, write=1 from 2 valid lines
	out := captureStdout(func() {
		if err := cmdAuditCounts(os.Stdout, path); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	lines := nonEmptyLines(out)
	if len(lines) != 2 {
		t.Errorf("counts: expected 2 action lines (read, write), got %d: %q", len(lines), out)
	}
	if !strings.Contains(out, "read\t1") {
		t.Errorf("counts: expected 'read\\t1', got: %q", out)
	}
	if !strings.Contains(out, "write\t1") {
		t.Errorf("counts: expected 'write\\t1', got: %q", out)
	}
}

func TestAuditMalformedLines_Rule(t *testing.T) {
	path := malformedFixture(t)
	// rule: total should be 2 (valid lines only), not 3 (which would include the malformed line)
	out := captureStdout(func() {
		if err := cmdAuditRule(os.Stdout, path, "no-such-rule"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	if !strings.Contains(out, "0/2 violations for rule no-such-rule") {
		t.Errorf("rule: expected '0/2 violations' (malformed excluded from total), got: %q", out)
	}
}

// --- cobra wiring ---

func TestAuditTail_CobraWiring(t *testing.T) {
	setupTestDeps(t)
	// Write a fixture to the expected audit path
	auditPath := filepath.Join(deps.cfg.StateDir, "audit.jsonl")
	entries := []intermediary.AuditEntry{
		makeEntry("a", intermediary.Allow, ""),
		makeEntry("b", intermediary.Allow, ""),
		makeEntry("c", intermediary.Allow, ""),
		makeEntry("d", intermediary.Allow, ""),
		makeEntry("e", intermediary.Allow, ""),
	}
	writeAuditFixture(t, auditPath, entries)

	cmd := newRootCmd()
	cmd.PersistentPreRunE = nil
	cmd.SetArgs([]string{"audit", "tail", "-n", "3"})

	out := captureStdout(func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	lines := nonEmptyLines(out)
	if len(lines) != 3 {
		t.Errorf("expected 3 lines from tail -n 3, got %d: %q", len(lines), out)
	}
}

// nonEmptyLines splits s by newline and returns non-empty lines.
func nonEmptyLines(s string) []string {
	var result []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			result = append(result, l)
		}
	}
	return result
}

// --- smoke scenarios (§13.3 acceptance criteria) ---
// Each TestSmoke_S* maps directly to one acceptance criterion from issue #387.
// Tests use full cobra wiring (cmd.Execute) to verify the end-to-end path,
// not just the internal cmdAudit* helpers.

// TestSmoke_S1_TailPrintsLastNEntries verifies AC2:
// "xylem audit tail -n 5 prints last 5 entries from
// config.RuntimePath(state_dir, 'audit.jsonl')".
func TestSmoke_S1_TailPrintsLastNEntries(t *testing.T) {
	setupTestDeps(t)
	auditPath := filepath.Join(deps.cfg.StateDir, "audit.jsonl")
	// Write 8 entries: tail -n 5 should return only the last 5.
	entries := []intermediary.AuditEntry{
		makeEntry("a", intermediary.Allow, ""),
		makeEntry("b", intermediary.Allow, ""),
		makeEntry("c", intermediary.Allow, ""),
		makeEntry("d", intermediary.Allow, ""),
		makeEntry("e", intermediary.Allow, ""),
		makeEntry("f", intermediary.Allow, ""),
		makeEntry("g", intermediary.Allow, ""),
		makeEntry("h", intermediary.Allow, ""),
	}
	writeAuditFixture(t, auditPath, entries)

	cmd := newRootCmd()
	cmd.PersistentPreRunE = nil
	cmd.SetArgs([]string{"audit", "tail", "-n", "5"})

	out := captureStdout(func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	lines := nonEmptyLines(out)
	if len(lines) != 5 {
		t.Errorf("expected 5 lines from tail -n 5 on 8 entries, got %d: %q", len(lines), out)
	}
	// The last entry should be "h" (newest).
	if !strings.Contains(lines[len(lines)-1], `"h"`) {
		t.Errorf("expected last line to contain action 'h', got: %q", lines[len(lines)-1])
	}
	// The first kept entry should be "d" (oldest in the last 5).
	if !strings.Contains(lines[0], `"d"`) {
		t.Errorf("expected first line to contain action 'd', got: %q", lines[0])
	}
}

// TestSmoke_S2_DeniedEmptyOnCleanLog verifies AC3 (first half):
// "xylem audit denied is empty on a pre-#366 log".
// A log containing only Allow entries should produce no output.
func TestSmoke_S2_DeniedEmptyOnCleanLog(t *testing.T) {
	setupTestDeps(t)
	auditPath := filepath.Join(deps.cfg.StateDir, "audit.jsonl")
	// Pre-#366-style log: entries exist but none are denied.
	entries := []intermediary.AuditEntry{
		makeEntry("read", intermediary.Allow, ""),
		makeEntry("exec", intermediary.Allow, ""),
	}
	writeAuditFixture(t, auditPath, entries)

	cmd := newRootCmd()
	cmd.PersistentPreRunE = nil
	cmd.SetArgs([]string{"audit", "denied"})

	out := captureStdout(func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty output for all-allow log, got: %q", out)
	}
}

// TestSmoke_S2b_DeniedNonEmptyAfterDeny verifies AC3 (second half):
// "xylem audit denied is non-empty after a deny".
func TestSmoke_S2b_DeniedNonEmptyAfterDeny(t *testing.T) {
	setupTestDeps(t)
	auditPath := filepath.Join(deps.cfg.StateDir, "audit.jsonl")
	entries := []intermediary.AuditEntry{
		makeEntry("read", intermediary.Allow, ""),
		makeEntry("write", intermediary.Deny, "no-write"),
	}
	writeAuditFixture(t, auditPath, entries)

	cmd := newRootCmd()
	cmd.PersistentPreRunE = nil
	cmd.SetArgs([]string{"audit", "denied"})

	out := captureStdout(func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	lines := nonEmptyLines(out)
	if len(lines) != 1 {
		t.Errorf("expected 1 denied line, got %d: %q", len(lines), out)
	}
	// Verify the emitted line decodes to a deny entry.
	var e intermediary.AuditEntry
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatalf("unmarshal denied output: %v", err)
	}
	if e.Decision != intermediary.Deny {
		t.Errorf("expected deny decision in output, got %q", e.Decision)
	}
}

// TestSmoke_S3_CountsIncludesEveryDistinctAction verifies AC4:
// "xylem audit counts output includes every distinct action present in the log".
func TestSmoke_S3_CountsIncludesEveryDistinctAction(t *testing.T) {
	setupTestDeps(t)
	auditPath := filepath.Join(deps.cfg.StateDir, "audit.jsonl")
	entries := []intermediary.AuditEntry{
		makeEntry("read", intermediary.Allow, ""),
		makeEntry("write", intermediary.Deny, ""),
		makeEntry("exec", intermediary.Allow, ""),
		makeEntry("read", intermediary.Allow, ""),
		makeEntry("delete", intermediary.Deny, ""),
	}
	writeAuditFixture(t, auditPath, entries)

	cmd := newRootCmd()
	cmd.PersistentPreRunE = nil
	cmd.SetArgs([]string{"audit", "counts"})

	out := captureStdout(func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	// Every distinct action must appear in the output.
	for _, action := range []string{"read", "write", "exec", "delete"} {
		if !strings.Contains(out, action) {
			t.Errorf("expected action %q in counts output, got: %q", action, out)
		}
	}
	// read appears twice — verify count is 2.
	if !strings.Contains(out, "read\t2") {
		t.Errorf("expected 'read\\t2' in counts output, got: %q", out)
	}
	// Exactly 4 distinct actions → 4 output lines.
	lines := nonEmptyLines(out)
	if len(lines) != 4 {
		t.Errorf("expected 4 lines (4 distinct actions), got %d: %q", len(lines), out)
	}
}
