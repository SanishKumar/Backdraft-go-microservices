package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SanishKumar/go-microservices/backdraft/analysis"
)

func TestCheckRejectsUnknownFormatAndBrokenGraph(t *testing.T) {
	var out, errs bytes.Buffer
	if code := runCheck([]string{"--format", "typo", "../../examples/hardened.policy.json"}, &out, &errs); code != 2 || out.Len() != 0 {
		t.Fatalf("invalid format: exit %d, output %s", code, &out)
	}
	path := filepath.Join(t.TempDir(), "policy.json")
	for _, raw := range []string{
		`{"entry":"api","services":{"api":{"server":{},"fault":{},"clients":{"missing":{"maxAttempts":1}}}}}`,
		`{"entry":"api","services":{"api":{}}} {}`,
	} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		errs.Reset()
		if code := runCheck([]string{path}, &out, &errs); code != 2 {
			t.Fatalf("accepted %s: exit %d", raw, code)
		}
	}
}

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
