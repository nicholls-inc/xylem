package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	toml "github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestInitCreatesConfigAndStateDir(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")
	stateDir := filepath.Join(dir, ".xylem")

	// Temporarily change to temp dir so defaultStateDir resolves there
	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	captureStdout(func() {
		err := cmdInit(configPath, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// Config file created
	if _, err := os.Stat(configPath); err != nil {
		t.Errorf("config file not created: %v", err)
	}

	// State directory created
	if _, err := os.Stat(stateDir); err != nil {
		t.Errorf("state dir not created: %v", err)
	}

	// .gitignore created
	gitignore := filepath.Join(stateDir, ".gitignore")
	data, err := os.ReadFile(gitignore)
	if err != nil {
		t.Fatalf("gitignore not created: %v", err)
	}
	if string(data) != "*\n!.gitignore\n" {
		t.Errorf("unexpected gitignore content: %q", string(data))
	}

}

func TestInitIdempotentWithoutForce(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	// Write existing config
	existing := "existing: true\n"
	os.WriteFile(configPath, []byte(existing), 0o644) //nolint:errcheck

	out := captureStdout(func() {
		err := cmdInit(configPath, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// Config preserved
	data, _ := os.ReadFile(configPath)
	if string(data) != existing {
		t.Errorf("config was overwritten, got: %s", string(data))
	}

	if !strings.Contains(out, "already exists") {
		t.Errorf("expected 'already exists' message, got: %s", out)
	}

	// State dir still created
	stateDir := filepath.Join(dir, ".xylem")
	if _, err := os.Stat(stateDir); err != nil {
		t.Errorf("state dir not created: %v", err)
	}
}

func TestInitForceOverwritesConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	// Write existing config
	os.WriteFile(configPath, []byte("old: true\n"), 0o644) //nolint:errcheck

	out := captureStdout(func() {
		err := cmdInit(configPath, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	data, _ := os.ReadFile(configPath)
	if strings.Contains(string(data), "old: true") {
		t.Errorf("config was not overwritten")
	}
	if !strings.Contains(string(data), "sources:") {
		t.Errorf("expected scaffold config, got: %s", string(data))
	}

	if !strings.Contains(out, "Created") {
		t.Errorf("expected 'Created' in output, got: %s", out)
	}
}

func TestInitStateDirAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")
	stateDir := filepath.Join(dir, ".xylem")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	// Pre-create state dir with a file
	os.MkdirAll(stateDir, 0o755)                                                    //nolint:errcheck
	os.WriteFile(filepath.Join(stateDir, "queue.jsonl"), []byte("existing"), 0o644) //nolint:errcheck

	err := cmdInit(configPath, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Existing file preserved
	data, _ := os.ReadFile(filepath.Join(stateDir, "queue.jsonl"))
	if string(data) != "existing" {
		t.Errorf("existing file in state dir was modified")
	}
}

func TestParseGitHubRepo(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"SSH", "git@github.com:owner/repo.git", "owner/repo"},
		{"SSH no .git", "git@github.com:owner/repo", "owner/repo"},
		{"HTTPS", "https://github.com/owner/repo.git", "owner/repo"},
		{"HTTPS no .git", "https://github.com/owner/repo", "owner/repo"},
		{"ssh protocol", "ssh://git@github.com/owner/repo.git", "owner/repo"},
		{"ssh protocol no .git", "ssh://git@github.com/owner/repo", "owner/repo"},
		{"non-GitHub SSH", "git@gitlab.com:owner/repo.git", ""},
		{"non-GitHub HTTPS", "https://gitlab.com/owner/repo.git", ""},
		{"malformed", "not-a-url", ""},
		{"empty", "", ""},
		{"with trailing newline", "git@github.com:owner/repo.git\n", "owner/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseGitHubRepo(tt.input)
			if got != tt.expected {
				t.Errorf("parseGitHubRepo(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestInitScaffoldConfigLoads(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	captureStdout(func() {
		if err := cmdInit(configPath, false); err != nil {
			t.Fatalf("cmdInit failed: %v", err)
		}
	})

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("scaffold config failed to load: %v", err)
	}
	if len(cfg.Sources) == 0 {
		t.Error("expected at least one source in scaffold config")
	}
}

func TestInitRespectsConfigFlag(t *testing.T) {
	dir := t.TempDir()
	customPath := filepath.Join(dir, "custom.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--config", customPath, "init"})

	captureStdout(func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("init with --config failed: %v", err)
		}
	})

	if _, err := os.Stat(customPath); err != nil {
		t.Fatalf("custom config not created at %s: %v", customPath, err)
	}

	// Default path should NOT exist
	if _, err := os.Stat(filepath.Join(dir, ".xylem.yml")); err == nil {
		t.Error(".xylem.yml was created despite --config pointing elsewhere")
	}
}

func TestInitCreatesV2Files(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	captureStdout(func() {
		err := cmdInit(configPath, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	expectedFiles := []string{
		".xylem/HARNESS.md",
		".xylem/workflows/fix-bug.yaml",
		".xylem/workflows/implement-feature.yaml",
		".xylem/prompts/fix-bug/analyze.md",
		".xylem/prompts/fix-bug/plan.md",
		".xylem/prompts/fix-bug/implement.md",
		".xylem/prompts/fix-bug/pr.md",
		".xylem/prompts/implement-feature/analyze.md",
		".xylem/prompts/implement-feature/plan.md",
		".xylem/prompts/implement-feature/implement.md",
		".xylem/prompts/implement-feature/pr.md",
	}
	for _, f := range expectedFiles {
		path := filepath.Join(dir, f)
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			t.Errorf("expected file %s to exist", f)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("expected file %s to be non-empty", f)
		}
	}
}

func TestInitCreatesEvalScaffold(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	captureStdout(func() {
		if err := cmdInit(configPath, false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	required := []string{
		".xylem/eval/harbor.yaml",
		".xylem/eval/helpers/xylem_verify.py",
		".xylem/eval/helpers/conftest.py",
		".xylem/eval/calibration/plan_quality/calibration.json",
		".xylem/eval/calibration/plan_quality/strong-fix-plan.md",
		".xylem/eval/calibration/plan_quality/scope-drift-plan.md",
		".xylem/eval/rubrics/plan_quality.toml",
		".xylem/eval/rubrics/evidence_quality.toml",
		".xylem/eval/scenarios/fix-simple-null-pointer/task.toml",
		".xylem/eval/scenarios/fix-simple-null-pointer/tests/conftest.py",
		".xylem/eval/scenarios/fix-simple-null-pointer/tests/test.sh",
		".xylem/eval/scenarios/modify-harness-md/tests/test_verification.py",
		".xylem/eval/scenarios/modify-harness-md/tests/conftest.py",
		".xylem/eval/scenarios/label-gate-resume/task.toml",
		".xylem/eval/scenarios/label-gate-resume/tests/conftest.py",
		".xylem/eval/scenarios/gate-retry-then-pass/task.toml",
		".xylem/eval/scenarios/gate-retry-then-pass/tests/conftest.py",
		".xylem/eval/scenarios/pr-reporting-path/task.toml",
		".xylem/eval/scenarios/pr-reporting-path/tests/conftest.py",
	}
	for _, rel := range required {
		path := filepath.Join(dir, rel)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}

	info, err := os.Stat(filepath.Join(dir, ".xylem/eval/scenarios/fix-simple-null-pointer/tests/test.sh"))
	if err != nil {
		t.Fatalf("stat eval test.sh: %v", err)
	}
	if info.Mode()&0o100 == 0 {
		t.Fatalf("expected eval test.sh to be executable, mode=%v", info.Mode())
	}
}

func TestInitSkipsExistingV2Files(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	// Pre-create HARNESS.md with custom content
	os.MkdirAll(filepath.Join(dir, ".xylem"), 0o755) //nolint:errcheck
	custom := "# My custom harness\n"
	os.WriteFile(filepath.Join(dir, ".xylem", "HARNESS.md"), []byte(custom), 0o644) //nolint:errcheck

	out := captureStdout(func() {
		err := cmdInit(configPath, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// HARNESS.md should be preserved
	data, _ := os.ReadFile(filepath.Join(dir, ".xylem", "HARNESS.md"))
	if string(data) != custom {
		t.Errorf("HARNESS.md was overwritten, got: %s", string(data))
	}

	if !strings.Contains(out, "skipped") {
		t.Errorf("expected 'skipped' message for existing file, got: %s", out)
	}

	// But other files should still be created
	if _, err := os.Stat(filepath.Join(dir, ".xylem", "workflows", "fix-bug.yaml")); os.IsNotExist(err) {
		t.Error("expected fix-bug.yaml to be created")
	}
}

func TestInitForceOverwritesV2Files(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	// Pre-create HARNESS.md with custom content
	os.MkdirAll(filepath.Join(dir, ".xylem"), 0o755)                                  //nolint:errcheck
	os.WriteFile(filepath.Join(dir, ".xylem", "HARNESS.md"), []byte("custom"), 0o644) //nolint:errcheck

	captureStdout(func() {
		err := cmdInit(configPath, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// HARNESS.md should now have the default content
	data, _ := os.ReadFile(filepath.Join(dir, ".xylem", "HARNESS.md"))
	if string(data) == "custom" {
		t.Error("HARNESS.md was not overwritten with force=true")
	}
	if !strings.Contains(string(data), "Project Overview") {
		t.Error("expected default HARNESS.md content after force overwrite")
	}
}

func TestInitScaffoldConfigV2Format(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".xylem.yml")

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	captureStdout(func() {
		err := cmdInit(configPath, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "flags:") {
		t.Error("expected v2 config to contain 'flags:' field")
	}
	if !strings.Contains(content, "env:") {
		t.Error("expected v2 config to contain 'env:' section")
	}
	if strings.Contains(content, "template:") {
		t.Error("expected v2 config to NOT contain 'template:' field")
	}
}

func TestInitCobraBypassesPersistentPreRunE(t *testing.T) {
	dir := t.TempDir()

	orig, _ := os.Getwd()
	os.Chdir(dir)                        //nolint:errcheck
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	// Negative control: status (non-init) should fail without a config file
	statusCmd := newRootCmd()
	statusCmd.SetArgs([]string{"status"})
	captureStdout(func() {
		if err := statusCmd.Execute(); err == nil {
			t.Fatal("status should fail without config via PersistentPreRunE")
		}
	})

	// init should succeed in the same condition (no config file)
	initCmd := newRootCmd()
	initCmd.SetArgs([]string{"init"})
	captureStdout(func() {
		if err := initCmd.Execute(); err != nil {
			t.Fatalf("init should bypass PersistentPreRunE: %v", err)
		}
	})
}

type smokeHarborConfig struct {
	Agent             string  `yaml:"agent"`
	Model             string  `yaml:"model"`
	Path              string  `yaml:"path"`
	NAttempts         int     `yaml:"n_attempts"`
	NConcurrent       int     `yaml:"n_concurrent"`
	TimeoutMultiplier float64 `yaml:"timeout_multiplier"`
}

type smokeTaskFile struct {
	Task struct {
		ID          string `toml:"id"`
		Version     string `toml:"version"`
		Environment struct {
			TimeoutSeconds int `toml:"timeout_seconds"`
		} `toml:"environment"`
		Metadata struct {
			Category   string   `toml:"category"`
			Tags       []string `toml:"tags"`
			Difficulty string   `toml:"difficulty"`
			Canary     string   `toml:"canary"`
		} `toml:"metadata"`
	} `toml:"task"`
}

type smokeRubricFile struct {
	Rubric struct {
		Name        string `toml:"name"`
		Description string `toml:"description"`
		Criteria    []struct {
			Name        string  `toml:"name"`
			Description string  `toml:"description"`
			Weight      float64 `toml:"weight"`
		} `toml:"criteria"`
	} `toml:"rubric"`
}

type smokeCalibrationFile struct {
	Rubric        string  `json:"rubric"`
	PassThreshold float64 `json:"pass_threshold"`
	Criteria      []struct {
		Name        string  `json:"name"`
		Description string  `json:"description"`
		Weight      float64 `json:"weight"`
		Threshold   float64 `json:"threshold"`
	} `json:"criteria"`
	Examples []struct {
		ID         string             `json:"id"`
		Judgment   string             `json:"judgment"`
		OutputFile string             `json:"output_file"`
		Criteria   map[string]float64 `json:"criteria"`
		Notes      string             `json:"notes"`
	} `json:"examples"`
}

func repoRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller should resolve init_test.go")

	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func repoPath(t *testing.T, elems ...string) string {
	t.Helper()

	parts := append([]string{repoRoot(t)}, elems...)
	return filepath.Join(parts...)
}

func readRepoFile(t *testing.T, elems ...string) []byte {
	t.Helper()

	path := repoPath(t, elems...)
	data, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)
	return data
}

func readRepoText(t *testing.T, elems ...string) string {
	t.Helper()
	return string(readRepoFile(t, elems...))
}

func evalScenarioDirs(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(repoPath(t, ".xylem", "eval", "scenarios"))
	require.NoError(t, err)

	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry.Name())
		}
	}
	sort.Strings(dirs)
	return dirs
}

func firstEvalScenario(t *testing.T) string {
	t.Helper()

	dirs := evalScenarioDirs(t)
	require.NotEmpty(t, dirs, "expected at least one eval scenario directory")
	return dirs[0]
}

func loadHarborConfig(t *testing.T) smokeHarborConfig {
	t.Helper()

	var cfg smokeHarborConfig
	require.NoError(t, yaml.Unmarshal(readRepoFile(t, ".xylem", "eval", "harbor.yaml"), &cfg))
	return cfg
}

func loadTaskFile(t *testing.T, scenario string) smokeTaskFile {
	t.Helper()

	var taskFile smokeTaskFile
	require.NoError(t, toml.Unmarshal(readRepoFile(t, ".xylem", "eval", "scenarios", scenario, "task.toml"), &taskFile))
	return taskFile
}

func loadRubricFile(t *testing.T, name string) smokeRubricFile {
	t.Helper()

	var rubric smokeRubricFile
	require.NoError(t, toml.Unmarshal(readRepoFile(t, ".xylem", "eval", "rubrics", name), &rubric))
	return rubric
}

func loadCalibrationFile(t *testing.T, rubric string) smokeCalibrationFile {
	t.Helper()

	var calibration smokeCalibrationFile
	require.NoError(t, json.Unmarshal(
		readRepoFile(t, ".xylem", "eval", "calibration", rubric, "calibration.json"),
		&calibration,
	))
	return calibration
}

func pythonFunctionNames(src string) []string {
	re := regexp.MustCompile(`(?m)^def ([A-Za-z_][A-Za-z0-9_]*)\(`)
	matches := re.FindAllStringSubmatch(src, -1)

	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match[1])
	}
	return names
}

func pythonImportNames(src string) []string {
	re := regexp.MustCompile(`(?m)^import ([A-Za-z_][A-Za-z0-9_]*)`)
	matches := re.FindAllStringSubmatch(src, -1)

	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match[1])
	}
	sort.Strings(names)
	return names
}

func pythonDictBlock(t *testing.T, src, name string) string {
	t.Helper()

	start := strings.Index(src, name+" = {")
	require.NotEqual(t, -1, start, "expected %s dictionary", name)

	block := src[start:]
	end := strings.Index(block, "}")
	require.NotEqual(t, -1, end, "expected closing brace for %s dictionary", name)

	return block[:end+1]
}

func parsePythonIntDict(t *testing.T, src, name string) map[string]int {
	t.Helper()

	block := pythonDictBlock(t, src, name)
	re := regexp.MustCompile(`(?m)^\s*"([^"]*)":\s*([0-9]+),?`)
	matches := re.FindAllStringSubmatch(block, -1)
	require.NotEmpty(t, matches, "expected entries in %s", name)

	values := make(map[string]int, len(matches))
	for _, match := range matches {
		var value int
		_, err := fmt.Sscanf(match[2], "%d", &value)
		require.NoError(t, err)
		values[match[1]] = value
	}
	return values
}

func parsePythonFloatDict(t *testing.T, src, name string) map[string]float64 {
	t.Helper()

	block := pythonDictBlock(t, src, name)
	re := regexp.MustCompile(`(?m)^\s*"([^"]+)":\s*([0-9.]+),?`)
	matches := re.FindAllStringSubmatch(block, -1)
	require.NotEmpty(t, matches, "expected entries in %s", name)

	values := make(map[string]float64, len(matches))
	for _, match := range matches {
		var value float64
		_, err := fmt.Sscanf(match[2], "%f", &value)
		require.NoError(t, err)
		values[match[1]] = value
	}
	return values
}

func computeReward(checks []struct {
	Name   string
	Passed bool
}, weights map[string]float64) float64 {
	if len(checks) == 0 {
		return 0
	}

	totalWeight := 0.0
	earnedWeight := 0.0
	for _, check := range checks {
		weight := 1.0
		if weights != nil {
			if configured, ok := weights[check.Name]; ok {
				weight = configured
			}
		}
		totalWeight += weight
		if check.Passed {
			earnedWeight += weight
		}
	}

	if totalWeight == 0 {
		return 0
	}
	return earnedWeight / totalWeight
}

func TestSmoke_S1_EvalDirectoryScaffoldExists(t *testing.T) {
	expectedPaths := []string{
		repoPath(t, ".xylem", "eval", "harbor.yaml"),
		repoPath(t, ".xylem", "eval", "helpers", "xylem_verify.py"),
		repoPath(t, ".xylem", "eval", "helpers", "conftest.py"),
		repoPath(t, ".xylem", "eval", "scenarios"),
		repoPath(t, ".xylem", "eval", "rubrics", "plan_quality.toml"),
		repoPath(t, ".xylem", "eval", "rubrics", "evidence_quality.toml"),
	}

	for _, path := range expectedPaths {
		info, err := os.Stat(path)
		require.NoError(t, err, "expected %s to exist", path)
		if strings.HasSuffix(path, "scenarios") {
			assert.True(t, info.IsDir(), "%s should be a directory", path)
			continue
		}
		assert.False(t, info.IsDir(), "%s should be a file", path)
	}
}

func TestSmoke_S2_HarborYamlValidWithRequiredFields(t *testing.T) {
	cfg := loadHarborConfig(t)

	assert.Equal(t, "claude-code", cfg.Agent)
	assert.NotEmpty(t, cfg.Model)
	assert.Equal(t, "scenarios/", cfg.Path)
	assert.Positive(t, cfg.NAttempts)
	assert.Positive(t, cfg.NConcurrent)
}

func TestSmoke_S3_XylemVerifyImportsCleanly(t *testing.T) {
	src := readRepoText(t, ".xylem", "eval", "helpers", "xylem_verify.py")
	imports := pythonImportNames(src)

	assert.Equal(t, []string{"glob", "json", "os"}, imports)
}

func TestSmoke_S4_XylemVerifyExposesExpectedPublicAPI(t *testing.T) {
	src := readRepoText(t, ".xylem", "eval", "helpers", "xylem_verify.py")
	names := pythonFunctionNames(src)
	expected := []string{
		"find_vessel_dir",
		"load_summary",
		"load_evidence",
		"load_phase_output",
		"load_audit_log",
		"assert_vessel_completed",
		"assert_vessel_failed",
		"assert_phases_completed",
		"assert_gates_passed",
		"assert_evidence_level",
		"assert_cost_within_budget",
		"max_evidence_level",
		"count_phase_retries",
		"compute_reward",
		"build_result",
		"write_result",
		"write_reward",
	}

	for _, expectedName := range expected {
		assert.Contains(t, names, expectedName)
	}
	assert.Contains(t, src, "EVIDENCE_RANK =")
}

func TestSmoke_S5_EvidenceRankContainsAllFiveDocumentedLevels(t *testing.T) {
	ranks := parsePythonIntDict(t, readRepoText(t, ".xylem", "eval", "helpers", "xylem_verify.py"), "EVIDENCE_RANK")

	assert.Equal(t, 5, len(ranks))
	assert.Contains(t, ranks, "proved")
	assert.Contains(t, ranks, "mechanically_checked")
	assert.Contains(t, ranks, "behaviorally_checked")
	assert.Contains(t, ranks, "observed_in_situ")
	assert.Contains(t, ranks, "")
	assert.Greater(t, ranks["proved"], ranks["mechanically_checked"])
	assert.Greater(t, ranks["mechanically_checked"], ranks["behaviorally_checked"])
	assert.Greater(t, ranks["behaviorally_checked"], ranks["observed_in_situ"])
	assert.Greater(t, ranks["observed_in_situ"], ranks[""])
}

func TestSmoke_S6_ComputeRewardReturnsCorrectScores(t *testing.T) {
	src := readRepoText(t, ".xylem", "eval", "helpers", "xylem_verify.py")

	assert.Contains(t, src, "if not checks:")
	assert.Contains(t, src, "return 0.0")
	assert.Equal(t, 1.0, computeReward([]struct {
		Name   string
		Passed bool
	}{
		{Name: "a", Passed: true},
		{Name: "b", Passed: true},
	}, nil))
	assert.Equal(t, 0.5, computeReward([]struct {
		Name   string
		Passed bool
	}{
		{Name: "a", Passed: true},
		{Name: "b", Passed: false},
	}, nil))
	assert.Equal(t, 0.0, computeReward(nil, nil))
}

func TestSmoke_S7_ConftestExposesWorkDirTaskDirAndVerifyFixtures(t *testing.T) {
	names := pythonFunctionNames(readRepoText(t, ".xylem", "eval", "helpers", "conftest.py"))

	assert.Contains(t, names, "work_dir")
	assert.Contains(t, names, "task_dir")
	assert.Contains(t, names, "verify")
}

func TestSmoke_S8_ScenarioTaskTomlHasRequiredFields(t *testing.T) {
	scenario := firstEvalScenario(t)
	taskFile := loadTaskFile(t, scenario)

	assert.Equal(t, scenario, taskFile.Task.ID)
	assert.Positive(t, taskFile.Task.Environment.TimeoutSeconds)
}

func TestSmoke_S9_ScenarioInstructionContainsNoHarborSpecificTerms(t *testing.T) {
	scenario := firstEvalScenario(t)
	text := strings.ToLower(readRepoText(t, ".xylem", "eval", "scenarios", scenario, "instruction.md"))

	assert.NotEmpty(t, strings.TrimSpace(text))
	assert.NotContains(t, text, "harbor")
	assert.NotContains(t, text, "scoring")
	assert.NotContains(t, text, "verification")
	assert.Regexp(t, regexp.MustCompile(`\bxylem\s+\w+`), text)
}

func TestSmoke_S10_ScenarioDirectoryContainsAllRequiredFiles(t *testing.T) {
	scenario := firstEvalScenario(t)
	base := repoPath(t, ".xylem", "eval", "scenarios", scenario)

	requiredPaths := []string{
		filepath.Join(base, "instruction.md"),
		filepath.Join(base, "task.toml"),
		filepath.Join(base, "tests", "test.sh"),
		filepath.Join(base, "tests", "test_verification.py"),
	}

	for _, path := range requiredPaths {
		info, err := os.Stat(path)
		require.NoError(t, err, "expected %s to exist", path)
		assert.False(t, info.IsDir(), "%s should be a file", path)
	}

	info, err := os.Stat(filepath.Join(base, "tests", "test.sh"))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&0o100, "test.sh should be executable by the owner")
}

func TestSmoke_S11_PlanQualityTomlValidWithWeightsSummingToOne(t *testing.T) {
	rubric := loadRubricFile(t, "plan_quality.toml")

	assert.Equal(t, "plan_quality", rubric.Rubric.Name)
	assert.Len(t, rubric.Rubric.Criteria, 3)

	total := 0.0
	for _, criterion := range rubric.Rubric.Criteria {
		total += criterion.Weight
		assert.NotEmpty(t, criterion.Description)
	}
	assert.InDelta(t, 1.0, total, 0.001)
}

func TestSmoke_S12_EvidenceQualityTomlValidWithWeightsSummingToOne(t *testing.T) {
	rubric := loadRubricFile(t, "evidence_quality.toml")

	assert.Equal(t, "evidence_quality", rubric.Rubric.Name)

	total := 0.0
	for _, criterion := range rubric.Rubric.Criteria {
		total += criterion.Weight
		assert.NotEmpty(t, criterion.Description)
	}
	assert.InDelta(t, 1.0, total, 0.001)
}

func TestSmoke_S13_HarborYamlPathResolvesToDirectoryThatExists(t *testing.T) {
	cfg := loadHarborConfig(t)
	resolved := repoPath(t, ".xylem", "eval", cfg.Path)

	info, err := os.Stat(resolved)
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "%s should resolve to a directory", resolved)
}

func TestSmoke_S14_WorkflowExecutionVerificationTemplateProducesValidReward(t *testing.T) {
	template := readRepoText(t, ".xylem", "eval", "scenarios", "fix-simple-null-pointer", "tests", "test_verification.py")
	weights := parsePythonFloatDict(t, template, "weights")

	score := computeReward([]struct {
		Name   string
		Passed bool
	}{
		{Name: "vessel_completed", Passed: true},
		{Name: "phases_completed", Passed: true},
		{Name: "gate_passed", Passed: true},
		{Name: "evidence_level", Passed: true},
		{Name: "budget_ok", Passed: true},
	}, weights)

	assert.Equal(t, 1.0, score)
	assert.Contains(t, template, `assert score >= 0.8`)
}

func TestSmoke_S15_WorkflowExecutionVerificationTemplateFailsBelowThresholdWhenChecksFail(t *testing.T) {
	template := readRepoText(t, ".xylem", "eval", "scenarios", "fix-simple-null-pointer", "tests", "test_verification.py")
	weights := parsePythonFloatDict(t, template, "weights")

	score := computeReward([]struct {
		Name   string
		Passed bool
	}{
		{Name: "vessel_completed", Passed: false},
		{Name: "phases_completed", Passed: true},
		{Name: "gate_passed", Passed: true},
		{Name: "evidence_level", Passed: true},
		{Name: "budget_ok", Passed: true},
	}, weights)

	assert.Less(t, score, 0.8)
	assert.Contains(t, template, `assert score >= 0.8`)
}

func TestSmoke_S16_WriteRewardCreatesRewardTxtWithFourDecimalScore(t *testing.T) {
	src := readRepoText(t, ".xylem", "eval", "helpers", "xylem_verify.py")
	assert.Contains(t, src, `reward.txt`)
	assert.Contains(t, src, `f.write(f"{score:.4f}\n")`)
}

func TestSmoke_S17_DeferredItemsAreNotPresent(t *testing.T) {
	scenarioDirs := evalScenarioDirs(t)
	for _, scenario := range scenarioDirs {
		dockerfile := repoPath(t, ".xylem", "eval", "scenarios", scenario, "environment", "Dockerfile")
		_, err := os.Stat(dockerfile)
		assert.True(t, os.IsNotExist(err), "Dockerfile is deferred and should not exist: %s", dockerfile)
	}

	for _, pattern := range []string{
		repoPath(t, ".github", "workflows", "*.yml"),
		repoPath(t, ".github", "workflows", "*.yaml"),
	} {
		files, err := filepath.Glob(pattern)
		require.NoError(t, err)
		for _, file := range files {
			content, err := os.ReadFile(file)
			require.NoError(t, err)
			assert.NotContains(t, string(content), "harbor run")
		}
	}

	_, err := os.Stat(repoPath(t, ".xylem", "eval", "jobs"))
	require.True(t, os.IsNotExist(err), ".xylem/eval/jobs should be absent")

	_, err = os.Stat(repoPath(t, "jobs"))
	require.True(t, os.IsNotExist(err), "jobs should be absent at repo root")
}

func TestSmoke_S18_ScenariosDirectoryPresentEvenWithNoScenariosYetPopulated(t *testing.T) {
	info, err := os.Stat(repoPath(t, ".xylem", "eval", "scenarios"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestSmoke_S19_EvalCorpusIncludesRepresentativeScenarios(t *testing.T) {
	assert.Equal(t,
		[]string{
			"fix-simple-null-pointer",
			"gate-retry-then-pass",
			"label-gate-resume",
			"modify-harness-md",
			"pr-reporting-path",
		},
		evalScenarioDirs(t),
	)
}

func TestSmoke_S20_PlanQualityCalibrationIncludesHumanPassAndFailExamples(t *testing.T) {
	calibration := loadCalibrationFile(t, "plan_quality")

	assert.Equal(t, "plan_quality", calibration.Rubric)
	assert.InDelta(t, 0.7, calibration.PassThreshold, 0.001)
	assert.Len(t, calibration.Criteria, 3)
	assert.Len(t, calibration.Examples, 2)

	judgments := map[string]bool{}
	for _, example := range calibration.Examples {
		judgments[example.Judgment] = true
		assert.NotEmpty(t, strings.TrimSpace(example.OutputFile))
		assert.NotEmpty(t, strings.TrimSpace(readRepoText(t, ".xylem", "eval", "calibration", "plan_quality", example.OutputFile)))
		assert.NotEmpty(t, example.Criteria)
	}

	assert.True(t, judgments["pass"], "expected at least one pass example")
	assert.True(t, judgments["fail"], "expected at least one fail example")
}
