package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"pgregory.net/rapid"
)

var validAllowedPaths = []string{
	".xylem.yml",
	"AGENTS.md",
	".xylem/workflows/fix-bug.yaml",
	".xylem/workflows/implement-feature.yml",
	".xylem/prompts/analyze/prompt.md",
	".xylem/HARNESS.md",
	"docs/README.md",
	"docs/adr/0001.md",
	"docs/getting-started.md",
}

var validNonDeleteOps = []string{"patch", "replace", "create"}

var validWorkflowPaths = []string{
	".xylem/workflows/fix-bug.yaml",
	".xylem/workflows/implement-feature.yml",
	".xylem/workflows/adapt-repo.yaml",
}

// genValidAdaptPlan generates an AdaptPlan that always passes Validate().
func genValidAdaptPlan(t *rapid.T) AdaptPlan {
	numChanges := rapid.IntRange(0, 5).Draw(t, "numChanges")
	changes := make([]AdaptPlanChange, numChanges)
	for i := range changes {
		// Use only non-delete ops to keep generation simple.
		op := rapid.SampledFrom(validNonDeleteOps).Draw(t, "op")
		path := rapid.SampledFrom(validAllowedPaths).Draw(t, "changePath")
		changes[i] = AdaptPlanChange{
			Path:      path,
			Op:        op,
			Rationale: rapid.StringMatching(`[a-zA-Z][a-zA-Z0-9 ]{3,20}`).Draw(t, "rationale"),
		}
	}

	numSkipped := rapid.IntRange(0, 3).Draw(t, "numSkipped")
	skipped := make([]AdaptPlanSkipped, numSkipped)
	for i := range skipped {
		path := rapid.SampledFrom(validAllowedPaths).Draw(t, "skippedPath")
		skipped[i] = AdaptPlanSkipped{
			Path:   path,
			Reason: rapid.StringMatching(`[a-zA-Z][a-zA-Z0-9 ]{3,20}`).Draw(t, "reason"),
		}
	}

	numLangs := rapid.IntRange(0, 4).Draw(t, "numLangs")
	langs := make([]string, numLangs)
	for i := range langs {
		langs[i] = rapid.StringMatching(`[A-Z][a-z]+`).Draw(t, "lang")
	}

	return AdaptPlan{
		SchemaVersion: 1,
		Detected: AdaptPlanDetected{
			Languages:   langs,
			BuildTools:  []string{},
			TestRunners: []string{},
			Linters:     []string{},
			HasFrontend: rapid.Bool().Draw(t, "hasFrontend"),
			HasDatabase: rapid.Bool().Draw(t, "hasDatabase"),
			EntryPoints: []string{},
		},
		PlannedChanges: changes,
		Skipped:        skipped,
	}
}

func TestPropAdaptPlanRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir, err := os.MkdirTemp("", "adapt-plan-prop-*")
		if err != nil {
			rt.Fatalf("MkdirTemp: %v", err)
		}
		defer os.RemoveAll(dir)

		original := genValidAdaptPlan(rt)
		path := filepath.Join(dir, "adapt-plan.json")

		if err := WriteAdaptPlan(path, &original); err != nil {
			rt.Fatalf("WriteAdaptPlan: %v", err)
		}

		read, err := ReadAdaptPlan(path)
		if err != nil {
			rt.Fatalf("ReadAdaptPlan: %v", err)
		}

		if read.SchemaVersion != original.SchemaVersion {
			rt.Fatalf("SchemaVersion: got %d, want %d", read.SchemaVersion, original.SchemaVersion)
		}
		if read.Detected.HasFrontend != original.Detected.HasFrontend {
			rt.Fatalf("HasFrontend: got %v, want %v", read.Detected.HasFrontend, original.Detected.HasFrontend)
		}
		if read.Detected.HasDatabase != original.Detected.HasDatabase {
			rt.Fatalf("HasDatabase: got %v, want %v", read.Detected.HasDatabase, original.Detected.HasDatabase)
		}
		if len(read.PlannedChanges) != len(original.PlannedChanges) {
			rt.Fatalf("len(PlannedChanges): got %d, want %d", len(read.PlannedChanges), len(original.PlannedChanges))
		}
		for i, c := range original.PlannedChanges {
			rc := read.PlannedChanges[i]
			if rc.Path != c.Path {
				rt.Fatalf("PlannedChanges[%d].Path: got %q, want %q", i, rc.Path, c.Path)
			}
			if rc.Op != c.Op {
				rt.Fatalf("PlannedChanges[%d].Op: got %q, want %q", i, rc.Op, c.Op)
			}
			if rc.Rationale != c.Rationale {
				rt.Fatalf("PlannedChanges[%d].Rationale: got %q, want %q", i, rc.Rationale, c.Rationale)
			}
		}
		if len(read.Skipped) != len(original.Skipped) {
			rt.Fatalf("len(Skipped): got %d, want %d", len(read.Skipped), len(original.Skipped))
		}
		for i, s := range original.Skipped {
			rs := read.Skipped[i]
			if rs.Path != s.Path {
				rt.Fatalf("Skipped[%d].Path: got %q, want %q", i, rs.Path, s.Path)
			}
			if rs.Reason != s.Reason {
				rt.Fatalf("Skipped[%d].Reason: got %q, want %q", i, rs.Reason, s.Reason)
			}
		}
		if len(read.Detected.Languages) != len(original.Detected.Languages) {
			rt.Fatalf("len(Languages): got %d, want %d", len(read.Detected.Languages), len(original.Detected.Languages))
		}
		for i, lang := range original.Detected.Languages {
			if read.Detected.Languages[i] != lang {
				rt.Fatalf("Languages[%d]: got %q, want %q", i, read.Detected.Languages[i], lang)
			}
		}
	})
}

func TestPropValidateAcceptsAnyValidPlan(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		plan := genValidAdaptPlan(rt)
		if err := plan.Validate(); err != nil {
			rt.Fatalf("Validate() rejected valid plan: %v\nplan: %+v", err, plan)
		}
	})
}

func TestPropValidateRejectsAnySchemaVersionNot1(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		plan := genValidAdaptPlan(rt)
		// Draw any version except 1.
		v := rapid.IntRange(-10, 10).Draw(rt, "version")
		if v == 1 {
			v = 2
		}
		plan.SchemaVersion = v

		if err := plan.Validate(); err == nil {
			rt.Fatalf("Validate() accepted plan with schema_version=%d, want error", v)
		}
	})
}

