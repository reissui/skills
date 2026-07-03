package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"
)

// commandTimeout bounds any single CLI invocation's outside-world work (a probe
// sweep, a gh call, an IPC round-trip). It is generous for humans but keeps a
// wedged dependency from hanging the process forever.
const commandTimeout = 30 * time.Second

// newFlagSet builds a flag.FlagSet wired to write errors/usage to stderr and to
// carry the standard --json flag. Every command uses this so --json is uniform
// and help goes to the right stream. The returned *bool is the parsed --json.
func newFlagSet(e *env, name, oneLine string) (*flag.FlagSet, *bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	fs.Usage = func() {
		fmt.Fprintf(e.stderr, "clex %s — %s\n\nFlags:\n", name, oneLine)
		fs.PrintDefaults()
	}
	return fs, jsonOut
}

// parseFlags parses args and maps flag's help/parse errors onto exit codes:
// -h/--help is success (usage was printed), any other parse error is a usage
// error (exit 2). ok is false when the caller should return code immediately.
func parseFlags(fs *flag.FlagSet, args []string) (code int, ok bool) {
	switch err := fs.Parse(args); err {
	case nil:
		return exitOK, true
	case flag.ErrHelp:
		return exitOK, false
	default:
		return exitProblem, false
	}
}

// writeJSON encodes v as indented JSON followed by a newline. Encoding failure
// is reported to stderr and returns exitError; callers propagate the code.
func writeJSON(w io.Writer, v any) int {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(w, "clex: encode json: %v\n", err)
		return exitError
	}
	fmt.Fprintf(w, "%s\n", b)
	return exitOK
}

// fail prints a leading "clex: " error line to stderr and returns exitError. In
// JSON mode it emits {"ok":false,"error":...} to stdout instead so scripts can
// parse the failure uniformly.
func fail(e *env, jsonMode bool, format string, a ...any) int {
	msg := fmt.Sprintf(format, a...)
	if jsonMode {
		writeJSON(e.stdout, map[string]any{"ok": false, "error": msg})
		return exitError
	}
	fmt.Fprintf(e.stderr, "clex: %s\n", msg)
	return exitError
}

// cmdContext returns a context bounded by commandTimeout and the env clock, plus
// its cancel. Every command that reaches the outside world derives from this.
func (e *env) cmdContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), commandTimeout)
}
