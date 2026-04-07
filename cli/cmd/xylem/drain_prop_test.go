package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/intermediary"
	"github.com/nicholls-inc/xylem/cli/internal/runner"
)

func TestPropWireRunnerScaffoldingNeverNil(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir, err := os.MkdirTemp("", "xylem-prop-*")
		if err != nil {
			rt.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(dir)

		nRules := rapid.IntRange(0, 5).Draw(rt, "nRules")
		rules := make([]config.PolicyRuleConfig, nRules)
		for i := range rules {
			rules[i] = config.PolicyRuleConfig{
				Action:   rapid.SampledFrom([]string{"*", "file_write", "git_push", "phase_execute"}).Draw(rt, fmt.Sprintf("action-%d", i)),
				Resource: rapid.SampledFrom([]string{"*", ".xylem/HARNESS.md", ".xylem.yml"}).Draw(rt, fmt.Sprintf("resource-%d", i)),
				Effect: rapid.SampledFrom([]string{
					string(intermediary.Allow),
					string(intermediary.Deny),
					string(intermediary.RequireApproval),
				}).Draw(rt, fmt.Sprintf("effect-%d", i)),
			}
		}

		cfg := &config.Config{
			StateDir: dir,
			Harness: config.HarnessConfig{
				Policy: config.PolicyConfig{Rules: rules},
			},
		}
		r := runner.New(cfg, nil, nil, nil)
		wireRunnerScaffolding(cfg, r, nil)

		if r.Intermediary == nil {
			rt.Fatalf("r.Intermediary = nil for nRules=%d", nRules)
		}
		if r.AuditLog == nil {
			rt.Fatalf("r.AuditLog = nil for nRules=%d", nRules)
		}
	})
}

func TestPropWireRunnerScaffoldingAuditLogUnderStateDir(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir, err := os.MkdirTemp("", "xylem-prop-*")
		if err != nil {
			rt.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(dir)

		relativePath := rapid.SampledFrom([]string{
			"audit.jsonl",
			"logs/audit.jsonl",
			"nested/audit-log.jsonl",
		}).Draw(rt, "relativePath")

		cfg := &config.Config{
			StateDir: dir,
			Harness: config.HarnessConfig{
				AuditLog: relativePath,
			},
		}
		r := runner.New(cfg, nil, nil, nil)
		wireRunnerScaffolding(cfg, r, nil)

		expectedPath := filepath.Join(dir, relativePath)
		if err := os.MkdirAll(filepath.Dir(expectedPath), 0o755); err != nil {
			rt.Fatalf("MkdirAll() error = %v", err)
		}

		entry := intermediary.AuditEntry{
			Intent: intermediary.Intent{
				Action:   "phase_execute",
				Resource: "fix",
				AgentID:  "vessel-1",
			},
			Decision:  intermediary.Allow,
			Timestamp: time.Now().UTC(),
		}
		if err := r.AuditLog.Append(entry); err != nil {
			rt.Fatalf("AuditLog.Append() error = %v", err)
		}
		if _, err := os.Stat(expectedPath); err != nil {
			rt.Fatalf("Stat(%q) error = %v", expectedPath, err)
		}
	})
}
