// Command clex is the CLI for setup, manual pipeline control, and diagnostics.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/reissui/clex/internal/version"
)

const usage = `clex — self-hosted agentic development orchestrator (CLI)

Usage:
  clex <command> [flags]

Commands:
  version    Print the clex version and exit
  help       Show this help

Run "clex <command> --help" for command-specific flags.
See docs/superpowers/specs/2026-07-03-clex-design.md for the full design.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("clex", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			// --help / -h: usage was already written to stderr; treat as success.
			return 0
		}
		// flag already printed the error and usage.
		return 2
	}

	switch fs.Arg(0) {
	case "", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version":
		fmt.Fprintln(stdout, version.Version)
		return 0
	default:
		fmt.Fprintf(stderr, "clex: unknown command %q\n\n%s", fs.Arg(0), usage)
		return 2
	}
}
