package version

import "testing"

// TestVersionDefault ensures the version variable is populated so that a build
// without ldflags still reports a usable string.
func TestVersionDefault(t *testing.T) {
	if Version == "" {
		t.Fatal("version.Version must not be empty")
	}
}
