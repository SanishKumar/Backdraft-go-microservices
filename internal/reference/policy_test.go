package reference

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/SanishKumar/go-microservices/backdraft"
	"github.com/SanishKumar/go-microservices/backdraft/analysis"
)

var update = flag.Bool("update", false, "rewrite example policies")

// The example policy that `backdraft check` is demonstrated on must be the
// policy the reference services actually run with.
func TestExamplePolicyIsCurrent(t *testing.T) {
	hardened := analysis.Mitigate(System(), analysis.All())
	// Non-idempotent creation methods must not inherit read retries.
	gw := hardened.Services[Gateway]
	for peer, method := range map[string]string{Account: "PostAccount", Catalog: "PostProduct"} {
		p := gw.Clients[peer]
		p.NoRetry = []string{method}
		gw.Clients[peer] = p
	}
	order := gw.Clients[Order]
	order.MaxAttempts = 1 // a second 1.5s attempt cannot fit the user's 2s deadline
	order.Budget = nil
	gw.Clients[Order] = order
	hardened.Services[Gateway] = gw
	for _, finding := range analysis.Analyze(analysis.Input{System: hardened}).Findings {
		if finding.Severity == analysis.Critical || finding.Severity == analysis.High {
			t.Fatalf("hardened example must pass the default CI gate: %s", finding.Title)
		}
	}
	for name, sys := range map[string]backdraft.SystemConfig{"reference": System(), "hardened": hardened} {
		t.Run(name, func(t *testing.T) { checkExample(t, name, sys) })
	}
}

func checkExample(t *testing.T, name string, sys backdraft.SystemConfig) {
	t.Helper()
	want, err := json.MarshalIndent(sys, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := "../../examples/" + name + ".policy.json"
	if *update {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), want) {
		t.Fatalf("%s is stale; run: go test ./internal/reference -update", path)
	}
}
