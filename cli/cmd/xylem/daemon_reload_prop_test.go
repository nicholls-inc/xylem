package main

import (
	"encoding/json"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestPropDaemonReloadLogEntryRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		entry := daemonReloadLogEntry{
			Trigger:        rapid.StringMatching(`[a-z-]{1,12}`).Draw(rt, "trigger"),
			Result:         rapid.StringMatching(`[a-z_]{1,12}`).Draw(rt, "result"),
			BeforeDigest:   rapid.StringMatching(`[a-z0-9-]{0,20}`).Draw(rt, "before_digest"),
			AfterDigest:    rapid.StringMatching(`[a-z0-9-]{0,20}`).Draw(rt, "after_digest"),
			PRNumber:       rapid.IntRange(0, 100000).Draw(rt, "pr_number"),
			MergeCommitSHA: rapid.StringMatching(`[a-f0-9]{0,16}`).Draw(rt, "merge_commit_sha"),
			Rollback:       rapid.Bool().Draw(rt, "rollback"),
			Error:          rapid.StringMatching(`[ -~]{0,40}`).Draw(rt, "error"),
			Timestamp:      time.Unix(rapid.Int64Range(0, 1<<20).Draw(rt, "timestamp"), 0).UTC(),
		}

		data, err := json.Marshal(entry)
		if err != nil {
			rt.Fatalf("Marshal() error = %v", err)
		}
		var decoded daemonReloadLogEntry
		if err := json.Unmarshal(data, &decoded); err != nil {
			rt.Fatalf("Unmarshal() error = %v", err)
		}
		if decoded != entry {
			rt.Fatalf("round trip mismatch: got %#v want %#v", decoded, entry)
		}
	})
}
