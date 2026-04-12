package phase

import (
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/ctxmgr"
	"pgregory.net/rapid"
)

func TestPropRenderPromptInterpolatesMergeMainPreviousOutput(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		mergeOutput := rapid.StringMatching(`[\t\n\r -~]{0,512}`).Draw(t, "mergeOutput")

		got, err := RenderPrompt("{{.PreviousOutputs.merge_main}}", TemplateData{
			PreviousOutputs: map[string]string{
				"merge_main": mergeOutput,
			},
		})
		if err != nil {
			t.Fatalf("RenderPrompt() error = %v", err)
		}
		if got != mergeOutput {
			t.Fatalf("RenderPrompt() = %q, want %q", got, mergeOutput)
		}
	})
}

func TestPropApplyContextBudgetPreservesConfiguredPreamble(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		preamble := rapid.StringMatching(`[\t\n\r -~]{1,64}`).Draw(t, "preamble")
		body := rapid.StringN(0, 4096, 4096).Draw(t, "body")
		prefix := preamble + "\n\n"
		minBudget := max(1, ctxmgr.EstimateTokens(prefix))
		budget := rapid.IntRange(minBudget, minBudget+2048).Draw(t, "budget")

		got := ApplyContextBudget(prefix+body, RenderOptions{
			ContextBudget: budget,
			Preamble:      preamble,
		})
		if len(got) < len(prefix) || got[:len(prefix)] != prefix {
			t.Fatalf("ApplyContextBudget() did not preserve prefix %q in %q", prefix, got)
		}
		if ctxmgr.EstimateTokens(got) > budget {
			t.Fatalf("EstimateTokens() = %d, want <= %d", ctxmgr.EstimateTokens(got), budget)
		}
	})
}
