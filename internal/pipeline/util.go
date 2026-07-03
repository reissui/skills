package pipeline

import "strings"

// containsAny reports whether s contains any of subs.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// slugify converts an issue title into a short, git-ref-safe slug used for the
// worktree/branch name. The workspace manager sanitizes the ref again, so this
// only needs to be reasonable, not perfect: lowercase, non-alphanumerics to
// hyphens, collapsed, trimmed, and capped in length.
func slugify(title string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	const maxLen = 40
	if len(s) > maxLen {
		s = strings.Trim(s[:maxLen], "-")
	}
	if s == "" {
		s = "issue"
	}
	return s
}
