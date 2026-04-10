package continuousstyle

import "testing"

func TestReportValidateRejectsDuplicateFindingIDs(t *testing.T) {
	report := &Report{
		Version:       ReportVersion,
		Repo:          "owner/repo",
		GeneratedAt:   "2026-04-10T00:00:00Z",
		TargetSurface: "cli/cmd/xylem and cli/internal/reporter",
		Findings: []Finding{
			testFinding("stderr-routing", "Route errors consistently"),
			testFinding("stderr-routing", "Duplicate"),
		},
	}

	if err := report.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want duplicate finding id error")
	}
}

func TestSortedFindingsOrdersByPriorityThenID(t *testing.T) {
	report := &Report{
		Findings: []Finding{
			testFinding("beta", "B"),
			testFinding("alpha", "A"),
		},
	}
	report.Findings[0].Priority = 5
	report.Findings[1].Priority = 5

	sorted := SortedFindings(report)
	if len(sorted) != 2 {
		t.Fatalf("len(SortedFindings()) = %d, want 2", len(sorted))
	}
	if sorted[0].ID != "alpha" || sorted[1].ID != "beta" {
		t.Fatalf("SortedFindings() = %#v, want alpha before beta", sorted)
	}
}

func testFinding(id, title string) Finding {
	return Finding{
		ID:             id,
		Title:          title,
		Category:       "consistency",
		Summary:        "summary",
		Recommendation: "recommendation",
		Priority:       7,
		Paths:          []string{"cli/cmd/xylem/status.go"},
		Evidence: []Evidence{{
			Path:      "cli/cmd/xylem/status.go",
			LineStart: 10,
			Summary:   "evidence",
		}},
	}
}
