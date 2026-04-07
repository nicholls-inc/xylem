package reporter

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/evidence"
	"pgregory.net/rapid"
)

func TestPropFormatEvidenceCellNoRawPipeOrNewline(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		input := rapid.StringOf(rapid.Rune()).Draw(t, "input")
		got := formatEvidenceCell(input)

		if strings.Contains(got, "\n") {
			t.Fatalf("raw newline in output: %q", got)
		}

		unescaped := strings.ReplaceAll(got, `\|`, "")
		if strings.Contains(unescaped, "|") {
			t.Fatalf("unescaped pipe in output: %q (input: %q)", got, input)
		}
	})
}

func TestPropFormatEvidenceSectionRowCountMatchesClaims(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 20).Draw(t, "n")
		claims := make([]evidence.Claim, n)
		for i := range claims {
			claims[i] = evidence.Claim{
				Claim:   rapid.StringMatching(`[A-Za-z ]+`).Draw(t, fmt.Sprintf("claim-%d", i)),
				Level:   evidence.BehaviorallyChecked,
				Checker: "go test",
				Passed:  rapid.Bool().Draw(t, fmt.Sprintf("passed-%d", i)),
			}
		}

		got := formatEvidenceSection(&evidence.Manifest{Claims: claims})
		if n == 0 {
			if got != "" {
				t.Fatalf("expected empty evidence section for zero claims, got %q", got)
			}
			return
		}

		dataRows := 0
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(line, "| ") && line != "| Claim | Level | Checker | Result |" {
				dataRows++
			}
		}

		if dataRows != n {
			t.Fatalf("expected %d data rows, got %d\n%s", n, dataRows, got)
		}
	})
}
