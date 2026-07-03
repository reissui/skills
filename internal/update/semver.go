package update

import (
	"fmt"
	"strconv"
	"strings"
)

// semver is a parsed major.minor.patch version. clex release tags are simple
// three-part semantic versions (optionally prefixed with "v" and optionally
// carrying a "-prerelease" suffix), so a hand-rolled parser is used here rather
// than pulling in golang.org/x/mod/semver: the whole comparison surface is
// parse + order, the input space is our own release tags, and keeping
// internal/update dependency-free (stdlib only) keeps the test suite trivial and
// the supply-chain footprint small (spec: Supply chain — fewer moving parts).
type semver struct {
	major, minor, patch int
	// pre is the prerelease suffix without the leading '-', or "" for a final
	// release. A prerelease sorts *below* the same major.minor.patch final
	// release (0.4.0-rc1 < 0.4.0), matching SemVer §11.
	pre string
}

// parseSemver parses a version string like "v0.4.1" or "1.2.3-rc1". A leading
// "v" is optional. Build metadata ("+…") is ignored. It returns an error for
// anything that is not three dot-separated non-negative integers.
func parseSemver(s string) (semver, error) {
	orig := s
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	// Drop build metadata: it never affects precedence (SemVer §10).
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var pre string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, fmt.Errorf("update: %q is not a major.minor.patch version", orig)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, fmt.Errorf("update: %q has a non-numeric component %q", orig, p)
		}
		nums[i] = n
	}
	return semver{major: nums[0], minor: nums[1], patch: nums[2], pre: pre}, nil
}

// compare returns -1 if a < b, 0 if equal, +1 if a > b, using SemVer precedence
// (numeric fields first, then a prerelease sorting below its final release).
// Prerelease identifier ordering beyond "present vs absent" is not needed for
// clex tags, so two differing prerelease strings compare lexically as a stable
// tiebreak.
func (a semver) compare(b semver) int {
	if c := cmpInt(a.major, b.major); c != 0 {
		return c
	}
	if c := cmpInt(a.minor, b.minor); c != 0 {
		return c
	}
	if c := cmpInt(a.patch, b.patch); c != 0 {
		return c
	}
	switch {
	case a.pre == "" && b.pre == "":
		return 0
	case a.pre == "": // a is final, b is prerelease → a > b
		return 1
	case b.pre == "": // a is prerelease, b is final → a < b
		return -1
	case a.pre < b.pre:
		return -1
	case a.pre > b.pre:
		return 1
	default:
		return 0
	}
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// Bump classifies the size of an upgrade from one version to another. It drives
// the auto-apply decision: a BumpPatch may auto-stage under update.auto="patch",
// anything larger requires an owner confirm (spec: Self-update — layer 1).
type Bump string

const (
	BumpNone  Bump = "none"  // to <= from: no upgrade
	BumpPatch Bump = "patch" // only the patch component increased
	BumpMinor Bump = "minor" // the minor component increased
	BumpMajor Bump = "major" // the major component increased
)

// bumpBetween reports the upgrade class from from→to. If to is not strictly
// greater than from it returns BumpNone (same or older is a no-op).
func bumpBetween(from, to semver) Bump {
	if to.compare(from) <= 0 {
		return BumpNone
	}
	switch {
	case to.major != from.major:
		return BumpMajor
	case to.minor != from.minor:
		return BumpMinor
	default:
		return BumpPatch
	}
}
