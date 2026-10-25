package review

import (
	"fmt"
	"io"
	"strings"
)

func WriteMarkdown(w io.Writer, b Bundle, r Report) {
	name := strings.NewReplacer("\n", " ", "\r", " ", "`", "'", "|", "/").Replace(b.Name)
	fmt.Fprintf(w, "# Policy review: `%s`\n\n**%s** — candidate %d/%d passed; baseline %d/%d; %d regressions.\n\n", name, strings.ToUpper(r.Status), r.CandidatePassed, len(r.Cases), r.BaselinePassed, len(r.Cases), r.Regressions)
	fmt.Fprintf(w, "Bundle: `%s`  \nModel: `%s`  \nEngine: `%s`\n\n", r.BundleDigest, r.ModelDigest, b.Engine)
	fmt.Fprintf(w, "Runtime: `%s`\n\n", r.Runtime)
	fmt.Fprintf(w, "Contract: at least %.1f%% final success; recovery within %.1fs; peak backlog at most %.0f.\n\n", 100*b.Contract.MinSuccessRatio, b.Contract.MaxRecoveryS, b.Contract.MaxQueue)
	fmt.Fprintln(w, "| Service | Load | Fault | Seed | Baseline | Candidate | Recovery | Peak queue |\n|---|---:|---:|---:|---|---|---:|---:|")
	for _, c := range r.Cases {
		service := strings.NewReplacer("|", "/", "\n", " ", "`", "'").Replace(c.Scenario.Service)
		recovery := fmt.Sprintf("%.1fs", c.Candidate.RecoveryS)
		if c.Candidate.RecoveryS < 0 {
			recovery = "not recovered"
		}
		fmt.Fprintf(w, "| `%s` | %.0f/s | %.1fs | %d | %s | %s | %s | %.0f |\n", service, c.Scenario.Load, c.Scenario.Duration, c.Scenario.Seed, c.Baseline.Status, c.Candidate.Status, recovery, c.Candidate.PeakQueue)
	}
	fmt.Fprintln(w, "\nThe witness is the smallest tested failure, ordered by fault duration, load, service and seed, or a representative case when both policies pass. Passing applies only to this finite envelope and frozen model.\n\nFinal success uses the last five seconds or final 20% of the run, whichever is shorter. Recovery means sustained return to 90% of the pre-fault goodput ratio, measured over two-second windows. A warmup below 90% success fails the contract.")
	for _, warning := range r.Warnings {
		fmt.Fprintf(w, "\n- %s\n", warning)
	}
}
