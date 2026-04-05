package reporter

import (
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/evidence"
)

func TestFormatEvidenceSectionEmptyManifest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		manifest *evidence.Manifest
	}{
		{name: "nil", manifest: nil},
		{name: "no claims", manifest: &evidence.Manifest{}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := formatEvidenceSection(tt.manifest); got != "" {
				t.Fatalf("formatEvidenceSection() = %q, want empty string", got)
			}
		})
	}
}

func TestFormatEvidenceSectionRendersTable(t *testing.T) {
	t.Parallel()

	got := formatEvidenceSection(&evidence.Manifest{
		Claims: []evidence.Claim{
			{
				Claim:   "All tests pass",
				Level:   evidence.BehaviorallyChecked,
				Checker: "go test",
				Passed:  true,
			},
		},
	})

	for _, want := range []string{
		"### Verification evidence",
		"| Claim | Level | Checker | Result |",
		"| All tests pass | behaviorally_checked | go test | :white_check_mark: |",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected rendered section to contain %q, got:\n%s", want, got)
		}
	}
}

func TestFormatEvidenceSectionRendersPassedAndFailedRows(t *testing.T) {
	t.Parallel()

	got := formatEvidenceSection(&evidence.Manifest{
		Claims: []evidence.Claim{
			{Claim: "Passing claim", Level: evidence.MechanicallyChecked, Checker: "go test", Passed: true},
			{Claim: "Failing claim", Level: evidence.ObservedInSitu, Checker: "manual repro", Passed: false},
		},
	})

	for _, want := range []string{
		"| Passing claim | mechanically_checked | go test | :white_check_mark: |",
		"| Failing claim | observed_in_situ | manual repro | :x: |",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected rendered section to contain %q, got:\n%s", want, got)
		}
	}
}

func TestFormatEvidenceSectionRendersTrustBoundaries(t *testing.T) {
	t.Parallel()

	got := formatEvidenceSection(&evidence.Manifest{
		Claims: []evidence.Claim{
			{
				Claim:         "All tests pass",
				Level:         evidence.BehaviorallyChecked,
				Checker:       "go test",
				TrustBoundary: "Package-level only",
				Passed:        true,
			},
		},
	})

	for _, want := range []string{
		"<details>",
		"<summary>Trust boundaries</summary>",
		"- **All tests pass** — Package-level only",
		"</details>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected rendered section to contain %q, got:\n%s", want, got)
		}
	}
}

func TestFormatEvidenceCellEscapesMarkdownTableSeparators(t *testing.T) {
	t.Parallel()

	got := formatEvidenceSection(&evidence.Manifest{
		Claims: []evidence.Claim{
			{
				Claim:   "A | B",
				Level:   evidence.BehaviorallyChecked,
				Checker: "line 1\nline 2",
				Passed:  true,
			},
		},
	})

	for _, want := range []string{
		`| A \| B | behaviorally_checked | line 1<br>line 2 | :white_check_mark: |`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected rendered section to contain %q, got:\n%s", want, got)
		}
	}
}
