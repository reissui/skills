package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/reissui/clex/internal/ipc"
)

// daemonUnavailableError builds the standard "is the daemon running?" message
// naming the socket path, so the operator knows exactly what to start (spec:
// daemon-required commands fail clearly). It maps to exit 1.
func (e *env) daemonUnavailableError(cause error) string {
	return fmt.Sprintf("cannot reach the clex daemon at %s (is clexd running? try 'clex service status'): %v",
		e.socketPath(), cause)
}

// doIPC dials the daemon and performs one request. A transport failure is
// wrapped with the socket-path hint. A protocol-level rejection (OK=false) is
// returned as a non-nil error carrying the daemon's message. On success it
// returns the Response for the caller to render.
func (e *env) doIPC(req ipc.Request, jsonMode bool) (ipc.Response, error) {
	ctx, cancel := e.cmdContext()
	defer cancel()
	resp, err := e.ipcClient().Do(ctx, req)
	if err != nil {
		return ipc.Response{}, errors.New(e.daemonUnavailableError(err))
	}
	if !resp.OK {
		return resp, fmt.Errorf("%s", strings.TrimSpace(resp.Error))
	}
	return resp, nil
}

// cmdStatus renders the pipeline + daemon snapshot. Human mode prints a compact
// board (running issues, pipeline counts, scheduler Explain reasons, pending
// gate); --json prints the raw ipc.Status.
func cmdStatus(e *env, args []string) int {
	fs, jsonOut := newFlagSet(e, "status", "show pipeline and daemon state")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	resp, err := e.doIPC(ipc.Request{Command: ipc.CmdStatus}, *jsonOut)
	if err != nil {
		return fail(e, *jsonOut, "%v", err)
	}
	if *jsonOut {
		return writeJSON(e.stdout, resp.Status)
	}
	fmt.Fprint(e.stdout, renderStatusTable(resp.Status))
	return exitOK
}

