package main

import (
	"testing"

	"github.com/nicholls-inc/xylem/cli/internal/config"
	"github.com/nicholls-inc/xylem/cli/internal/queue"
	reviewpkg "github.com/nicholls-inc/xylem/cli/internal/review"
	"pgregory.net/rapid"
)

func TestPropResolveScheduledAuditRepoPrefersSourceThenMetadataThenFallback(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		sourceRepo := rapid.SampledFrom([]string{"", "owner/source", "team/service"}).Draw(t, "sourceRepo")
		metaRepo := rapid.SampledFrom([]string{"", "owner/meta", "ops/weekly"}).Draw(t, "metaRepo")
		cfgRepo := rapid.SampledFrom([]string{"", "owner/cfg", "platform/xylem"}).Draw(t, "cfgRepo")
		fallbackRepo := rapid.SampledFrom([]string{"", "owner/fallback", "core/repo"}).Draw(t, "fallbackRepo")

		cfg := &config.Config{
			Repo: cfgRepo,
			Sources: map[string]config.SourceConfig{
				"doctor": {
					Type: "scheduled",
					Repo: sourceRepo,
				},
			},
		}
		if fallbackRepo != "" {
			cfg.Sources["bugs"] = config.SourceConfig{
				Type: "github",
				Repo: fallbackRepo,
			}
		}

		vessel := queue.Vessel{
			Source:   "schedule",
			Workflow: reviewpkg.WorkflowHealthReportWorkflow,
			Meta: map[string]string{
				"config_source":  "doctor",
				"scheduled_repo": metaRepo,
			},
		}

		want := sourceRepo
		switch {
		case want != "":
		case metaRepo != "":
			want = metaRepo
		case cfgRepo != "":
			want = cfgRepo
		default:
			want = fallbackRepo
		}

		if got := resolveScheduledAuditRepo(cfg, vessel); got != want {
			t.Fatalf("resolveScheduledAuditRepo() = %q, want %q", got, want)
		}
	})
}
