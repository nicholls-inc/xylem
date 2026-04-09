package profiles

import (
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func TestPropComposeReturnsIndependentBytes(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		names := []string{"core"}
		if rapid.Bool().Draw(t, "includeSelfHosting") {
			if rapid.Bool().Draw(t, "selfHostingFirst") {
				names = []string{"self-hosting-xylem", "core"}
			} else {
				names = []string{"core", "self-hosting-xylem"}
			}
		}

		first, err := Compose(names...)
		if err != nil {
			t.Fatalf("Compose(%v) error = %v", names, err)
		}

		stable, err := Compose(names...)
		if err != nil {
			t.Fatalf("Compose(%v) second call error = %v", names, err)
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

		fresh, err := Compose(names...)
		if err != nil {
			t.Fatalf("Compose(%v) fresh call error = %v", names, err)
		}
		if got := composedSignature(fresh); got != expected {
			t.Fatalf("Compose(%v) fresh signature mismatch after mutating %s: got %q, want %q", names, asset.name, got, expected)
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
