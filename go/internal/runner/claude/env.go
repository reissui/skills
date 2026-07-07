package claude

import (
	"os/exec"
	"strings"
	"syscall"
)

// strippedVars are removed from the child environment unconditionally. If either
// is present, the `claude` CLI would authenticate against the metered Anthropic
// API instead of the user's subscription — a billing and compliance hazard the
// spec calls out explicitly (Security model: "ANTHROPIC_API_KEY /
// ANTHROPIC_AUTH_TOKEN stay stripped"). They are dropped even when the parent
// process sets them.
var strippedVars = map[string]bool{
	"ANTHROPIC_API_KEY":    true,
	"ANTHROPIC_AUTH_TOKEN": true,
}

// allowedVars is the allowlist of variable names passed through to the child.
// Everything else in the parent environment is dropped (least-privilege child
// processes). Subscription auth lives under HOME (~/.claude), so no API-key var
// is needed here. git/ssh essentials keep clone/commit/push working from inside
// a runner.
var allowedVars = map[string]bool{
	"PATH":            true,
	"HOME":            true,
	"USER":            true,
	"LOGNAME":         true,
	"SHELL":           true,
	"TMPDIR":          true,
	"LANG":            true,
	"LC_ALL":          true,
	"TERM":            true,
	"SSH_AUTH_SOCK":   true, // ssh-agent for git over ssh
	"GIT_SSH":         true,
	"GIT_SSH_COMMAND": true,
	"XDG_CONFIG_HOME": true, // git/ssh config discovery
	"XDG_CACHE_HOME":  true,
	"XDG_DATA_HOME":   true,
}

// allowedPrefixes lets whole families through without enumerating every name,
// e.g. all GIT_* configuration. The stripped set still wins over any prefix.
var allowedPrefixes = []string{"GIT_"}

// childEnv derives the minimal, allowlisted environment for a spawned CLI from
// the parent environment `parent` (each entry "KEY=VALUE"). It returns only
// allowlisted names and always omits the stripped auth vars, regardless of
// whether the parent set them.
func childEnv(parent []string) []string {
	out := make([]string, 0, len(allowedVars))
	for _, kv := range parent {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if strippedVars[name] {
			continue
		}
		if allowedVars[name] || hasAllowedPrefix(name) {
			out = append(out, kv)
		}
	}
	return out
}

// hasAllowedPrefix reports whether name matches an allowlisted prefix while not
// being an explicitly stripped var.
func hasAllowedPrefix(name string) bool {
	if strippedVars[name] {
		return false
	}
	for _, p := range allowedPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// killGroup terminates the child's entire process group so cancellation leaves
// no orphaned CLI tool subprocesses. Setpgid at spawn made the child a group
// leader whose pgid equals its pid; signalling the negated pid hits the group.
func killGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	// SIGKILL the group; the CLI does not need a graceful window here because
	// the pipeline reverts the issue on cancellation anyway.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	// Fall back to the direct child in case group setup failed for any reason.
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
