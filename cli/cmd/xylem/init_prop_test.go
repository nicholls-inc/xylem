package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/profiles"
	"pgregory.net/rapid"
)

func TestPropResolveProfilesClassifiesInputsConsistently(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		rawParts := rapid.SliceOf(rapid.SampledFrom([]string{
			"core",
			"self-hosting-xylem",
			"nonexistent",
			"",
			"  core",
			"self-hosting-xylem  ",
		})).Draw(t, "parts")

		raw := strings.Join(rawParts, ",")
		profiles, err := resolveProfiles(raw)
		expected, wantErr := expectedProfiles(raw)
		if wantErr {
			if err == nil {
				t.Fatalf("resolveProfiles(%q) succeeded, want error", raw)
			}
			if !strings.Contains(err.Error(), "invalid --profile") {
				t.Fatalf("resolveProfiles(%q) error %q does not mention invalid --profile", raw, err)
			}
			return
		}

		if err != nil {
			t.Fatalf("resolveProfiles(%q) returned unexpected error: %v", raw, err)
		}
		if !slices.Equal(profiles, expected) {
			t.Fatalf("resolveProfiles(%q) = %v, want %v", raw, profiles, expected)
		}
	})
}

func expectedProfiles(raw string) ([]string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []string{"core"}, false
	}

	parts := strings.Split(trimmed, ",")
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		names = append(names, name)
	}

	switch {
	case slices.Equal(names, []string{"core"}):
		return []string{"core"}, false
	case slices.Equal(names, []string{"core", "self-hosting-xylem"}):
		return []string{"core", "self-hosting-xylem"}, false
	default:
		return nil, true
	}
}

func TestPropSyncProfileAssetsPreservesExistingFilesWhenForceFalse(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		stateDir, err := os.MkdirTemp("", "xylem-sync-assets-*")
		if err != nil {
			rt.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(stateDir)

		composed := &profiles.ComposedProfile{
			Workflows: rapidWorkflowMap(rt, "workflow"),
			Prompts:   rapidPromptMap(rt, "prompt"),
		}

		existingWorkflows := make(map[string][]byte)
		for _, name := range sortedKeys(composed.Workflows) {
			if !rapid.Bool().Draw(rt, "existing-workflow-"+name) {
				continue
			}
			content := []byte(rapid.StringMatching(`[a-z0-9 -]{0,24}`).Draw(rt, "existing-workflow-content-"+name))
			path := filepath.Join(stateDir, "workflows", name+".yaml")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				rt.Fatalf("MkdirAll(%q) error = %v", path, err)
			}
			if err := os.WriteFile(path, content, 0o644); err != nil {
				rt.Fatalf("WriteFile(%q) error = %v", path, err)
			}
			existingWorkflows[name] = content
		}

		existingPrompts := make(map[string][]byte)
		for _, name := range sortedKeys(composed.Prompts) {
			if !rapid.Bool().Draw(rt, "existing-prompt-"+name) {
				continue
			}
			content := []byte(rapid.StringMatching(`[a-z0-9 -]{0,24}`).Draw(rt, "existing-prompt-content-"+name))
			path := filepath.Join(stateDir, "prompts", filepath.FromSlash(name)+".md")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				rt.Fatalf("MkdirAll(%q) error = %v", path, err)
			}
			if err := os.WriteFile(path, content, 0o644); err != nil {
				rt.Fatalf("WriteFile(%q) error = %v", path, err)
			}
			existingPrompts[name] = content
		}

		if err := syncProfileAssets(stateDir, composed, false); err != nil {
			rt.Fatalf("syncProfileAssets() error = %v", err)
		}

		for name, want := range composed.Workflows {
			got, err := os.ReadFile(filepath.Join(stateDir, "workflows", name+".yaml"))
			if err != nil {
				rt.Fatalf("ReadFile(workflow %q) error = %v", name, err)
			}
			if existing, ok := existingWorkflows[name]; ok {
				if string(got) != string(existing) {
					rt.Fatalf("workflow %q = %q, want preserved %q", name, string(got), string(existing))
				}
				continue
			}
			if string(got) != string(want) {
				rt.Fatalf("workflow %q = %q, want %q", name, string(got), string(want))
			}
		}

		for name, want := range composed.Prompts {
			got, err := os.ReadFile(filepath.Join(stateDir, "prompts", filepath.FromSlash(name)+".md"))
			if err != nil {
				rt.Fatalf("ReadFile(prompt %q) error = %v", name, err)
			}
			if existing, ok := existingPrompts[name]; ok {
				if string(got) != string(existing) {
					rt.Fatalf("prompt %q = %q, want preserved %q", name, string(got), string(existing))
				}
				continue
			}
			if string(got) != string(want) {
				rt.Fatalf("prompt %q = %q, want %q", name, string(got), string(want))
			}
		}
	})
}

