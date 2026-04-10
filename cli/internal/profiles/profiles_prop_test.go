package profiles

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func TestPropComposeCoreReturnsIndependentBytes(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		first, err := Compose("core")
		if err != nil {
			t.Fatalf("Compose(core) error = %v", err)
		}

		stable, err := Compose("core")
		if err != nil {
			t.Fatalf("Compose(core) second call error = %v", err)
		}
		expected := composedSignature(stable)

		assets := composedAssets(first)
		if len(assets) == 0 {
			t.Fatal("Compose(core) returned no assets")
		}
		asset := assets[rapid.IntRange(0, len(assets)-1).Draw(t, "assetIndex")]
		if len(asset.data) == 0 {
			t.Fatalf("asset %q is empty", asset.name)
		}
		byteIndex := rapid.IntRange(0, len(asset.data)-1).Draw(t, "byteIndex")
		asset.data[byteIndex] ^= 0xff

		if got := composedSignature(stable); got != expected {
			t.Fatalf("mutating %s changed another composition: got %q, want %q", asset.name, got, expected)
		}

		fresh, err := Compose("core")
		if err != nil {
			t.Fatalf("Compose(core) fresh call error = %v", err)
		}
		if got := composedSignature(fresh); got != expected {
			t.Fatalf("Compose(core) fresh signature mismatch after mutating %s: got %q, want %q", asset.name, got, expected)
		}
	})
}

func TestPropComposeCoreKeepsSecurityComplianceBundlePresent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		composed, err := Compose("core")
		if err != nil {
			t.Fatalf("Compose(core) error = %v", err)
		}

		checks := []assetRef{
			{name: "workflow:security-compliance", data: composed.Workflows["security-compliance"]},
			{name: "prompt:security-compliance/scan_secrets", data: composed.Prompts["security-compliance/scan_secrets"]},
			{name: "prompt:security-compliance/synthesize", data: composed.Prompts["security-compliance/synthesize"]},
			{name: "source:security-compliance", data: composed.Sources["security-compliance"]},
		}
		check := checks[rapid.IntRange(0, len(checks)-1).Draw(t, "checkIndex")]
		if len(check.data) == 0 {
			t.Fatalf("%s missing or empty", check.name)
		}

		expectedFragment := map[string]string{
			"workflow:security-compliance":            "name: security-compliance",
			"prompt:security-compliance/scan_secrets": "RESULT: CLEAN | FINDINGS | TOOLING-GAP",
			"prompt:security-compliance/synthesize":   "ISSUES_CREATED:",
			"source:security-compliance":              "workflow: security-compliance",
		}[check.name]
		if !strings.Contains(string(check.data), expectedFragment) {
			t.Fatalf("%s = %q, want fragment %q", check.name, string(check.data), expectedFragment)
		}
	})
}

func TestPropComposeCoreAndSelfHostingRetainsAutoTriageAssets(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		composed, err := Compose("core", "self-hosting-xylem")
		if err != nil {
			t.Fatalf("Compose(core, self-hosting-xylem) error = %v", err)
		}

		stable, err := Compose("core", "self-hosting-xylem")
		if err != nil {
			t.Fatalf("Compose(core, self-hosting-xylem) stable call error = %v", err)
		}

		expected, err := autoTriageAssetSnapshot(stable)
		if err != nil {
			t.Fatalf("snapshot stable auto-triage assets: %v", err)
		}

		candidates := make([]assetRef, 0, len(expected))
		for _, asset := range composedAssets(composed) {
			if _, ok := expected[asset.name]; ok {
				candidates = append(candidates, asset)
			}
		}
		if len(candidates) == 0 {
			t.Fatal("no auto-triage assets available for mutation")
		}

		asset := candidates[rapid.IntRange(0, len(candidates)-1).Draw(t, "assetIndex")]
		byteIndex := rapid.IntRange(0, len(asset.data)-1).Draw(t, "byteIndex")
		asset.data[byteIndex] ^= 0xff
		if bytes.Equal(asset.data, expected[asset.name]) {
			t.Fatalf("mutating %s did not change its bytes", asset.name)
		}

		fresh, err := Compose("core", "self-hosting-xylem")
		if err != nil {
			t.Fatalf("Compose(core, self-hosting-xylem) fresh call error = %v", err)
		}

		got, err := autoTriageAssetSnapshot(fresh)
		if err != nil {
			t.Fatalf("snapshot fresh auto-triage assets: %v", err)
		}
		if len(got) != len(expected) {
			t.Fatalf("fresh snapshot count mismatch: got %d, want %d", len(got), len(expected))
		}
		for name, want := range expected {
			if !bytes.Equal(got[name], want) {
				t.Fatalf("fresh asset %s mismatch after mutating %s", name, asset.name)
			}
		}
	})
}

