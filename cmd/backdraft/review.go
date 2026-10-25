package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft/review"
	"github.com/SanishKumar/go-microservices/internal/jsonutil"
	"github.com/SanishKumar/go-microservices/internal/reference"
)

func readReviewFile(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 8<<20 {
		return fmt.Errorf("%s exceeds 8 MiB", path)
	}
	return jsonutil.DecodeObject(f, v)
}

func saveReviewJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0600)
}

func runCapture(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("capture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	address := fs.String("url", "http://127.0.0.1:7070", "control plane URL")
	out := fs.String("out", "", "output bundle file (required)")
	name := fs.String("name", "Policy review", "review name")
	example := fs.Bool("example", false, "use the declared reference model; no running services required")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *out == "" {
		fmt.Fprintln(stderr, "usage: backdraft capture --out review.json [--url http://127.0.0.1:7070 | --example] [--name NAME]")
		return 2
	}
	var b review.Bundle
	if *example {
		sys := reference.System()
		b = review.Bundle{Version: review.Version, Engine: review.Engine, Name: *name, Model: reference.SimModel(sys), Candidate: review.ProposedPolicy(reference.SimModel(sys)), Contract: review.DefaultContract(reference.Catalog, 200), Evidence: review.Evidence{Source: "declared"}}
	} else {
		u, err := url.Parse(*address)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			fmt.Fprintln(stderr, "--url must be an HTTP(S) control plane URL without credentials")
			return 2
		}
		u.Path = strings.TrimRight(u.Path, "/") + "/api/reviews/capture"
		u.RawQuery = ""
		u.Fragment = ""
		payload, _ := json.Marshal(map[string]string{"name": *name})
		client := http.Client{Timeout: 15 * time.Second}
		res, err := client.Post(u.String(), "application/json", bytes.NewReader(payload))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
			fmt.Fprintf(stderr, "capture: HTTP %d: %s\n", res.StatusCode, raw)
			return 2
		}
		var response struct {
			Bundle   review.Bundle `json:"bundle"`
			Warnings []string      `json:"warnings"`
		}
		if err = jsonutil.DecodeObject(io.LimitReader(res.Body, 8<<20), &response); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		b = response.Bundle
	}
	if err := b.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := saveReviewJSON(*out, b); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintf(stdout, "Saved %s (%s model, %s).\n", *out, b.Evidence.Source, review.Digest(b)[:12])
	for _, warning := range b.Warnings() {
		fmt.Fprintln(stderr, warning)
	}
	return 0
}

func runReview(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(stderr)
	candidate := fs.String("candidate", "", "candidate SystemConfig JSON (overrides bundle candidate)")
	out := fs.String("out", "", "write the complete JSON report, including failing-case time series")
	format := fs.String("format", "text", "stdout format: text, json, markdown")
	timeout := fs.Duration("timeout", 90*time.Second, "maximum evaluation time (1s to 10m)")
	requireCapture := fs.Bool("require-capture", false, "reject declared models; require a calibrated capture")
	maxAge := fs.Duration("max-model-age", 0, "reject captures older than this duration (0 disables age check)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: backdraft review [--candidate policy.json] [--out report.json] [--format text|json|markdown] bundle.json")
		return 2
	}
	if *format != "text" && *format != "json" && *format != "markdown" {
		fmt.Fprintln(stderr, "unknown output format")
		return 2
	}
	if *timeout < time.Second || *timeout > 10*time.Minute {
		fmt.Fprintln(stderr, "timeout must be between 1s and 10m")
		return 2
	}
	if *maxAge < 0 {
		fmt.Fprintln(stderr, "max-model-age cannot be negative")
		return 2
	}
	var b review.Bundle
	if err := readReviewFile(fs.Arg(0), &b); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if (*requireCapture || *maxAge > 0) && b.Evidence.Source != "capture" {
		fmt.Fprintln(stderr, "this gate requires a captured model; declared examples are not accepted")
		return 2
	}
	if *maxAge > 0 && (b.Evidence.CapturedAt.IsZero() || time.Since(b.Evidence.CapturedAt) > *maxAge || b.Evidence.CapturedAt.After(time.Now().Add(time.Minute))) {
		fmt.Fprintln(stderr, "model capture is stale or has an invalid timestamp; capture healthy traffic again")
		return 2
	}
	if *candidate != "" {
		if err := readReviewFile(*candidate, &b.Candidate); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	r, err := review.Run(ctx, b)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *out != "" {
		if err := saveReviewJSON(*out, r); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	switch *format {
	case "json":
		json.NewEncoder(stdout).Encode(r)
	case "markdown":
		review.WriteMarkdown(stdout, b, *r)
	default:
		fmt.Fprintf(stdout, "%s: %s\nCandidate %d/%d passed; baseline %d/%d; %d regressions.\nBundle %s\n", strings.ToUpper(r.Status), b.Name, r.CandidatePassed, len(r.Cases), r.BaselinePassed, len(r.Cases), r.Regressions, r.BundleDigest)
		for _, c := range r.Cases {
			if c.Candidate.Status != "pass" {
				fmt.Fprintf(stdout, "  %s: %s, %.0f req/s, %.1fs fault, seed %d: %s\n", c.Candidate.Status, c.Scenario.Service, c.Scenario.Load, c.Scenario.Duration, c.Scenario.Seed, strings.Join(c.Candidate.Reasons, " "))
			}
		}
		for _, warning := range r.Warnings {
			fmt.Fprintln(stdout, warning)
		}
	}
	if r.Status == "fail" {
		return 1
	}
	if r.Status != "pass" {
		return 2
	}
	return 0
}
