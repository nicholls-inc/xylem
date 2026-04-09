package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestProp_LatestPhaseActivityAtReturnsNewestMTime(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir, err := os.MkdirTemp("", "runner-monitor-prop-*")
		if err != nil {
			t.Fatalf("MkdirTemp() error = %v", err)
		}
		defer os.RemoveAll(dir)
		vesselID := rapid.StringMatching(`[A-Za-z0-9_-]{1,16}`).Draw(t, "vesselID")
		phaseDir := filepath.Join(dir, "phases", vesselID)
		if err := os.MkdirAll(phaseDir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", phaseDir, err)
		}

		count := rapid.IntRange(1, 8).Draw(t, "count")
		base := time.Now().UTC().Add(-1 * time.Hour)
		var want time.Time
		for i := 0; i < count; i++ {
			path := filepath.Join(phaseDir, fmt.Sprintf("%d-%s", i, rapid.StringMatching(`[a-z]{1,8}\.(output|prompt|command)`).Draw(t, "name")))
			if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
				t.Fatalf("WriteFile(%q): %v", path, err)
			}
			modTime := base.Add(time.Duration(rapid.IntRange(0, 3_600).Draw(t, "seconds")) * time.Second)
			if err := os.Chtimes(path, modTime, modTime); err != nil {
				t.Fatalf("Chtimes(%q): %v", path, err)
			}
			if modTime.After(want) {
				want = modTime
			}
		}

		got, err := latestPhaseActivityAt(dir, vesselID)
		if err != nil {
			t.Fatalf("latestPhaseActivityAt() error = %v", err)
		}
		if !got.Equal(want) {
			t.Fatalf("latestPhaseActivityAt() = %s, want %s", got, want)
		}
	})
}
