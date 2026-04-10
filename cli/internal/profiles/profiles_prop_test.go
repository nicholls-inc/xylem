package profiles

import (
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
