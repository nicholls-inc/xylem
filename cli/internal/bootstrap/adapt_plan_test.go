package bootstrap

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAdaptPlanJSON writes a JSON body to adapt-plan.json in dir and returns the path.
func writeAdaptPlanJSON(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "adapt-plan.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile adapt-plan.json: %v", err)
	}
	return path
}

func minimalValidPlanJSON() string {
	return `{
  "schema_version": 1,
  "detected": {
    "languages": [],
    "build_tools": [],
    "test_runners": [],
    "linters": [],
    "has_frontend": false,
    "has_database": false,
    "entry_points": []
  },
  "planned_changes": [],
  "skipped": []
}`
}

func minimalValidPlan() AdaptPlan {
	return AdaptPlan{
		SchemaVersion: 1,
		Detected: AdaptPlanDetected{
			Languages:   []string{},
			BuildTools:  []string{},
			TestRunners: []string{},
			Linters:     []string{},
			EntryPoints: []string{},
		},
		PlannedChanges: []AdaptPlanChange{},
		Skipped:        []AdaptPlanSkipped{},
	}
}

func TestReadAdaptPlan_ValidMinimal(t *testing.T) {
	dir := t.TempDir()
	path := writeAdaptPlanJSON(t, dir, minimalValidPlanJSON())

	plan, err := ReadAdaptPlan(path)
	if err != nil {
		t.Fatalf("ReadAdaptPlan() unexpected error: %v", err)
	}
	if plan.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", plan.SchemaVersion)
	}
	if plan.PlannedChanges == nil {
		t.Error("PlannedChanges is nil, want empty slice")
	}
	if plan.Skipped == nil {
		t.Error("Skipped is nil, want empty slice")
	}
}

func TestReadAdaptPlan_ValidWithChanges(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "schema_version": 1,
  "detected": {
    "languages": ["Go"],
    "build_tools": ["make"],
    "test_runners": ["go test"],
    "linters": ["golangci-lint"],
    "has_frontend": false,
    "has_database": true,
    "entry_points": ["cmd/main"]
  },
  "planned_changes": [
    {"path": ".xylem.yml", "op": "patch", "rationale": "set build command"},
    {"path": "AGENTS.md", "op": "replace", "rationale": "update agent docs"},
    {"path": ".xylem/prompts/analyze/prompt.md", "op": "create", "rationale": "add prompt"},
    {"path": ".xylem/workflows/fix-bug.yaml", "op": "delete", "rationale": "remove unused workflow"}
  ],
  "skipped": [
    {"path": "docs/README.md", "reason": "manual content"}
  ]
}`
	path := writeAdaptPlanJSON(t, dir, body)

	plan, err := ReadAdaptPlan(path)
	if err != nil {
		t.Fatalf("ReadAdaptPlan() unexpected error: %v", err)
	}
	if len(plan.PlannedChanges) != 4 {
		t.Errorf("len(PlannedChanges) = %d, want 4", len(plan.PlannedChanges))
	}
	if len(plan.Skipped) != 1 {
		t.Errorf("len(Skipped) = %d, want 1", len(plan.Skipped))
	}
}

func TestReadAdaptPlan_RejectsSchemaVersionZero(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(minimalValidPlanJSON(), `"schema_version": 1`, `"schema_version": 0`, 1)
	path := writeAdaptPlanJSON(t, dir, body)

	_, err := ReadAdaptPlan(path)
	if err == nil {
		t.Fatal("ReadAdaptPlan() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("error %q does not mention schema_version", err.Error())
	}
}

func TestReadAdaptPlan_RejectsMissingSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "detected": {"languages":[],"build_tools":[],"test_runners":[],"linters":[],"has_frontend":false,"has_database":false,"entry_points":[]},
  "planned_changes": [],
  "skipped": []
}`
	path := writeAdaptPlanJSON(t, dir, body)

	_, err := ReadAdaptPlan(path)
	if err == nil {
		t.Fatal("ReadAdaptPlan() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("error %q does not mention schema_version", err.Error())
	}
}

