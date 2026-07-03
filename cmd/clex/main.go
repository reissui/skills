// Command clex is the CLI for setup, manual pipeline control, and diagnostics.
//
// Every command runs against an injectable env (app.go): production wires the
// real world in newEnv, tests construct one with fakes so no command touches a
// live service. main() is a thin shell; run() parses the top-level command and
// dispatches. Human tables are the default; --json on every command emits a
// stable machine object. Exit codes are 0 ok, 1 error, 2 doctor-problems/usage
// (spec: CLI).
package main

import (
	"fmt"
	"os"

	"github.com/reissui/clex/internal/version"
)

const usage = `clex — self-hosted agentic development orchestrator (CLI)

Usage:
  clex <command> [flags]

Setup & diagnostics:
  init       Guided setup wizard (labels, config, Telegram bind)
  doctor     Check binaries, auth, tokens, and role resolution
  service    Install/uninstall/status the launchd or systemd unit

Pipeline:
  idea       File a feature idea as a labelled GitHub issue
  plan       Plan an issue (research → plan gate)
  build      Build an issue or epic
  status     Show pipeline and daemon state
  steer      Send steering guidance to an issue or epic
  models     Show model registry health
  costs      Show spend and estimate drift
  pause      Pause new dispatches (running work continues)
  resume     Resume dispatching
  gc         Garbage-collect merged worktrees

Maintenance:
  update     Update the clex binary (self-update)
  version    Print the clex version and exit
  help       Show this help

Global flags:
  --json     Emit machine-readable JSON instead of a human table

Run "clex <command> --help" for command-specific flags.
See docs/superpowers/specs/2026-07-03-clex-design.md for the full design.
`

func main() {
	env := newEnv(os.Stdin, os.Stdout, os.Stderr)
	os.Exit(run(env, os.Args[1:]))
}

// run dispatches the top-level command. It is kept small and table-testable:
// version/help/unknown are handled here; every real command is a func(*env,
// []string) int registered in commands, so a test can invoke one directly with
// a fake env.
func run(e *env, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stdout, usage)
		return exitOK
	}
	name, rest := args[0], args[1:]
	switch name {
	case "help", "-h", "--help":
		fmt.Fprint(e.stdout, usage)
		return exitOK
	case "version", "--version":
		fmt.Fprintln(e.stdout, version.Version)
		return exitOK
	}
	if cmd, ok := commands[name]; ok {
		return cmd(e, rest)
	}
	fmt.Fprintf(e.stderr, "clex: unknown command %q\n\n%s", name, usage)
	return exitProblem
}

// commands is the command registry: name → handler. Each handler parses its own
// flags (including --json) and returns an exit code. Registering here keeps run
// trivial and lets tests dispatch a single command in isolation.
var commands = map[string]func(*env, []string) int{
	"init":    cmdInit,
	"doctor":  cmdDoctor,
	"service": cmdService,
	"idea":    cmdIdea,
	"plan":    cmdPlan,
	"build":   cmdBuild,
	"status":  cmdStatus,
	"steer":   cmdSteer,
	"models":  cmdModels,
	"costs":   cmdCosts,
	"pause":   cmdPause,
	"resume":  cmdResume,
	"gc":      cmdGC,
	"update":  cmdUpdate,
}
