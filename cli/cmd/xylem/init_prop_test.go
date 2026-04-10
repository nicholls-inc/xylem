package main

import (
	"slices"
	"strings"
	"testing"

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
