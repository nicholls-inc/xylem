package phase

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/ctxmgr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTruncateOutput(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "short string under limit",
			input:  "hello",
			maxLen: 100,
			want:   "hello",
		},
		{
			name:   "exact length string",
			input:  "abcde",
			maxLen: 5,
			want:   "abcde",
		},
		{
			name:   "string exceeding limit",
			input:  "abcdefghij",
			maxLen: 5,
			want:   "abcde" + fmt.Sprintf(TruncationSuffix, 5),
		},
		{
			name:   "empty string",
			input:  "",
			maxLen: 100,
			want:   "",
		},
		{
			name:   "string of exactly limit plus one",
			input:  "abcdef",
			maxLen: 5,
			want:   "abcde" + fmt.Sprintf(TruncationSuffix, 5),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateOutput(tt.input, tt.maxLen)
			if got != tt.want {
				t.Fatalf("TruncateOutput(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestRenderPrompt(t *testing.T) {
	tests := []struct {
		name     string
		template string
		data     TemplateData
		want     string
		wantErr  bool
	}{
		{
			name:     "renders issue title",
			template: "Title: {{.Issue.Title}}",
			data: TemplateData{
				Issue: IssueData{Title: "Fix the bug"},
			},
			want: "Title: Fix the bug",
		},
		{
			name:     "renders issue body",
			template: "Body: {{.Issue.Body}}",
			data: TemplateData{
				Issue: IssueData{Body: "Detailed description here"},
			},
			want: "Body: Detailed description here",
		},
		{
			name:     "renders issue labels",
			template: "Labels: {{.Issue.Labels}}",
			data: TemplateData{
				Issue: IssueData{Labels: []string{"bug", "urgent"}},
			},
			want: "Labels: [bug urgent]",
		},
		{
			name:     "renders phase name and index",
			template: "Phase: {{.Phase.Name}} ({{.Phase.Index}})",
			data: TemplateData{
				Phase: PhaseData{Name: "analyze", Index: 0},
			},
			want: "Phase: analyze (0)",
		},
		{
			name:     "renders previous phase output",
			template: "Previous: {{.PreviousOutputs.analyze}}",
			data: TemplateData{
				PreviousOutputs: map[string]string{
					"analyze": "analysis result",
				},
			},
			want: "Previous: analysis result",
		},
		{
			name:     "nonexistent previous output renders empty",
			template: "Previous: [{{.PreviousOutputs.nonexistent}}]",
			data: TemplateData{
				PreviousOutputs: map[string]string{
					"analyze": "analysis result",
				},
			},
			want: "Previous: []",
		},
		{
			name:     "renders gate result",
			template: "Gate: {{.GateResult}}",
			data: TemplateData{
				GateResult: "all checks passed",
			},
			want: "Gate: all checks passed",
		},
		{
			name:     "renders evaluation context",
			template: "Iteration: {{.Evaluation.Iteration}}\nFeedback: {{.Evaluation.Feedback}}\nOutput: {{.Evaluation.Output}}\nCriteria: {{.Evaluation.Criteria}}",
			data: TemplateData{
				Evaluation: EvaluationData{
					Iteration: 2,
					Feedback:  "Add tests",
					Output:    "draft 1",
					Criteria:  "correctness",
				},
			},
			want: "Iteration: 2\nFeedback: Add tests\nOutput: draft 1\nCriteria: correctness",
		},
		{
			name:     "renders vessel ID and source",
			template: "Vessel: {{.Vessel.ID}} from {{.Vessel.Source}}",
			data: TemplateData{
				Vessel: VesselData{ID: "issue-42", Source: "github"},
			},
			want: "Vessel: issue-42 from github",
		},
		{
			name:     "renders vessel ref and metadata",
			template: `Ref: {{.Vessel.Ref}} Source: {{index .Vessel.Meta "schedule.source_name"}} Fired: {{index .Vessel.Meta "schedule.fired_at"}}`,
			data: TemplateData{
				Vessel: VesselData{
					ID:     "schedule-security-compliance-20260410",
					Ref:    "schedule://security-compliance/2026-04-10T00:00:00Z",
					Source: "schedule",
					Meta: map[string]string{
						"schedule.source_name": "security-compliance",
						"schedule.fired_at":    "2026-04-10T00:00:00Z",
					},
				},
			},
			want: "Ref: schedule://security-compliance/2026-04-10T00:00:00Z Source: security-compliance Fired: 2026-04-10T00:00:00Z",
		},
		{
			name:     "template syntax error",
			template: "{{.BadSyntax",
			data:     TemplateData{},
			wantErr:  true,
		},
		{
			name:     "undefined function",
			template: "{{undefined}}",
			data:     TemplateData{},
			wantErr:  true,
		},
		{
			name:     "empty template",
			template: "",
			data:     TemplateData{},
			want:     "",
		},
		{
			name:     "complex template combining multiple fields",
			template: "Issue #{{.Issue.Number}}: {{.Issue.Title}}\nURL: {{.Issue.URL}}\nPhase: {{.Phase.Name}} ({{.Phase.Index}})\nVessel: {{.Vessel.ID}}\nPrevious: {{.PreviousOutputs.plan}}\nGate: {{.GateResult}}",
			data: TemplateData{
				Issue: IssueData{
					URL:    "https://github.com/org/repo/issues/7",
					Title:  "Add caching layer",
					Number: 7,
				},
				Phase: PhaseData{Name: "implement", Index: 2},
				PreviousOutputs: map[string]string{
					"plan": "Step 1: Add Redis. Step 2: Wire it up.",
				},
				GateResult: "tests pass",
				Vessel:     VesselData{ID: "issue-7", Source: "github"},
			},
			want: "Issue #7: Add caching layer\nURL: https://github.com/org/repo/issues/7\nPhase: implement (2)\nVessel: issue-7\nPrevious: Step 1: Add Redis. Step 2: Wire it up.\nGate: tests pass",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RenderPrompt(tt.template, tt.data)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("RenderPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderPromptTruncation(t *testing.T) {
	longString := func(n int) string {
		return strings.Repeat("x", n)
	}

	t.Run("PreviousOutputs value exceeding limit is truncated", func(t *testing.T) {
		data := TemplateData{
			PreviousOutputs: map[string]string{
				"analyze": longString(MaxPreviousOutputLen + 500),
			},
		}
		got, err := RenderPrompt("{{.PreviousOutputs.analyze}}", data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		suffix := fmt.Sprintf(TruncationSuffix, MaxPreviousOutputLen)
		if !strings.HasSuffix(got, suffix) {
			t.Fatalf("expected truncation suffix, got ending: %q", got[len(got)-80:])
		}
		// The content before the suffix should be exactly MaxPreviousOutputLen chars.
		contentLen := len(got) - len(suffix)
		if contentLen != MaxPreviousOutputLen {
			t.Fatalf("content length = %d, want %d", contentLen, MaxPreviousOutputLen)
		}
	})

	t.Run("GateResult exceeding limit is truncated", func(t *testing.T) {
		data := TemplateData{
			GateResult: longString(MaxGateResultLen + 100),
		}
		got, err := RenderPrompt("{{.GateResult}}", data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		suffix := fmt.Sprintf(TruncationSuffix, MaxGateResultLen)
		if !strings.HasSuffix(got, suffix) {
			t.Fatalf("expected truncation suffix")
		}
	})

	t.Run("Issue.Body exceeding limit is truncated", func(t *testing.T) {
		data := TemplateData{
			Issue: IssueData{Body: longString(MaxIssueBodyLen + 200)},
		}
		got, err := RenderPrompt("{{.Issue.Body}}", data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		suffix := fmt.Sprintf(TruncationSuffix, MaxIssueBodyLen)
		if !strings.HasSuffix(got, suffix) {
			t.Fatalf("expected truncation suffix")
		}
	})

	t.Run("only large PreviousOutputs value is truncated", func(t *testing.T) {
		shortVal := "short output"
		longVal := longString(MaxPreviousOutputLen + 100)
		data := TemplateData{
			PreviousOutputs: map[string]string{
				"small": shortVal,
				"large": longVal,
			},
		}

		// Check that small value is not truncated.
		got, err := RenderPrompt("{{.PreviousOutputs.small}}", data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != shortVal {
			t.Fatalf("small value was modified: got %q, want %q", got, shortVal)
		}

		// Check that large value is truncated.
		got, err = RenderPrompt("{{.PreviousOutputs.large}}", data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		suffix := fmt.Sprintf(TruncationSuffix, MaxPreviousOutputLen)
		if !strings.HasSuffix(got, suffix) {
			t.Fatalf("expected truncation suffix for large value")
		}
	})

	t.Run("Evaluation fields exceeding limits are truncated", func(t *testing.T) {
		data := TemplateData{
			Evaluation: EvaluationData{
				Feedback: longString(MaxEvalFeedbackLen + 100),
				Output:   longString(MaxEvalOutputLen + 100),
				Criteria: longString(MaxEvalCriteriaLen + 100),
			},
		}
		got, err := RenderPrompt("{{.Evaluation.Feedback}}\n{{.Evaluation.Output}}\n{{.Evaluation.Criteria}}", data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, fmt.Sprintf(TruncationSuffix, MaxEvalFeedbackLen)) {
			t.Fatalf("expected feedback truncation suffix")
		}
		if !strings.Contains(got, fmt.Sprintf(TruncationSuffix, MaxEvalOutputLen)) {
			t.Fatalf("expected output truncation suffix")
		}
		if !strings.Contains(got, fmt.Sprintf(TruncationSuffix, MaxEvalCriteriaLen)) {
			t.Fatalf("expected criteria truncation suffix")
		}
	})
}

func TestPrepareDataDoesNotMutateOriginal(t *testing.T) {
	original := TemplateData{
		PreviousOutputs: map[string]string{
			"analyze": strings.Repeat("x", MaxPreviousOutputLen+100),
		},
		GateResult: strings.Repeat("y", MaxGateResultLen+100),
		Issue:      IssueData{Body: strings.Repeat("z", MaxIssueBodyLen+100)},
		Vessel: VesselData{
			Meta: map[string]string{
				"schedule.source_name": "security-compliance",
			},
		},
	}

	originalAnalyzeLen := len(original.PreviousOutputs["analyze"])
	originalGateLen := len(original.GateResult)
	originalBodyLen := len(original.Issue.Body)

	_ = prepareData(original)

	if len(original.PreviousOutputs["analyze"]) != originalAnalyzeLen {
		t.Fatal("prepareData mutated original PreviousOutputs")
	}
	if len(original.GateResult) != originalGateLen {
		t.Fatal("prepareData mutated original GateResult")
	}
	if len(original.Issue.Body) != originalBodyLen {
		t.Fatal("prepareData mutated original Issue.Body")
	}
	if original.Vessel.Meta["schedule.source_name"] != "security-compliance" {
		t.Fatal("prepareData mutated original Vessel.Meta")
	}
}

func TestRenderPromptHyphenatedKeys(t *testing.T) {
	t.Run("index syntax accesses hyphenated PreviousOutputs key", func(t *testing.T) {
		data := TemplateData{
			PreviousOutputs: map[string]string{
				"create-issues": "created 3 issues",
			},
		}
		got, err := RenderPrompt(`Result: {{index .PreviousOutputs "create-issues"}}`, data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "Result: created 3 issues" {
			t.Fatalf("got %q, want %q", got, "Result: created 3 issues")
		}
	})

	t.Run("index syntax returns empty for nonexistent hyphenated key", func(t *testing.T) {
		data := TemplateData{
			PreviousOutputs: map[string]string{
				"analyze": "analysis result",
			},
		}
		got, err := RenderPrompt(`Result: [{{index .PreviousOutputs "no-such-phase"}}]`, data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "Result: []" {
			t.Fatalf("got %q, want %q", got, "Result: []")
		}
	})
}

func TestRenderPromptNilPreviousOutputs(t *testing.T) {
	data := TemplateData{
		PreviousOutputs: nil,
	}
	got, err := RenderPrompt("done", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "done" {
		t.Fatalf("got %q, want %q", got, "done")
	}
}

func TestRenderPromptWithOptions(t *testing.T) {
	t.Run("small prompt is unchanged", func(t *testing.T) {
		got, err := RenderPromptWithOptions("Title: {{.Issue.Title}}", TemplateData{
			Issue: IssueData{Title: "compact me not"},
		}, RenderOptions{ContextBudget: 100})
		if err != nil {
			t.Fatalf("RenderPromptWithOptions() error = %v", err)
		}
		if got != "Title: compact me not" {
			t.Fatalf("RenderPromptWithOptions() = %q, want %q", got, "Title: compact me not")
		}
	})

	t.Run("oversize prompt drops oldest previous outputs first", func(t *testing.T) {
		data := TemplateData{
			PreviousOutputs: map[string]string{
				"analyze": strings.Repeat("a", 1200),
				"plan":    strings.Repeat("b", 1200),
				"recent":  strings.Repeat("c", 1200),
			},
			PreviousOutputOrder: []string{"analyze", "plan", "recent"},
		}
		got, err := RenderPromptWithOptions(
			`{{.PreviousOutputs.analyze}}|{{.PreviousOutputs.plan}}|{{.PreviousOutputs.recent}}`,
			data,
			RenderOptions{ContextBudget: 500},
		)
		if err != nil {
			t.Fatalf("RenderPromptWithOptions() error = %v", err)
		}
		if ctxmgr.EstimateTokens(got) > 500 {
			t.Fatalf("EstimateTokens() = %d, want <= 500", ctxmgr.EstimateTokens(got))
		}
		if strings.Contains(got, strings.Repeat("a", 32)) {
			t.Fatal("expected oldest previous output to be compacted out first")
		}
		if !strings.Contains(got, strings.Repeat("c", 32)) {
			t.Fatal("expected most recent previous output to survive initial compaction")
		}
	})

	t.Run("accounts for preamble rounding in final budget", func(t *testing.T) {
		got, err := RenderPromptWithOptions("AAAAA", TemplateData{}, RenderOptions{
			ContextBudget: 1,
			Preamble:      "\t",
		})
		if err != nil {
			t.Fatalf("RenderPromptWithOptions() error = %v", err)
		}
		if ctxmgr.EstimateTokens("\t\n\n"+got) > 1 {
			t.Fatalf("EstimateTokens() = %d, want <= 1", ctxmgr.EstimateTokens("\t\n\n"+got))
		}
		if got != "AAAA" {
			t.Fatalf("RenderPromptWithOptions() = %q, want %q", got, "AAAA")
		}
	})
}

func TestSmoke_S1_SmallPromptUnchanged(t *testing.T) {
	got, err := RenderPromptWithOptions("Title: {{.Issue.Title}}", TemplateData{
		Issue: IssueData{Title: "compact me not"},
	}, RenderOptions{ContextBudget: 100})

	require.NoError(t, err)
	assert.Equal(t, "Title: compact me not", got)
}

func TestSmoke_S2_OversizePromptCompactedToBudget(t *testing.T) {
	data := TemplateData{
		PreviousOutputs: map[string]string{
			"analyze": strings.Repeat("a", 1200),
			"plan":    strings.Repeat("b", 1200),
			"recent":  strings.Repeat("c", 1200),
		},
		PreviousOutputOrder: []string{"analyze", "plan", "recent"},
	}

	got, err := RenderPromptWithOptions(
		`{{.PreviousOutputs.analyze}}|{{.PreviousOutputs.plan}}|{{.PreviousOutputs.recent}}`,
		data,
		RenderOptions{ContextBudget: 500},
	)

	require.NoError(t, err)
	assert.LessOrEqual(t, ctxmgr.EstimateTokens(got), 500)
	assert.NotContains(t, got, strings.Repeat("a", 32))
	assert.Contains(t, got, strings.Repeat("c", 32))
}

func TestSmoke_S3_HarnessPreamblePreservedVerbatim(t *testing.T) {
	preamble := "HARNESS RULES\n- keep this exact"
	body := strings.Repeat("body paragraph\n", 400)

	got := ApplyContextBudget(preamble+"\n\n"+body, RenderOptions{
		ContextBudget: 500,
		Preamble:      preamble,
	})

	require.NotEmpty(t, got)
	assert.True(t, strings.HasPrefix(got, preamble+"\n\n"))
	assert.LessOrEqual(t, ctxmgr.EstimateTokens(got), 500)
}

func TestApplyContextBudgetPreservesPreamble(t *testing.T) {
	preamble := "HARNESS RULES\n- keep this exact"
	body := strings.Repeat("body paragraph\n", 400)
	got := ApplyContextBudget(preamble+"\n\n"+body, RenderOptions{
		ContextBudget: 500,
		Preamble:      preamble,
	})
	if !strings.HasPrefix(got, preamble+"\n\n") {
		t.Fatalf("ApplyContextBudget() did not preserve preamble verbatim: %q", got[:len(preamble)+2])
	}
	if ctxmgr.EstimateTokens(got) > 500 {
		t.Fatalf("EstimateTokens() = %d, want <= 500", ctxmgr.EstimateTokens(got))
	}
}

func TestSummarizeText(t *testing.T) {
	findBudget := func(t *testing.T, predicate func(maxTokens, remaining int, notice string) bool) (int, int, string) {
		t.Helper()
		for maxTokens := 1; maxTokens <= 100; maxTokens++ {
			notice := fmt.Sprintf("[context compacted to fit %d tokens]\n", maxTokens)
			remaining := maxTokens*4 - len(notice)
			if predicate(maxTokens, remaining, notice) {
				return maxTokens, remaining, notice
			}
		}
		t.Fatal("failed to find matching maxTokens")
		return 0, 0, ""
	}

	t.Run("returns original content when already within budget", func(t *testing.T) {
		content := "hi"
		got := summarizeText(content, 2)
		if got != content {
			t.Fatalf("summarizeText(%q, 2) = %q, want %q", content, got, content)
		}
	})

	t.Run("returns empty string for non-positive budgets", func(t *testing.T) {
		got := summarizeText("will be dropped", 0)
		if got != "" {
			t.Fatalf("summarizeText() = %q, want empty string", got)
		}
	})

	t.Run("truncates compaction notice when the notice alone exceeds the budget", func(t *testing.T) {
		content := strings.Repeat("x", 8)
		const maxTokens = 1
		notice := fmt.Sprintf("[context compacted to fit %d tokens]\n", maxTokens)

		got := summarizeText(content, maxTokens)
		want := notice[:maxTokens*4]
		if got != want {
			t.Fatalf("summarizeText() = %q, want %q", got, want)
		}
	})

	t.Run("appends only the available prefix when a few characters remain", func(t *testing.T) {
		maxTokens, remaining, notice := findBudget(t, func(_ int, remaining int, _ string) bool {
			return remaining > 0 && remaining <= 5
		})
		content := strings.Repeat("abcdef", maxTokens)

		got := summarizeText(content, maxTokens)
		want := notice + content[:remaining]
		if got != want {
			t.Fatalf("summarizeText() = %q, want %q", got, want)
		}
	})

	t.Run("keeps both head and tail with ellipsis when there is room for a summary", func(t *testing.T) {
		maxTokens, remaining, notice := findBudget(t, func(maxTokens, remaining int, _ string) bool {
			return remaining > len("\n...\n") && maxTokens > minSummaryTokens
		})
		content := strings.Repeat("0123456789", maxTokens*2)
		payload := remaining - len("\n...\n")
		headLen := payload * 3 / 4
		tailLen := payload - headLen

		got := summarizeText(content, maxTokens)
		want := notice + content[:headLen] + "\n...\n" + content[len(content)-tailLen:]
		if got != want {
			t.Fatalf("summarizeText() = %q, want %q", got, want)
		}
	})
}
