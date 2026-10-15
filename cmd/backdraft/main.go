// Command backdraft is the Backdraft control plane and policy checker.
//
//	backdraft serve   run the control plane (agent sessions, API, UI)
//	backdraft check   analyze a policy file, for CI
package main

import (
	"fmt"
	"os"
	"sort"
)

const usage = `usage: backdraft <command> [flags]

commands:
  serve    run the control plane: agent sessions, HTTP API and UI
  check    analyze a policy file and fail on findings (for CI)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		os.Exit(runServe(os.Args[2:], os.Stderr))
	case "check":
		os.Exit(runCheck(os.Args[2:], os.Stdout, os.Stderr))
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func sorted(s []string) []string {
	sort.Strings(s)
	return s
}
