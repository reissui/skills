package scheduler

import "github.com/bmatcuk/doublestar/v4"

// overlaps reports whether two sets of file globs can match a common path — i.e.
// whether the two issues might touch the same file and therefore must not run
// concurrently.
//
// Rules (spec: Scheduler — Conflict avoidance):
//   - An empty glob set means "touches everything" and overlaps any non-nil or
//     empty set.
//   - Two glob sets overlap if any glob in one is compatible with any glob in
//     the other, judged by prefix compatibility on their literal (non-wildcard)
//     leading segments, or if either glob is a pure wildcard.
//
// Because clex globs are declared over paths that don't exist yet, we cannot
// test them against a real filesystem; instead we compare glob *shapes*: two
// globs overlap when one could match a path the other could also match. We
// approximate this conservatively (favouring serialization over missed
// conflicts) via literal-prefix compatibility.
func overlaps(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		// Missing/wildcard touches overlaps everything.
		return true
	}
	for _, ga := range a {
		for _, gb := range b {
			if globsOverlap(ga, gb) {
				return true
			}
		}
	}
	return false
}

// overlapsAny reports whether globs a overlaps any of the claimed glob sets.
func overlapsAny(a []string, claimed [][]string) bool {
	for _, c := range claimed {
		if overlaps(a, c) {
			return true
		}
	}
	return false
}

// globsOverlap reports whether two individual globs could match a common path.
// It validates both patterns and then compares their literal prefixes: if one
// literal prefix is a path-prefix of the other, the globs can converge. A glob
// that is effectively a top-level wildcard (e.g. "**", "*") overlaps anything.
func globsOverlap(ga, gb string) bool {
	if !doublestar.ValidatePattern(ga) || !doublestar.ValidatePattern(gb) {
		// Malformed pattern: be conservative and assume overlap.
		return true
	}
	// Direct pattern-matches-pattern shortcuts via the literal of each.
	la := literalPrefix(ga)
	lb := literalPrefix(gb)
	if la == "" || lb == "" {
		// One side begins with a wildcard segment → can match under any dir.
		return true
	}
	return pathPrefixCompatible(la, lb)
}

// literalPrefix returns the leading run of path segments in glob that contain no
// wildcard metacharacters. E.g. "internal/gh/**" → "internal/gh";
// "internal/runner/*/x" → "internal/runner"; "**/foo" → "".
func literalPrefix(glob string) string {
	var out []byte
	lastSlash := -1
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		if c == '*' || c == '?' || c == '[' || c == '{' {
			break
		}
		if c == '/' {
			lastSlash = len(out)
		}
		out = append(out, c)
	}
	// Trim to the last complete segment boundary if a wildcard interrupted a
	// segment (so "internal/runner/cod*" yields "internal/runner", not
	// "internal/runner/cod").
	s := string(out)
	if len(s) > 0 && s[len(s)-1] != '/' {
		// Did the scan stop mid-segment (i.e. before a slash)? If the full glob
		// continues with a wildcard in this segment, cut back to lastSlash.
		if lastSlash >= 0 && lastSlash < len(out) {
			// Only trim when the char after the literal prefix is a wildcard,
			// meaning this segment is partial.
			if len(s) < len(glob) {
				next := glob[len(s)]
				if next == '*' || next == '?' || next == '[' || next == '{' {
					s = s[:lastSlash]
				}
			}
		} else if len(s) < len(glob) {
			// No slash seen and a wildcard follows → partial first segment.
			next := glob[len(s)]
			if next == '*' || next == '?' || next == '[' || next == '{' {
				s = ""
			}
		}
	}
	return trimTrailingSlash(s)
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// pathPrefixCompatible reports whether path a and path b are equal or one is an
// ancestor directory of the other (segment-aligned). "internal/gh" and
// "internal/gh" → true; "internal" and "internal/gh" → true; "internal/gh" and
// "internal/store" → false.
func pathPrefixCompatible(a, b string) bool {
	if a == b {
		return true
	}
	shorter, longer := a, b
	if len(a) > len(b) {
		shorter, longer = b, a
	}
	if len(shorter) == 0 {
		return true
	}
	if len(longer) > len(shorter) && longer[:len(shorter)] == shorter && longer[len(shorter)] == '/' {
		return true
	}
	return false
}
