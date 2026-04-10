package simplicity

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestPropBuildPlanNeverExceedsConfiguredCap(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		maxPRs := rapid.IntRange(1, 5).Draw(t, "max_prs")
		count := rapid.IntRange(1, 12).Draw(t, "count")
		findings := make([]Finding, 0, count)
		for i := 0; i < count; i++ {
			findings = append(findings, Finding{
				ID:         fmt.Sprintf("candidate-%d-%s", i, strings.ToLower(rapid.StringMatching(`[a-z]{4,8}`).Draw(t, "id"))),
				Kind:       "simplification",
				Title:      "refactor: " + rapid.StringMatching(`[a-z]{4,10}`).Draw(t, "title"),
				Summary:    "summary",
				Paths:      []string{"cli/internal/" + rapid.StringMatching(`[a-z]{4,10}`).Draw(t, "path") + ".go"},
				Confidence: rapid.Float64Range(0.8, 1.0).Draw(t, "confidence"),
			})
		}
		file := &FindingsFile{
			Version:     1,
			GeneratedAt: time.Date(2026, 4, 10, 5, 24, 49, 0, time.UTC).Format(time.RFC3339),
			Findings:    findings,
		}
		plan, err := BuildPlan("owner/repo", "main", PlanOptions{
			MaxPRs:                maxPRs,
			MinConfidence:         0.8,
			MinDuplicateLines:     10,
			MinDuplicateLocations: 3,
		}, file, &FindingsFile{
			Version:     1,
			GeneratedAt: file.GeneratedAt,
			Findings:    nil,
		}, time.Date(2026, 4, 10, 5, 24, 49, 0, time.UTC))
		if err != nil {
			t.Fatalf("BuildPlan() error = %v", err)
		}
		if len(plan.Selected) > maxPRs {
			t.Fatalf("len(Selected) = %d, want <= %d", len(plan.Selected), maxPRs)
		}
	})
}

func TestPropSanitizeBranchComponentReturnsSafeSlug(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		raw := rapid.String().Draw(t, "raw")
		got := sanitizeBranchComponent(raw)
		if got == "" {
			t.Fatal("sanitizeBranchComponent() returned empty string")
		}
		for _, r := range got {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				t.Fatalf("sanitizeBranchComponent(%q) produced unsafe rune %q in %q", raw, r, got)
			}
		}
	})
}
