package reporter

import (
	"fmt"
	"strings"

	"github.com/nicholls-inc/xylem/cli/internal/evidence"
)

func formatEvidenceSection(manifest *evidence.Manifest) string {
	if manifest == nil || len(manifest.Claims) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("### Verification evidence\n\n")
	sb.WriteString("| Claim | Level | Checker | Result |\n")
	sb.WriteString("|-------|-------|---------|--------|\n")

	trustBoundaries := make([]evidence.Claim, 0, len(manifest.Claims))
	for _, claim := range manifest.Claims {
		fmt.Fprintf(&sb, "| %s | %s | %s | %s |\n",
			formatEvidenceCell(claim.Claim),
			formatEvidenceCell(claim.Level.String()),
			formatEvidenceCell(claim.Checker),
			evidenceResult(claim.Passed),
		)
		if claim.TrustBoundary != "" {
			trustBoundaries = append(trustBoundaries, claim)
		}
	}

	if len(trustBoundaries) == 0 {
		return sb.String()
	}

	sb.WriteString("\n<details>\n<summary>Trust boundaries</summary>\n\n")
	for _, claim := range trustBoundaries {
		fmt.Fprintf(&sb, "- **%s** — %s\n", claim.Claim, claim.TrustBoundary)
	}
	sb.WriteString("\n</details>")

	return sb.String()
}

func evidenceResult(passed bool) string {
	if passed {
		return ":white_check_mark:"
	}
	return ":x:"
}

func formatEvidenceCell(value string) string {
	replacer := strings.NewReplacer("\r\n", "<br>", "\r", "<br>", "\n", "<br>", "|", `\|`)
	return replacer.Replace(value)
}