type assetRef struct {
	name string
	data []byte
}

func composedSignature(composed *ComposedProfile) string {
	return joinMap(composed.Workflows) + "|" +
		joinMap(composed.Prompts) + "|" +
		joinMap(composed.Sources) + "|" +
		joinOverlays(composed.ConfigOverlays)
}

func joinMap(m map[string][]byte) string {
	keys := sortedKeys(m)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+string(m[key]))
	}
	return strings.Join(parts, "\n--\n")
}

func joinOverlays(overlays [][]byte) string {
	parts := make([]string, 0, len(overlays))
	for _, overlay := range overlays {
		parts = append(parts, string(overlay))
	}
	return strings.Join(parts, "\n==\n")
}

func composedAssets(composed *ComposedProfile) []assetRef {
	assets := make([]assetRef, 0, len(composed.Workflows)+len(composed.Prompts)+len(composed.Sources)+len(composed.ConfigOverlays))
	for _, key := range sortedKeys(composed.Workflows) {
		assets = append(assets, assetRef{name: "workflow:" + key, data: composed.Workflows[key]})
	}
	for _, key := range sortedKeys(composed.Prompts) {
		assets = append(assets, assetRef{name: "prompt:" + key, data: composed.Prompts[key]})
	}
	for _, key := range sortedKeys(composed.Sources) {
		assets = append(assets, assetRef{name: "source:" + key, data: composed.Sources[key]})
	}
	for i, overlay := range composed.ConfigOverlays {
		assets = append(assets, assetRef{name: fmt.Sprintf("overlay:%d", i), data: overlay})
	}
	return assets
}

func autoTriageAssetSnapshot(composed *ComposedProfile) (map[string][]byte, error) {
	if composed == nil {
		return nil, fmt.Errorf("composed profile is nil")
	}

	snapshot := make(map[string][]byte, 5)
	requiredAssets := []struct {
		name string
		data []byte
		ok   bool
	}{
		{name: "workflow:auto-triage-issues", data: composed.Workflows["auto-triage-issues"], ok: composed.Workflows["auto-triage-issues"] != nil},
		{name: "prompt:auto-triage-issues/discover", data: composed.Prompts["auto-triage-issues/discover"], ok: composed.Prompts["auto-triage-issues/discover"] != nil},
		{name: "prompt:auto-triage-issues/classify", data: composed.Prompts["auto-triage-issues/classify"], ok: composed.Prompts["auto-triage-issues/classify"] != nil},
		{name: "prompt:auto-triage-issues/apply", data: composed.Prompts["auto-triage-issues/apply"], ok: composed.Prompts["auto-triage-issues/apply"] != nil},
		{name: "source:auto-triage", data: composed.Sources["auto-triage"], ok: composed.Sources["auto-triage"] != nil},
	}
	for _, asset := range requiredAssets {
		if !asset.ok {
			return nil, fmt.Errorf("missing %s", asset.name)
		}
		if len(asset.data) == 0 {
			return nil, fmt.Errorf("%s is empty", asset.name)
		}
		snapshot[asset.name] = append([]byte(nil), asset.data...)
	}

	overlayMatches := 0
	for i, overlay := range composed.ConfigOverlays {
		if !bytes.Contains(overlay, []byte("auto-triage:")) {
			continue
		}

		name := fmt.Sprintf("overlay:%d", i)
		if len(overlay) == 0 {
			return nil, fmt.Errorf("%s is empty", name)
		}
		snapshot[name] = append([]byte(nil), overlay...)
		overlayMatches++
	}
	if overlayMatches != 1 {
		return nil, fmt.Errorf("expected 1 auto-triage overlay, got %d", overlayMatches)
	}

	return snapshot, nil
}
