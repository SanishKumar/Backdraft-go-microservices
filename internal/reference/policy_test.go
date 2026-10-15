package reference

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"
)

var update = flag.Bool("update", false, "rewrite examples/reference.policy.json")

// The example policy that `backdraft check` is demonstrated on must be the
// policy the reference services actually run with.
func TestExamplePolicyIsCurrent(t *testing.T) {
	want, err := json.MarshalIndent(System(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	const path = "../../examples/reference.policy.json"
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