// renderStatusTable formats a Status for humans. Kept in the CLI (not reused
// from the daemon's Telegram renderer) so the terminal layout can differ.
func renderStatusTable(st *ipc.Status) string {
	if st == nil {
		return "no status available\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "clexd %s", st.Version)
	if st.Repo != "" {
		fmt.Fprintf(&b, "  %s", st.Repo)
	}
	if st.Paused {
		b.WriteString("  [paused]")
	}
	if st.Uptime != "" {
		fmt.Fprintf(&b, "  up %s", st.Uptime)
	}
	b.WriteByte('\n')

	if len(st.Running) == 0 {
		b.WriteString("running: (idle)\n")
	} else {
		b.WriteString("running:\n")
		for _, r := range st.Running {
			fmt.Fprintf(&b, "  #%-5d %-10s %-16s %s\n", r.Issue, r.Provider, r.Model, r.Stage)
		}
	}
	if len(st.Pipeline) > 0 {
		b.WriteString("pipeline:")
		for _, k := range sortedKeys(st.Pipeline) {
			fmt.Fprintf(&b, " %s=%d", k, st.Pipeline[k])
		}
		b.WriteByte('\n')
	}
	if len(st.Explain) > 0 {
		b.WriteString("scheduler:\n")
		for _, line := range st.Explain {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	if st.PendingGate != "" {
		fmt.Fprintf(&b, "gate pending: %s\n", st.PendingGate)
	}
	return b.String()
}

// cmdSteer sends steering text toward an issue/epic. Usage:
//
//	clex steer <issue|epic> "guidance…"   (issue 0 / "epic" steers the epic)
func cmdSteer(e *env, args []string) int {
	fs, jsonOut := newFlagSet(e, "steer", "send steering guidance to an issue or epic")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	rest := fs.Args()
	if len(rest) < 2 {
		return fail(e, *jsonOut, `usage: clex steer <issue|epic> "guidance"`)
	}
	issue, perr := parseIssueTarget(rest[0])
	if perr != nil {
		return fail(e, *jsonOut, "%v", perr)
	}
	text := strings.TrimSpace(strings.Join(rest[1:], " "))
	if text == "" {
		return fail(e, *jsonOut, "steering text is empty")
	}
	resp, err := e.doIPC(ipc.Request{Command: ipc.CmdSteer, Issue: issue, Text: text}, *jsonOut)
	if err != nil {
		return fail(e, *jsonOut, "%v", err)
	}
	return renderMessage(e, *jsonOut, resp)
}

// cmdModels renders registry health per model. Human mode is a ✓/✗ table.
func cmdModels(e *env, args []string) int {
	fs, jsonOut := newFlagSet(e, "models", "show model registry health")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	resp, err := e.doIPC(ipc.Request{Command: ipc.CmdModels}, *jsonOut)
	if err != nil {
		return fail(e, *jsonOut, "%v", err)
	}
	if *jsonOut {
		return writeJSON(e.stdout, resp.Models)
	}
	if len(resp.Models) == 0 {
		fmt.Fprintln(e.stdout, "no models available")
		return exitOK
	}
	for _, m := range resp.Models {
		mark := "✓"
		if !m.Healthy {
			mark = "✗"
		}
		fmt.Fprintf(e.stdout, "%s %-18s %-10s %s\n", mark, m.Model, m.Provider, m.Detail)
	}
	return exitOK
}

// cmdCosts renders spend and estimate-vs-actual drift.
func cmdCosts(e *env, args []string) int {
	fs, jsonOut := newFlagSet(e, "costs", "show spend and estimate drift")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	resp, err := e.doIPC(ipc.Request{Command: ipc.CmdCosts}, *jsonOut)
	if err != nil {
		return fail(e, *jsonOut, "%v", err)
	}
	if *jsonOut {
		return writeJSON(e.stdout, resp.Costs)
	}
	c := resp.Costs
	if c == nil {
		fmt.Fprintln(e.stdout, "no cost data available")
		return exitOK
	}
	fmt.Fprintf(e.stdout, "epic:  spent $%.2f", c.SpentThisEpicUSD)
	if c.EstimatedThisEpicUSD > 0 {
		drift := c.SpentThisEpicUSD - c.EstimatedThisEpicUSD
		fmt.Fprintf(e.stdout, "  (est $%.2f, drift %+.2f)", c.EstimatedThisEpicUSD, drift)
	}
	fmt.Fprintf(e.stdout, "\ntoday: spent $%.2f\n", c.SpentTodayUSD)
	for _, line := range c.Lines {
		fmt.Fprintf(e.stdout, "  %s\n", line)
	}
	return exitOK
}

// cmdPause sets the global pause flag.
func cmdPause(e *env, args []string) int {
	return e.simpleControl(args, "pause", "pause new dispatches (running work continues)", ipc.CmdPause)
}

// cmdResume clears the global pause flag.
func cmdResume(e *env, args []string) int {
	return e.simpleControl(args, "resume", "resume dispatching", ipc.CmdResume)
}

// simpleControl runs a no-argument state-changing command and renders the
// daemon's confirmation message. Shared by pause/resume.
func (e *env) simpleControl(args []string, name, oneLine string, cmd ipc.Command) int {
	fs, jsonOut := newFlagSet(e, name, oneLine)
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	resp, err := e.doIPC(ipc.Request{Command: cmd}, *jsonOut)
	if err != nil {
		return fail(e, *jsonOut, "%v", err)
	}
	return renderMessage(e, *jsonOut, resp)
}

// renderMessage prints a daemon Response's Message line (human) or the whole
// response (JSON). Used by the control commands that return only a confirmation.
func renderMessage(e *env, jsonMode bool, resp ipc.Response) int {
	if jsonMode {
		return writeJSON(e.stdout, map[string]any{"ok": resp.OK, "message": resp.Message})
	}
	msg := strings.TrimSpace(resp.Message)
	if msg == "" {
		msg = "ok"
	}
	fmt.Fprintln(e.stdout, msg)
	return exitOK
}

// parseIssueTarget parses a steer/plan/build target: a bare number, "#123", or
// the literal "epic" (which targets issue 0, the epic).
func parseIssueTarget(s string) (int, error) {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "epic") {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimPrefix(s, "#"))
	if err != nil {
		return 0, fmt.Errorf("invalid issue target %q: want a number, #number, or 'epic'", s)
	}
	return n, nil
}

// sortedKeys returns the keys of m in sorted order (stable table/JSON output).
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
