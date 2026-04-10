package phase

import (
	"testing"

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