func TestReadAdaptPlan_RejectsMissingDetected(t *testing.T) {
	dir := t.TempDir()
	body := `{"schema_version": 1, "planned_changes": [], "skipped": []}`
	path := writeAdaptPlanJSON(t, dir, body)

	_, err := ReadAdaptPlan(path)
	if err == nil {
		t.Fatal("ReadAdaptPlan() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "detected") {
		t.Errorf("error %q does not mention detected", err.Error())
	}
}

func TestReadAdaptPlan_RejectsMissingPlannedChanges(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "schema_version": 1,
  "detected": {"languages":[],"build_tools":[],"test_runners":[],"linters":[],"has_frontend":false,"has_database":false,"entry_points":[]},
  "skipped": []
}`
	path := writeAdaptPlanJSON(t, dir, body)

	_, err := ReadAdaptPlan(path)
	if err == nil {
		t.Fatal("ReadAdaptPlan() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "planned_changes") {
		t.Errorf("error %q does not mention planned_changes", err.Error())
	}
}

func TestReadAdaptPlan_RejectsNullSkipped(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "schema_version": 1,
  "detected": {"languages":[],"build_tools":[],"test_runners":[],"linters":[],"has_frontend":false,"has_database":false,"entry_points":[]},
  "planned_changes": [],
  "skipped": null
}`
	path := writeAdaptPlanJSON(t, dir, body)

	_, err := ReadAdaptPlan(path)
	if err == nil {
		t.Fatal("ReadAdaptPlan() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "skipped") {
		t.Errorf("error %q does not mention skipped", err.Error())
	}
}

func TestReadAdaptPlan_RejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(minimalValidPlanJSON(), `"skipped": []`, `"skipped": [], "unexpected_key": true`, 1)
	path := writeAdaptPlanJSON(t, dir, body)

	_, err := ReadAdaptPlan(path)
	if err == nil {
		t.Fatal("ReadAdaptPlan() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("error %q does not mention unknown field", err.Error())
	}
}

func TestReadAdaptPlan_RejectsTrailingData(t *testing.T) {
	dir := t.TempDir()
	body := minimalValidPlanJSON() + "\n" + minimalValidPlanJSON()
	path := writeAdaptPlanJSON(t, dir, body)

	_, err := ReadAdaptPlan(path)
	if err == nil {
		t.Fatal("ReadAdaptPlan() expected error for trailing data, got nil")
	}
	if !strings.Contains(err.Error(), "trailing") {
		t.Errorf("error %q does not mention trailing", err.Error())
	}
}

func TestReadAdaptPlan_RejectsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "schema_version": 1,
  "detected": {"languages":[],"build_tools":[],"test_runners":[],"linters":[],"has_frontend":false,"has_database":false,"entry_points":[]},
  "planned_changes": [{"path": "/etc/passwd", "op": "patch", "rationale": "bad"}],
  "skipped": []
}`
	path := writeAdaptPlanJSON(t, dir, body)

	_, err := ReadAdaptPlan(path)
	if err == nil {
		t.Fatal("ReadAdaptPlan() expected error for absolute path, got nil")
	}
	if !strings.Contains(err.Error(), "relative") {
		t.Errorf("error %q does not mention relative", err.Error())
	}
}

func TestReadAdaptPlan_RejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "schema_version": 1,
  "detected": {"languages":[],"build_tools":[],"test_runners":[],"linters":[],"has_frontend":false,"has_database":false,"entry_points":[]},
  "planned_changes": [{"path": "../secret", "op": "patch", "rationale": "escape"}],
  "skipped": []
}`
	path := writeAdaptPlanJSON(t, dir, body)

	_, err := ReadAdaptPlan(path)
	if err == nil {
		t.Fatal("ReadAdaptPlan() expected error for path escape, got nil")
	}
	if !strings.Contains(err.Error(), "repository root") {
		t.Errorf("error %q does not mention repository root", err.Error())
	}
}

func TestReadAdaptPlan_RejectsDisallowedPath(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "schema_version": 1,
  "detected": {"languages":[],"build_tools":[],"test_runners":[],"linters":[],"has_frontend":false,"has_database":false,"entry_points":[]},
  "planned_changes": [{"path": "internal/secret.go", "op": "patch", "rationale": "not allowed"}],
  "skipped": []
}`
	path := writeAdaptPlanJSON(t, dir, body)

	_, err := ReadAdaptPlan(path)
	if err == nil {
		t.Fatal("ReadAdaptPlan() expected error for disallowed path, got nil")
	}
	if !strings.Contains(err.Error(), "allowlist") {
		t.Errorf("error %q does not mention allowlist", err.Error())
	}
}

func TestReadAdaptPlan_RejectsDeleteOutsideWorkflows(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "schema_version": 1,
  "detected": {"languages":[],"build_tools":[],"test_runners":[],"linters":[],"has_frontend":false,"has_database":false,"entry_points":[]},
  "planned_changes": [{"path": ".xylem.yml", "op": "delete", "rationale": "bad delete"}],
  "skipped": []
}`
	path := writeAdaptPlanJSON(t, dir, body)

	_, err := ReadAdaptPlan(path)
	if err == nil {
		t.Fatal("ReadAdaptPlan() expected error for delete outside workflows, got nil")
	}
	if !strings.Contains(err.Error(), ".xylem/workflows/") {
		t.Errorf("error %q does not mention .xylem/workflows/", err.Error())
	}
}

