package daemon

import (
	"regexp"
	"strings"
)

// redactedPlaceholder replaces any matched secret in a log line.
const redactedPlaceholder = "[REDACTED]"

// tokenPatterns matches well-known secret shapes so they never reach the event
// log (spec: Security model — "the event log redacts known secret patterns").
// These are deliberately broad: a false positive that redacts a non-secret is
// strictly safer than leaking a credential. Patterns cover the token families
// clex actually handles.
var tokenPatterns = []*regexp.Regexp{
	// GitHub tokens: fine-grained PAT (github_pat_), classic PAT (ghp_),
	// OAuth (gho_), app/user/server (ghu_/ghs_/ghr_). Base62 body of varying
	// length; require a sizable run so ordinary words don't match.
	regexp.MustCompile(`\b(?:github_pat_|ghp_|gho_|ghu_|ghs_|ghr_)[A-Za-z0-9_]{20,}\b`),
	// Anthropic API keys.
	regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}\b`),
	// OpenAI keys, including project-scoped (sk-proj-) and legacy (sk-).
	regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_\-]{20,}\b`),
	// Telegram bot tokens: "<digits>:<35+ base64url chars>".
	regexp.MustCompile(`\b\d{6,}:[A-Za-z0-9_\-]{30,}\b`),
	// Slack-style xoxb/xoxp tokens (defensive; clex may log third-party output).
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9\-]{10,}\b`),
}

// Redactor scrubs secrets from strings before they are written to the event
// log or other operator-facing surfaces. It combines the built-in token-shape
// patterns with a set of exact literal secrets supplied at construction (the
// live Telegram token and GitHub PAT), so even a token that does not match a
// generic pattern is removed by exact substring match. The zero value is not
// usable — build one with NewRedactor.
type Redactor struct {
	literals []string
}

// NewRedactor returns a Redactor that removes both the built-in token patterns
// and any of the given exact literals. Empty literals are ignored. Literals are
// sorted longest-first so a token that contains a shorter secret as a substring
// is fully removed before the shorter match runs.
func NewRedactor(literals ...string) *Redactor {
	var kept []string
	for _, l := range literals {
		if l != "" {
			kept = append(kept, l)
		}
	}
	// Longest-first: prevents a short literal from partially masking a longer
	// one and leaving a recognizable tail.
	for i := 0; i < len(kept); i++ {
		for j := i + 1; j < len(kept); j++ {
			if len(kept[j]) > len(kept[i]) {
				kept[i], kept[j] = kept[j], kept[i]
			}
		}
	}
	return &Redactor{literals: kept}
}

// Redact returns s with every known secret replaced by the placeholder. It is
// safe to call on arbitrary text — runner output, error messages, issue bodies
// — and is applied to every event-log detail.
func (r *Redactor) Redact(s string) string {
	if s == "" {
		return s
	}
	for _, lit := range r.literals {
		if strings.Contains(s, lit) {
			s = strings.ReplaceAll(s, lit, redactedPlaceholder)
		}
	}
	for _, re := range tokenPatterns {
		s = re.ReplaceAllString(s, redactedPlaceholder)
	}
	return s
}
