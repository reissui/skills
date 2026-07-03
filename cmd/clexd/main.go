// Command clexd is the long-running clex daemon: it polls GitHub and Telegram,
// schedules work, and supervises runner child processes.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/reissui/clex/internal/version"
)

const usage = `clexd — self-hosted agentic development orchestrator (daemon)

Usage:
  clexd [flags]

Flags:
  --version    Print the clexd version and exit
  --help       Show this help

The daemon long-polls GitHub and Telegram (no public endpoint required),
schedules eligible issues, and supervises runner processes.
See docs/superpowers/specs/2026-07-03-clex-design.md for the full design.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("clexd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			// --help / -h: usage was already written; this is success.
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, version.Version)
		return 0
	}
	// v1 scaffold: the daemon event loop is implemented in issue #16.
	fmt.Fprint(stdout, usage)
	return 0
}