func TestReadAdaptPlan_RejectsDeleteNonWorkflowFile(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "schema_version": 1,
  "detected": {"languages":[],"build_tools":[],"test_runners":[],"linters":[],"has_frontend":false,"has_database":false,"entry_points":[]},
  "planned_changes": [{"path": ".xylem/workflows/data.json", "op": "delete", "rationale": "not yaml"}],
  "skipped": []
}`
	path := writeAdaptPlanJSON(t, dir, body)

	_, err := ReadAdaptPlan(path)
	if err == nil {
		t.Fatal("ReadAdaptPlan() expected error for delete of non-yaml workflow file, got nil")
	}
	if !strings.Contains(err.Error(), ".xylem/workflows/") {
		t.Errorf("error %q does not mention .xylem/workflows/", err.Error())
	}
}

func TestReadAdaptPlan_RejectsEmptyRationale(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "schema_version": 1,
  "detected": {"languages":[],"build_tools":[],"test_runners":[],"linters":[],"has_frontend":false,"has_database":false,"entry_points":[]},
  "planned_changes": [{"path": ".xylem.yml", "op": "patch", "rationale": ""}],
  "skipped": []
}`
	path := writeAdaptPlanJSON(t, dir, body)

	_, err := ReadAdaptPlan(path)
	if err == nil {
		t.Fatal("ReadAdaptPlan() expected error for empty rationale, got nil")
	}
	if !strings.Contains(err.Error(), "rationale") {
		t.Errorf("error %q does not mention rationale", err.Error())
	}
}

func TestReadAdaptPlan_RejectsEmptySkippedReason(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "schema_version": 1,
  "detected": {"languages":[],"build_tools":[],"test_runners":[],"linters":[],"has_frontend":false,"has_database":false,"entry_points":[]},
  "planned_changes": [],
  "skipped": [{"path": "docs/README.md", "reason": ""}]
}`
	path := writeAdaptPlanJSON(t, dir, body)

	_, err := ReadAdaptPlan(path)
	if err == nil {
		t.Fatal("ReadAdaptPlan() expected error for empty skip reason, got nil")
	}
	if !strings.Contains(err.Error(), "reason") {
		t.Errorf("error %q does not mention reason", err.Error())
	}
}

func TestReadAdaptPlan_RejectsEmptyPath(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "schema_version": 1,
  "detected": {"languages":[],"build_tools":[],"test_runners":[],"linters":[],"has_frontend":false,"has_database":false,"entry_points":[]},
  "planned_changes": [{"path": "", "op": "patch", "rationale": "empty path"}],
  "skipped": []
}`
	path := writeAdaptPlanJSON(t, dir, body)

	_, err := ReadAdaptPlan(path)
	if err == nil {
		t.Fatal("ReadAdaptPlan() expected error for empty path, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error %q does not mention empty", err.Error())
	}
}

func TestReadAdaptPlan_FileNotFound(t *testing.T) {
	_, err := ReadAdaptPlan("/nonexistent/path/adapt-plan.json")
	if err == nil {
		t.Fatal("ReadAdaptPlan() expected error for missing file, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadAdaptPlan() error %v does not wrap os.ErrNotExist", err)
	}
}

func TestWriteAdaptPlan_CreatesFileAndParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deep", "nested", "adapt-plan.json")
	plan := minimalValidPlan()

	if err := WriteAdaptPlan(path, &plan); err != nil {
		t.Fatalf("WriteAdaptPlan() error: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created at %q: %v", path, err)
	}
}