func TestPropWriteReadIsStable(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir, err := os.MkdirTemp("", "adapt-plan-stable-*")
		if err != nil {
			rt.Fatalf("MkdirTemp: %v", err)
		}
		defer os.RemoveAll(dir)

		original := genValidAdaptPlan(rt)
		path := filepath.Join(dir, "adapt-plan.json")

		if err := WriteAdaptPlan(path, &original); err != nil {
			rt.Fatalf("WriteAdaptPlan first: %v", err)
		}

		first, err := ReadAdaptPlan(path)
		if err != nil {
			rt.Fatalf("ReadAdaptPlan first: %v", err)
		}

		// Write again using the read result.
		if err := WriteAdaptPlan(path, first); err != nil {
			rt.Fatalf("WriteAdaptPlan second: %v", err)
		}

		second, err := ReadAdaptPlan(path)
		if err != nil {
			rt.Fatalf("ReadAdaptPlan second: %v", err)
		}

		// Plans should remain identical across write-read cycles.
		if first.SchemaVersion != second.SchemaVersion {
			rt.Fatalf("SchemaVersion changed: first=%d second=%d", first.SchemaVersion, second.SchemaVersion)
		}
		if first.Detected.HasFrontend != second.Detected.HasFrontend {
			rt.Fatalf("HasFrontend changed: first=%v second=%v", first.Detected.HasFrontend, second.Detected.HasFrontend)
		}
		if len(first.PlannedChanges) != len(second.PlannedChanges) {
			rt.Fatalf("PlannedChanges count changed: first=%d second=%d", len(first.PlannedChanges), len(second.PlannedChanges))
		}
		for i := range first.PlannedChanges {
			fc, sc := first.PlannedChanges[i], second.PlannedChanges[i]
			if fc.Path != sc.Path {
				rt.Fatalf("PlannedChanges[%d].Path changed: first=%q second=%q", i, fc.Path, sc.Path)
			}
			if fc.Op != sc.Op {
				rt.Fatalf("PlannedChanges[%d].Op changed: first=%q second=%q", i, fc.Op, sc.Op)
			}
			if fc.Rationale != sc.Rationale {
				rt.Fatalf("PlannedChanges[%d].Rationale changed: first=%q second=%q", i, fc.Rationale, sc.Rationale)
			}
		}
		if len(first.Skipped) != len(second.Skipped) {
			rt.Fatalf("Skipped count changed: first=%d second=%d", len(first.Skipped), len(second.Skipped))
		}
		for i := range first.Skipped {
			fs, ss := first.Skipped[i], second.Skipped[i]
			if fs.Path != ss.Path {
				rt.Fatalf("Skipped[%d].Path changed: first=%q second=%q", i, fs.Path, ss.Path)
			}
			if fs.Reason != ss.Reason {
				rt.Fatalf("Skipped[%d].Reason changed: first=%q second=%q", i, fs.Reason, ss.Reason)
			}
		}
	})
}

func TestPropNormalizePathIsIdempotent(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate simple relative paths that look plausible.
		path := rapid.SampledFrom([]string{
			".xylem.yml",
			".xylem/workflows/fix-bug.yaml",
			"docs/README.md",
			"AGENTS.md",
			".xylem/prompts/analyze/prompt.md",
		}).Draw(rt, "path")

		first, err := normalizeAdaptPlanPath(path)
		if err != nil {
			// path itself might already be a bad one in some generators; skip
			return
		}

		second, err := normalizeAdaptPlanPath(first)
		if err != nil {
			rt.Fatalf("normalizeAdaptPlanPath(%q) failed on already-normalized path: %v", first, err)
		}

		if first != second {
			rt.Fatalf("normalizeAdaptPlanPath not idempotent: %q → %q → %q", path, first, second)
		}
	})
}

func TestPropDeleteOnlyAllowedForWorkflowYAML(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// A delete on any non-workflow path must be rejected.
		nonWorkflowPaths := []string{
			".xylem.yml",
			"AGENTS.md",
			"docs/README.md",
			".xylem/HARNESS.md",
			".xylem/prompts/analyze/prompt.md",
		}
		path := rapid.SampledFrom(nonWorkflowPaths).Draw(rt, "path")

		plan := AdaptPlan{
			SchemaVersion: 1,
			Detected:      AdaptPlanDetected{},
			PlannedChanges: []AdaptPlanChange{
				{Path: path, Op: "delete", Rationale: "test delete"},
			},
			Skipped: []AdaptPlanSkipped{},
		}

		if err := plan.Validate(); err == nil {
			rt.Fatalf("Validate() accepted delete for non-workflow path %q, want error", path)
		}
	})
}

func TestPropDeleteWorkflowPathsAlwaysAccepted(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		path := rapid.SampledFrom(validWorkflowPaths).Draw(rt, "workflowPath")

		plan := AdaptPlan{
			SchemaVersion: 1,
			Detected:      AdaptPlanDetected{},
			PlannedChanges: []AdaptPlanChange{
				{Path: path, Op: "delete", Rationale: "remove workflow"},
			},
			Skipped: []AdaptPlanSkipped{},
		}

		if err := plan.Validate(); err != nil {
			rt.Fatalf("Validate() rejected delete for workflow path %q: %v", path, err)
		}
	})
}