func TestPropSyncProfileAssetsMaterializesComposedAssetsWhenForceTrue(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		stateDir, err := os.MkdirTemp("", "xylem-sync-assets-*")
		if err != nil {
			rt.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(stateDir)

		composed := &profiles.ComposedProfile{
			Workflows: rapidWorkflowMap(rt, "workflow"),
			Prompts:   rapidPromptMap(rt, "prompt"),
		}

		for _, name := range sortedKeys(composed.Workflows) {
			path := filepath.Join(stateDir, "workflows", name+".yaml")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				rt.Fatalf("MkdirAll(%q) error = %v", path, err)
			}
			oldContent := []byte(rapid.StringMatching(`[a-z0-9 -]{0,24}`).Draw(rt, "old-workflow-content-"+name))
			if err := os.WriteFile(path, oldContent, 0o644); err != nil {
				rt.Fatalf("WriteFile(%q) error = %v", path, err)
			}
		}

		for _, name := range sortedKeys(composed.Prompts) {
			path := filepath.Join(stateDir, "prompts", filepath.FromSlash(name)+".md")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				rt.Fatalf("MkdirAll(%q) error = %v", path, err)
			}
			oldContent := []byte(rapid.StringMatching(`[a-z0-9 -]{0,24}`).Draw(rt, "old-prompt-content-"+name))
			if err := os.WriteFile(path, oldContent, 0o644); err != nil {
				rt.Fatalf("WriteFile(%q) error = %v", path, err)
			}
		}

		if err := syncProfileAssets(stateDir, composed, true); err != nil {
			rt.Fatalf("syncProfileAssets() error = %v", err)
		}

		for name, want := range composed.Workflows {
			got, err := os.ReadFile(filepath.Join(stateDir, "workflows", name+".yaml"))
			if err != nil {
				rt.Fatalf("ReadFile(workflow %q) error = %v", name, err)
			}
			if string(got) != string(want) {
				rt.Fatalf("workflow %q = %q, want %q", name, string(got), string(want))
			}
		}

		for name, want := range composed.Prompts {
			got, err := os.ReadFile(filepath.Join(stateDir, "prompts", filepath.FromSlash(name)+".md"))
			if err != nil {
				rt.Fatalf("ReadFile(prompt %q) error = %v", name, err)
			}
			if string(got) != string(want) {
				rt.Fatalf("prompt %q = %q, want %q", name, string(got), string(want))
			}
		}
	})
}

// TestPropResyncProfileAssetsMaterializesAllComposedAssets verifies that
// resyncProfileAssets always writes every composed asset to disk, regardless
// of whether the file already exists with different content.
func TestPropResyncProfileAssetsMaterializesAllComposedAssets(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		stateDir, err := os.MkdirTemp("", "xylem-resync-assets-*")
		if err != nil {
			rt.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(stateDir)

		composed := &profiles.ComposedProfile{
			Workflows: rapidWorkflowMap(rt, "workflow"),
			Prompts:   rapidPromptMap(rt, "prompt"),
		}

		// Pre-populate some files with arbitrary (stale) content.
		for _, name := range sortedKeys(composed.Workflows) {
			if !rapid.Bool().Draw(rt, "pre-exist-workflow-"+name) {
				continue
			}
			path := filepath.Join(stateDir, "workflows", name+".yaml")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				rt.Fatalf("MkdirAll(%q) error = %v", path, err)
			}
			stale := []byte(rapid.StringMatching(`[a-z0-9 -]{0,24}`).Draw(rt, "stale-wf-"+name))
			if err := os.WriteFile(path, stale, 0o644); err != nil {
				rt.Fatalf("WriteFile(%q) error = %v", path, err)
			}
		}
		for _, name := range sortedKeys(composed.Prompts) {
			if !rapid.Bool().Draw(rt, "pre-exist-prompt-"+name) {
				continue
			}
			path := filepath.Join(stateDir, "prompts", filepath.FromSlash(name)+".md")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				rt.Fatalf("MkdirAll(%q) error = %v", path, err)
			}
			stale := []byte(rapid.StringMatching(`[a-z0-9 -]{0,24}`).Draw(rt, "stale-pr-"+name))
			if err := os.WriteFile(path, stale, 0o644); err != nil {
				rt.Fatalf("WriteFile(%q) error = %v", path, err)
			}
		}

		if err := resyncProfileAssets(stateDir, composed); err != nil {
			rt.Fatalf("resyncProfileAssets() error = %v", err)
		}

		// Every composed workflow must match the embedded content exactly.
		for name, want := range composed.Workflows {
			got, err := os.ReadFile(filepath.Join(stateDir, "workflows", name+".yaml"))
			if err != nil {
				rt.Fatalf("ReadFile(workflow %q) error = %v", name, err)
			}
			if string(got) != string(want) {
				rt.Fatalf("workflow %q = %q, want %q", name, string(got), string(want))
			}
		}

		// Every composed prompt must match the embedded content exactly.
		for name, want := range composed.Prompts {
			got, err := os.ReadFile(filepath.Join(stateDir, "prompts", filepath.FromSlash(name)+".md"))
			if err != nil {
				rt.Fatalf("ReadFile(prompt %q) error = %v", name, err)
			}
			if string(got) != string(want) {
				rt.Fatalf("prompt %q = %q, want %q", name, string(got), string(want))
			}
		}
	})
}

func rapidWorkflowMap(rt *rapid.T, label string) map[string][]byte {
	count := rapid.IntRange(0, 4).Draw(rt, label+"-count")
	assets := make(map[string][]byte, count)
	for len(assets) < count {
		name := rapid.StringMatching(`[a-z0-9-]{1,8}`).Draw(rt, label+"-name")
		assets[name] = []byte(rapid.StringMatching(`[a-z0-9 -]{0,24}`).Draw(rt, label+"-content-"+name))
	}
	return assets
}

func rapidPromptMap(rt *rapid.T, label string) map[string][]byte {
	count := rapid.IntRange(0, 4).Draw(rt, label+"-count")
	assets := make(map[string][]byte, count)
	for len(assets) < count {
		name := rapid.StringMatching(`[a-z0-9-]{1,8}/[a-z0-9-]{1,8}`).Draw(rt, label+"-name")
		assets[name] = []byte(rapid.StringMatching(`[a-z0-9 -]{0,24}`).Draw(rt, label+"-content-"+name))
	}
	return assets
}