func TestWriteAdaptPlan_OutputIsValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adapt-plan.json")
	plan := minimalValidPlan()

	if err := WriteAdaptPlan(path, &plan); err != nil {
		t.Fatalf("WriteAdaptPlan() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Errorf("written file is not valid JSON: %v", err)
	}
	// Required top-level keys must be present.
	for _, key := range []string{"schema_version", "detected", "planned_changes", "skipped"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("written JSON missing required key %q", key)
		}
	}
	if v, ok := decoded["schema_version"].(float64); !ok || v != 1 {
		t.Errorf("schema_version = %v, want 1", decoded["schema_version"])
	}
}

func TestAdaptPlan_ValidateDeleteWorkflowYAML(t *testing.T) {
	plan := AdaptPlan{
		SchemaVersion: 1,
		Detected:      AdaptPlanDetected{},
		PlannedChanges: []AdaptPlanChange{
			{Path: ".xylem/workflows/fix-bug.yaml", Op: "delete", Rationale: "remove"},
		},
		Skipped: []AdaptPlanSkipped{},
	}
	if err := plan.Validate(); err != nil {
		t.Errorf("Validate() unexpected error for .yaml delete: %v", err)
	}
}

func TestAdaptPlan_ValidateDeleteWorkflowYML(t *testing.T) {
	plan := AdaptPlan{
		SchemaVersion: 1,
		Detected:      AdaptPlanDetected{},
		PlannedChanges: []AdaptPlanChange{
			{Path: ".xylem/workflows/fix-bug.yml", Op: "delete", Rationale: "remove"},
		},
		Skipped: []AdaptPlanSkipped{},
	}
	if err := plan.Validate(); err != nil {
		t.Errorf("Validate() unexpected error for .yml delete: %v", err)
	}
}

func TestAdaptPlan_ValidateAllowedPaths(t *testing.T) {
	paths := []struct {
		name string
		path string
		op   string
	}{
		{"xylem-yml", ".xylem.yml", "patch"},
		{"agents-md", "AGENTS.md", "patch"},
		{"xylem-prompts", ".xylem/prompts/analyze/prompt.md", "create"},
		{"docs", "docs/getting-started.md", "replace"},
	}
	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			plan := AdaptPlan{
				SchemaVersion: 1,
				Detected:      AdaptPlanDetected{},
				PlannedChanges: []AdaptPlanChange{
					{Path: tc.path, Op: tc.op, Rationale: "test"},
				},
				Skipped: []AdaptPlanSkipped{},
			}
			if err := plan.Validate(); err != nil {
				t.Errorf("Validate() unexpected error for path %q: %v", tc.path, err)
			}
		})
	}
}

func TestIsAllowedAdaptPlanPath(t *testing.T) {
	tests := []struct {
		path    string
		allowed bool
	}{
		{".xylem.yml", true},
		{"AGENTS.md", true},
		{".xylem/workflows/fix-bug.yaml", true},
		{".xylem/prompts/analyze/prompt.md", true},
		{".xylem/HARNESS.md", true},
		{"docs/README.md", true},
		{"docs/adr/0001.md", true},
		{"internal/pkg/foo.go", false},
		{"cmd/main.go", false},
		{"go.mod", false},
		{"README.md", false},
		{".github/workflows/ci.yml", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			got := IsAllowedAdaptPlanPath(tc.path)
			if got != tc.allowed {
				t.Errorf("IsAllowedAdaptPlanPath(%q) = %v, want %v", tc.path, got, tc.allowed)
			}
		})
	}
}

