package cadence

import (
	"fmt"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestPropIntervalFireTimeRespectsBoundary(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seconds := rapid.Int64Range(1, 24*60*60).Draw(t, "seconds")
		beforeOffset := rapid.Int64Range(0, seconds-1).Draw(t, "beforeOffset")
		afterOffset := rapid.Int64Range(seconds, seconds*2).Draw(t, "afterOffset")
		baseUnix := rapid.Int64Range(0, 4_000_000_000).Draw(t, "baseUnix")

		spec, err := Parse(fmt.Sprintf("%ds", seconds))
		if err != nil {
			t.Fatalf("Parse() error = %v", err)
		}

		base := time.Unix(baseUnix, 0).UTC()
		if _, due := spec.FireTime(&base, base.Add(time.Duration(beforeOffset)*time.Second)); due {
			t.Fatalf("expected no fire before interval boundary")
		}

		firedAt, due := spec.FireTime(&base, base.Add(time.Duration(afterOffset)*time.Second))
		if !due {
			t.Fatalf("expected fire at or after interval boundary")
		}
		want := base.Add(time.Duration(seconds) * time.Second)
		if !firedAt.Equal(want) {
			t.Fatalf("FireTime() = %s, want %s", firedAt, want)
		}
	})
}
