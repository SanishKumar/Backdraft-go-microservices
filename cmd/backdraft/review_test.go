package main

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft/review"
)

func TestOfflineReviewCLIExitCodesAndArtifacts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review.json")
	out := new(bytes.Buffer)
	if code := runCapture([]string{"--example", "--out", path}, out, out); code != 0 {
		t.Fatal(code, out.String())
	}
	var b review.Bundle
	if err := readReviewFile(path, &b); err != nil {
		t.Fatal(err)
	}
	b.Contract.Seeds = []uint64{1}
	b.Contract.Durations = []float64{2}
	b.Contract.Loads = []float64{200}
	if err := saveReviewJSON(path, b); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(dir, "report.json")
	if code := runReview([]string{"--format", "markdown", "--out", reportPath, path}, out, out); code != 0 {
		t.Fatal(code, out.String())
	}
	var r review.Report
	if err := readReviewFile(reportPath, &r); err != nil {
		t.Fatal(err)
	}
	if r.BundleDigest != review.Digest(b) {
		t.Fatal("report does not identify reviewed inputs")
	}
	b.Contract.MaxQueue = 0
	if code := runReview([]string{"--require-capture", path}, out, out); code != 2 {
		t.Fatal("declared model bypassed capture requirement")
	}
	if err := saveReviewJSON(path, b); err != nil {
		t.Fatal(err)
	}
	if code := runReview([]string{path}, out, out); code != 1 {
		t.Fatal("failed contract exit", code, out.String())
	}
	b.Contract.MaxQueue = 1000
	b.Evidence.Source = "capture"
	b.Evidence.CapturedAt = time.Now().Add(-48 * time.Hour)
	if err := saveReviewJSON(path, b); err != nil {
		t.Fatal(err)
	}
	if code := runReview([]string{path}, out, out); code != 2 {
		t.Fatal("inconclusive exit", code, out.String())
	}
	if code := runReview([]string{"--candidate", filepath.Join(dir, "missing.json"), path}, out, out); code != 2 {
		t.Fatal("invalid input exit", code)
	}
	if code := runReview([]string{"--max-model-age", "24h", path}, out, out); code != 2 || !bytes.Contains(out.Bytes(), []byte("stale")) {
		t.Fatal("stale model bypassed freshness gate")
	}
}
