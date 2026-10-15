package lab_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft/tower"
	"github.com/SanishKumar/go-microservices/internal/lab"
)

type client struct {
	t   *testing.T
	api string
}

func (c client) do(method, path string, body any, out any) {
	c.t.Helper()
	if code := c.try(method, path, body, out); code >= 300 {
		c.t.Fatalf("%s %s: HTTP %d", method, path, code)
	}
}

func (c client) try(method, path string, body any, out any) int {
	c.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, c.api+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode < 300 {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// success is the share of user requests that succeeded over the last
// three seconds.
func (c client) success() (ratio, offered float64) {
	var good float64
	for i := 0; i < 3; i++ {
		var s tower.Snapshot
		c.try("GET", "/api/live", nil, &s) // 503 until the first tick
		good += s.Entry.Goodput
		offered += s.Entry.Offered
		time.Sleep(time.Second)
	}
	if offered == 0 {
		return 0, 0
	}
	return min(1, good/offered), offered / 3
}

func (c client) waitFor(what string, limit time.Duration, cond func(ratio, offered float64) bool) {
	c.t.Helper()
	deadline := time.Now().Add(limit)
	for {
		r, o := c.success()
		if cond(r, o) {
			c.t.Logf("%s: %.0f%% of %.0f req/s succeed", what, 100*r, o)
			return
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("%s: not reached within %s (%.0f%% of %.0f req/s succeed)", what, limit, 100*r, o)
		}
	}
}

// The claim the whole project rests on, checked against the real running
// system rather than the simulator: at about a fifth of capacity, eight
// seconds of catalog slowness leaves the reference system down after the
// slowness is gone, and switching on retry budgets brings it back.
func TestMetastableFailureAndRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a live system through an incident for about a minute")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, err := lab.Start(ctx, lab.Options{RPS: 200})
	if err != nil {
		t.Fatal(err)
	}
	c := client{t, l.API}

	c.waitFor("steady state", 45*time.Second, func(r, o float64) bool { return o > 150 && r > 0.97 })

	c.do("POST", "/api/fault", map[string]any{"service": "catalog", "latencyMs": 300, "durationS": 8}, nil)
	time.Sleep(8 * time.Second)
	t.Log("fault cleared")
	time.Sleep(10 * time.Second)
	if r, o := c.success(); r > 0.2 {
		t.Fatalf("expected the system to stay down after the fault cleared, but %.0f%% of %.0f req/s succeed", 100*r, o)
	} else {
		t.Logf("13s after the fault cleared: %.0f%% succeed (metastable)", 100*r)
	}

	c.do("POST", "/api/mitigations", map[string]bool{"retryBudgets": true}, nil)
	start := time.Now()
	c.waitFor("recovery with retry budgets", 60*time.Second, func(r, _ float64) bool { return r > 0.95 })
	t.Log(fmt.Sprintf("recovered %s after enabling retry budgets", time.Since(start).Round(time.Second)))
}