func TestReadAdaptPlan_AcceptsOutOfAllowlistSkipped(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "schema_version": 1,
  "detected": {"languages":[],"build_tools":[],"test_runners":[],"linters":[],"has_frontend":false,"has_database":false,"entry_points":[]},
  "planned_changes": [],
  "skipped": [
    {"path": "CHANGELOG.md", "reason": "not in scope"},
    {"path": "Makefile", "reason": "out of scope"}
  ]
}`
	path := writeAdaptPlanJSON(t, dir, body)

	plan, err := ReadAdaptPlan(path)
	if err != nil {
		t.Fatalf("ReadAdaptPlan() unexpected error for out-of-allowlist skipped paths: %v", err)
	}
	if len(plan.Skipped) != 2 {
		t.Errorf("len(Skipped) = %d, want 2", len(plan.Skipped))
	}
}

func TestValidate_AcceptsOutOfAllowlistSkipped(t *testing.T) {
	plan := AdaptPlan{
		SchemaVersion:  1,
		Detected:       AdaptPlanDetected{},
		PlannedChanges: []AdaptPlanChange{},
		Skipped: []AdaptPlanSkipped{
			{Path: "Makefile", Reason: "out of scope"},
		},
	}
	if err := plan.Validate(); err != nil {
		t.Errorf("Validate() unexpected error for out-of-allowlist skipped path: %v", err)
	}
}

func TestAdaptPlanSchema_IsEmbedded(t *testing.T) {
	if len(AdaptPlanSchema) == 0 {
		t.Fatal("AdaptPlanSchema is empty; embed may have failed")
	}
	var schema map[string]interface{}
	if err := json.Unmarshal(AdaptPlanSchema, &schema); err != nil {
		t.Fatalf("AdaptPlanSchema is not valid JSON: %v", err)
	}
	if schema["$schema"] == nil {
		t.Error("AdaptPlanSchema missing $schema field")
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adapt-plan.json")

	original := AdaptPlan{
		SchemaVersion: 1,
		Detected: AdaptPlanDetected{
			Languages:   []string{"Go", "TypeScript"},
			BuildTools:  []string{"make"},
			TestRunners: []string{"go test"},
			Linters:     []string{"golangci-lint"},
			HasFrontend: true,
			HasDatabase: false,
			EntryPoints: []string{"cmd/server"},
		},
		PlannedChanges: []AdaptPlanChange{
			{Path: ".xylem.yml", Op: "patch", Rationale: "update build", DiffSummary: "validation.build: make build"},
			{Path: "docs/setup.md", Op: "create", Rationale: "add docs"},
		},
		Skipped: []AdaptPlanSkipped{
			{Path: "CHANGELOG.md", Reason: "already configured"},
		},
	}

	if err := WriteAdaptPlan(path, &original); err != nil {
		t.Fatalf("WriteAdaptPlan() error: %v", err)
	}

	read, err := ReadAdaptPlan(path)
	if err != nil {
		t.Fatalf("ReadAdaptPlan() error: %v", err)
	}

	if read.SchemaVersion != original.SchemaVersion {
		t.Errorf("SchemaVersion: got %d, want %d", read.SchemaVersion, original.SchemaVersion)
	}
	if read.Detected.HasFrontend != original.Detected.HasFrontend {
		t.Errorf("Detected.HasFrontend: got %v, want %v", read.Detected.HasFrontend, original.Detected.HasFrontend)
	}
	if len(read.Detected.Languages) != len(original.Detected.Languages) {
		t.Errorf("Detected.Languages length: got %d, want %d", len(read.Detected.Languages), len(original.Detected.Languages))
	} else {
		for i, lang := range original.Detected.Languages {
			if read.Detected.Languages[i] != lang {
				t.Errorf("Detected.Languages[%d]: got %q, want %q", i, read.Detected.Languages[i], lang)
			}
		}
	}
	if len(read.PlannedChanges) != len(original.PlannedChanges) {
		t.Errorf("len(PlannedChanges): got %d, want %d", len(read.PlannedChanges), len(original.PlannedChanges))
	} else {
		for i, c := range original.PlannedChanges {
			rc := read.PlannedChanges[i]
			if rc.Path != c.Path {
				t.Errorf("PlannedChanges[%d].Path: got %q, want %q", i, rc.Path, c.Path)
			}
			if rc.Op != c.Op {
				t.Errorf("PlannedChanges[%d].Op: got %q, want %q", i, rc.Op, c.Op)
			}
			if rc.Rationale != c.Rationale {
				t.Errorf("PlannedChanges[%d].Rationale: got %q, want %q", i, rc.Rationale, c.Rationale)
			}
			if rc.DiffSummary != c.DiffSummary {
				t.Errorf("PlannedChanges[%d].DiffSummary: got %q, want %q", i, rc.DiffSummary, c.DiffSummary)
			}
		}
	}
	if len(read.Skipped) != len(original.Skipped) {
		t.Errorf("len(Skipped): got %d, want %d", len(read.Skipped), len(original.Skipped))
	} else {
		for i, s := range original.Skipped {
			rs := read.Skipped[i]
			if rs.Path != s.Path {
				t.Errorf("Skipped[%d].Path: got %q, want %q", i, rs.Path, s.Path)
			}
			if rs.Reason != s.Reason {
				t.Errorf("Skipped[%d].Reason: got %q, want %q", i, rs.Reason, s.Reason)
			}
		}
	}
}
