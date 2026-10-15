package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/SanishKumar/go-microservices/backdraft"
	"github.com/SanishKumar/go-microservices/backdraft/analysis"
)

const checkUsage = `usage: backdraft check [flags] policy.json

Analyzes a system's timeout, retry and admission policy and reports the
problems that only exist between services. Exits 1 if any finding is at or
above --fail-on, so it can gate a merge.

`

func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	failOn := fs.String("fail-on", "high", "lowest severity that fails the check: critical, high, medium, low, or none")
	format := fs.String("format", "text", "output format: text or json")
	fs.Usage = func() {
		fmt.Fprint(stderr, checkUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}

	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	var sys backdraft.SystemConfig
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sys); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", fs.Arg(0), err)
		return 2
	}
	if _, ok := sys.Services[sys.Entry]; !ok {
		fmt.Fprintf(stderr, "%s: entry %q is not one of the services\n", fs.Arg(0), sys.Entry)
		return 2
	}
	for name, cfg := range sys.Services {
		if err := cfg.Validate(); err != nil {
			fmt.Fprintf(stderr, "%s: service %s: %v\n", fs.Arg(0), name, err)
			return 2
		}
	}

	report := analysis.Analyze(analysis.Input{System: sys})
	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.Encode(report)
	} else {
		printReport(stdout, report)
	}

	threshold, ok := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "none": -1}[*failOn]
	if !ok {
		fmt.Fprintf(stderr, "unknown --fail-on %q\n", *failOn)
		return 2
	}
	rank := map[analysis.Severity]int{analysis.Critical: 0, analysis.High: 1, analysis.Medium: 2, analysis.Low: 3}
	for _, f := range report.Findings {
		if rank[f.Severity] <= threshold {
			return 1
		}
	}
	return 0
}

func printReport(w io.Writer, r analysis.Report) {
	if len(r.Findings) == 0 {
		fmt.Fprintln(w, "No findings.")
		return
	}
	for _, f := range r.Findings {
		fmt.Fprintf(w, "%-8s  %s\n", strings.ToUpper(string(f.Severity)), f.Title)
		fmt.Fprintf(w, "          %s\n", wrap(f.Detail, 72, "          "))
		fmt.Fprintf(w, "    fix:  %s\n", wrap(f.Fix, 72, "          "))
		if f.Patch != nil {
			fmt.Fprintf(w, "          (auto-fixable: %s)\n", strings.Join(patchServices(f.Patch), ", "))
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "%d critical, %d high, %d medium, %d low\n",
		r.Counts[analysis.Critical], r.Counts[analysis.High], r.Counts[analysis.Medium], r.Counts[analysis.Low])
}

func patchServices(p analysis.Patch) []string {
	var out []string
	for s := range p {
		out = append(out, s)
	}
	return sorted(out)
}

func wrap(s string, width int, indent string) string {
	var b strings.Builder
	line := 0
	for i, word := range strings.Fields(s) {
		if i > 0 && line+1+len(word) > width {
			b.WriteString("\n" + indent)
			line = 0
		} else if i > 0 {
			b.WriteByte(' ')
			line++
		}
		b.WriteString(word)
		line += len(word)
	}
	return b.String()
}
