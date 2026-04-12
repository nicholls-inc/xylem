package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/bootstrap"
	"github.com/nicholls-inc/xylem/cli/internal/config"
	"pgregory.net/rapid"
)

func TestPropConfigValidateAcceptsProposedValidationAssignments(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp("", "xylem-config-validate-*")
		if err != nil {
			t.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(dir)

		configPath := filepath.Join(dir, ".xylem.yml")
		configBody := `sources:
  github:
    type: github
    repo: owner/name
    tasks:
      adapt:
        labels: [bootstrap]
        workflow: adapt-repo
concurrency: 2
max_turns: 50
timeout: "30m"
claude:
  command: "claude"
  default_model: "claude-sonnet-4-6"
`
		if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", configPath, err)
		}

		key := rapid.SampledFrom([]string{
			"validation.format",
			"validation.lint",
			"validation.build",
			"validation.test",
		}).Draw(t, "key")
		value := rapid.SampledFrom([]string{
			"goimports -l .",
			"cd cli && go vet ./...",
			"cd cli && go build ./cmd/xylem",
			"cd cli && go test ./...",
		}).Draw(t, "value")

		plan := bootstrap.AdaptPlan{
			SchemaVersion: 1,
			Detected:      bootstrap.AdaptPlanDetected{},
			PlannedChanges: []bootstrap.AdaptPlanChange{{
				Path:        ".xylem.yml",
				Op:          "patch",
				Rationale:   "apply validation override",
				DiffSummary: key + ": " + value,
			}},
			Skipped: []bootstrap.AdaptPlanSkipped{},
		}
		data, err := json.Marshal(plan)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		planPath := filepath.Join(dir, "adapt-plan.json")
		if err := os.WriteFile(planPath, append(data, '\n'), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", planPath, err)
		}

		if err := cmdConfigValidate(configPath, planPath, io.Discard); err != nil {
			t.Fatalf("cmdConfigValidate() error = %v", err)
		}
	})
}

func TestPropApplyConfigDiffSummaryRejectsUnknownKeys(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		key := rapid.StringMatching(`[a-z][a-z0-9_.-]{2,16}`).Draw(t, "key")
		if key == "validation.format" || key == "validation.lint" || key == "validation.build" || key == "validation.test" || key == "profiles" {
			key = "daemon.scan_interval"
		}

		err := applyConfigDiffSummary(&config.Config{}, key+": value")
		if err == nil {
			t.Fatalf("applyConfigDiffSummary(%q) succeeded, want error", key)
		}
		if !strings.Contains(err.Error(), key) {
			t.Fatalf("applyConfigDiffSummary(%q) error = %v, want key in error", key, err)
		}
	})
}
