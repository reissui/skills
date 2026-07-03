package registry

import "regexp"

// tokenPattern matches common secret shapes (bearer tokens, sk-/ghp_ style keys,
// long hex/base64 runs) so error details folded into provider health never carry
// a credential (spec: Security — secrets never logged; event log redacts token
// patterns).
var tokenPattern = regexp.MustCompile(`(?i)(sk-[a-z0-9\-_]{8,}|ghp_[a-z0-9]{8,}|xox[baprs]-[a-z0-9\-]{8,}|bearer\s+[a-z0-9\-_.]{8,}|[a-f0-9]{32,}|[a-z0-9+/]{40,}={0,2})`)

// redactError renders an error's message with any token-shaped substring masked.
// It is applied to provider probe errors before they are stored as health
// detail, which the status line and doctor may surface.
func redactError(err error) string {
	if err == nil {
		return ""
	}
	return tokenPattern.ReplaceAllString(err.Error(), "[redacted]")
}
