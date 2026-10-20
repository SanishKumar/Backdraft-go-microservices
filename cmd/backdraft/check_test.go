package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/SanishKumar/go-microservices/backdraft/analysis"
)

func TestPolicyGateRejectsRiskyAndAcceptsHardenedPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
	}{{"reference", 1}, {"hardened", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errs bytes.Buffer
			code := runCheck([]string{"--format", "json", "../../examples/" + tc.name + ".policy.json"}, &out, &errs)
			if code != tc.code {
				t.Fatalf("exit %d, want %d: %s", code, tc.code, errs.String())
			}
			var report analysis.Report
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatalf("invalid JSON report: %v", err)
			}
		})
	}
}
