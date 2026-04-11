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

		checks := securityComplianceBundle(composed)
		check := checks[rapid.IntRange(0, len(checks)-1).Draw(t, "checkIndex")]
		if len(check.data) == 0 {
			t.Fatalf("%s missing or empty", check.name)
		}
		byteIndex := rapid.IntRange(0, len(check.data)-1).Draw(t, "byteIndex")
		check.data[byteIndex] ^= 0xff

		fresh, err := Compose("core")
		if err != nil {
			t.Fatalf("Compose(core) fresh call error = %v", err)
		}

		for _, want := range securityComplianceBundle(fresh) {
			expectedFragment := securityComplianceExpectedFragments[want.name]
			if len(want.data) == 0 {
				t.Fatalf("%s missing or empty", want.name)
			}
			if !strings.Contains(string(want.data), expectedFragment) {
				t.Fatalf("%s = %q, want fragment %q", want.name, string(want.data), expectedFragment)
			}
		}
	})
}

func TestPropComposeCoreKeepsDocGardenBundlePresent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		composed, err := Compose("core")
		if err != nil {
			t.Fatalf("Compose(core) error = %v", err)
		}

		checks := docGardenBundle(composed)
		check := checks[rapid.IntRange(0, len(checks)-1).Draw(t, "checkIndex")]
		if len(check.data) == 0 {
			t.Fatalf("%s missing or empty", check.name)
		}
		byteIndex := rapid.IntRange(0, len(check.data)-1).Draw(t, "byteIndex")
		check.data[byteIndex] ^= 0xff

		fresh, err := Compose("core")
		if err != nil {
			t.Fatalf("Compose(core) fresh call error = %v", err)
		}

		for _, want := range docGardenBundle(fresh) {
			expectedFragment := docGardenExpectedFragments[want.name]
			if len(want.data) == 0 {
				t.Fatalf("%s missing or empty", want.name)
			}
			if !strings.Contains(string(want.data), expectedFragment) {
				t.Fatalf("%s = %q, want fragment %q", want.name, string(want.data), expectedFragment)
			}
		}
	})
}

func TestPropComposeSelfHostingXylemImplementHarnessWorkflowKeepsPRCreateContract(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		composed, err := Compose("core", "self-hosting-xylem")
		if err != nil {
			t.Fatalf("Compose(core, self-hosting-xylem) error = %v", err)
		}

		workflowData, ok := composed.Workflows["implement-harness"]
		if !ok {
			t.Fatal(`Compose(core, self-hosting-xylem) missing workflow "implement-harness"`)
		}
		if len(workflowData) == 0 {
			t.Fatal(`Compose(core, self-hosting-xylem) returned empty workflow "implement-harness"`)
		}

		byteIndex := rapid.IntRange(0, len(workflowData)-1).Draw(t, "byteIndex")
		workflowData[byteIndex] ^= 0xff

		fresh, err := Compose("core", "self-hosting-xylem")
		if err != nil {
			t.Fatalf("Compose(core, self-hosting-xylem) fresh call error = %v", err)
		}
		freshWorkflowData, ok := fresh.Workflows["implement-harness"]
		if !ok {
			t.Fatal(`Compose(core, self-hosting-xylem) fresh call missing workflow "implement-harness"`)
		}

		for _, requiredFragment := range implementHarnessPRCreateContract {
			if !strings.Contains(string(freshWorkflowData), requiredFragment) {
				t.Fatalf("implement-harness workflow missing fragment %q", requiredFragment)
			}
		}
	})
}

var securityComplianceExpectedFragments = map[string]string{
	"workflow:security-compliance":            "name: security-compliance",
	"prompt:security-compliance/scan_secrets": "RESULT: CLEAN | FINDINGS | TOOLING-GAP",
	"prompt:security-compliance/synthesize":   "ISSUES_CREATED:",
	"source:security-compliance":              "workflow: security-compliance",
}

var docGardenExpectedFragments = map[string]string{
	"workflow:doc-garden":       "name: doc-garden",
	"prompt:doc-garden/analyze": "cheap heuristics",
	"prompt:doc-garden/verify":  "current checked-in defaults and behavior",
	"source:doc-gardener":       "workflow: doc-garden",
}

var implementHarnessPRCreateContract = []string{
	`gh pr create`,
	`--repo nicholls-inc/xylem`,
	`--label "harness-impl"`,
	`--label "ready-to-merge"`,
}

type assetRef struct {
	name string
	data []byte
}

func securityComplianceBundle(composed *ComposedProfile) []assetRef {
	return []assetRef{
		{name: "workflow:security-compliance", data: composed.Workflows["security-compliance"]},
		{name: "prompt:security-compliance/scan_secrets", data: composed.Prompts["security-compliance/scan_secrets"]},
		{name: "prompt:security-compliance/synthesize", data: composed.Prompts["security-compliance/synthesize"]},
		{name: "source:security-compliance", data: composed.Sources["security-compliance"]},
	}
}

func docGardenBundle(composed *ComposedProfile) []assetRef {
	return []assetRef{
		{name: "workflow:doc-garden", data: composed.Workflows["doc-garden"]},
		{name: "prompt:doc-garden/analyze", data: composed.Prompts["doc-garden/analyze"]},
		{name: "prompt:doc-garden/verify", data: composed.Prompts["doc-garden/verify"]},
		{name: "source:doc-gardener", data: composed.Sources["doc-gardener"]},
	}
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
