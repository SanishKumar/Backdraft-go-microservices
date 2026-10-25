// Command backdraft is the Backdraft control plane and policy checker.
//
//	backdraft serve   run the control plane (agent sessions, API, UI)
//	backdraft check   analyze a policy file, for CI
package main

import (
	"fmt"
	"os"
	"runtime"
	"sort"
)

const usage = `usage: backdraft <command> [flags]

commands:
  serve    run the control plane: agent sessions, HTTP API and UI
  check    analyze a policy file and fail on findings (for CI)
  capture  freeze a connected system or the reference model into a review bundle
  review   evaluate a frozen review bundle offline (for CI)
  demo     start the reference services and review workspace locally
  version  print the build version
`

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version", "--version":
		fmt.Printf("backdraft %s (%s)\n", version, runtime.Version())
	case "demo":
		os.Exit(runDemo(os.Args[2:], os.Stderr))
	case "serve":
		os.Exit(runServe(os.Args[2:], os.Stderr))
	case "check":
		os.Exit(runCheck(os.Args[2:], os.Stdout, os.Stderr))
	case "capture":
		os.Exit(runCapture(os.Args[2:], os.Stdout, os.Stderr))
	case "review":
		os.Exit(runReview(os.Args[2:], os.Stdout, os.Stderr))
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func sorted(s []string) []string {
	sort.Strings(s)
	return s
}
