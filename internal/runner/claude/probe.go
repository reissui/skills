package claude

import (
	"bytes"
	"context"
	"os/exec"
	"strings"

	"github.com/reissui/clex/internal/core"
)

// Probe reports whether the Claude Code CLI is installed and authenticated, and
// how much rate-limit headroom remains. It runs two cheap commands: `claude
// --version` (presence + version string) and a minimal streamed prompt whose
// result/rate-limit lines reveal auth failures and usage-limit exhaustion.
// Failures are classified into Availability.Detail rather than returned as a Go
// error, so the model registry can display "why" for an unavailable provider.
func (a *Adapter) Probe(ctx context.Context) (core.Availability, error) {
	verCmd := exec.CommandContext(ctx, a.binPath, "--version")
	verCmd.Env = a.envFunc()
	verOut, err := verCmd.Output()
	if err != nil {
		return core.Availability{Healthy: false, Detail: "claude CLI not runnable: " + errString(err)}, nil
	}
	version := strings.TrimSpace(string(verOut))

	// Cheap auth/headroom check: a tiny streamed prompt. We only inspect the
	// terminal + rate-limit lines, so any trivial prompt works.
	authCmd := exec.CommandContext(ctx, a.binPath,
		"-p", "ping",
		"--output-format", "stream-json",
		"--verbose",
	)
	authCmd.Env = a.envFunc()
	var buf bytes.Buffer
	authCmd.Stdout = &buf
	authCmd.Stderr = &buf
	runErr := authCmd.Run()

	avail := classifyProbe(version, buf.Bytes(), runErr)
	return avail, nil
}

// classifyProbe turns the auth-check output into an Availability. It scans the
// stream for a rejecting rate_limit_event or an error result and matches known
// auth/limit phrases; anything else with a clean exit is treated as healthy.
func classifyProbe(version string, streamOut []byte, runErr error) core.Availability {
	var rateLimited, authFailed bool
	var detail string

	for _, raw := range bytes.Split(streamOut, []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		evs, _ := parseLine(raw)
		for _, ev := range evs {
			if ev.Type != core.EventError {
				continue
			}
			if isAuthError(ev.Err) {
				authFailed = true
				detail = firstNonEmpty(detail, ev.Err)
			} else if isRateLimit(ev.Err) {
				rateLimited = true
				detail = firstNonEmpty(detail, ev.Err)
			} else {
				detail = firstNonEmpty(detail, ev.Err)
			}
		}
		// A rejecting rate_limit_event line is a headroom signal even without an
		// error result.
		if bytes.Contains(raw, []byte(`"rate_limit_event"`)) && bytes.Contains(raw, []byte(`"rejected"`)) {
			rateLimited = true
		}
	}

	switch {
	case authFailed:
		return core.Availability{Healthy: false, Detail: firstNonEmpty(detail, "authentication failed; run `claude` and /login")}
	case rateLimited:
		return core.Availability{Healthy: false, Detail: firstNonEmpty(detail, "rate limit reached")}
	case runErr != nil && detail != "":
		return core.Availability{Healthy: false, Detail: detail}
	case runErr != nil:
		return core.Availability{Healthy: false, Detail: "auth check failed: " + errString(runErr)}
	default:
		return core.Availability{Healthy: true, Detail: version}
	}
}

// isAuthError reports whether an error string looks like a Claude auth failure.
func isAuthError(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "invalid api key") ||
		strings.Contains(l, "/login") ||
		strings.Contains(l, "authenticate") ||
		strings.Contains(l, "unauthorized") ||
		strings.Contains(l, "not logged in")
}

// isRateLimit reports whether an error string looks like a usage-limit / rate
// exhaustion message.
func isRateLimit(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "usage limit") ||
		strings.Contains(l, "rate limit") ||
		strings.Contains(l, "out_of_credits") ||
		strings.Contains(l, "out of credits")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
