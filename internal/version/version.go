// Package version exposes the build version, stamped at link time via ldflags.
package version

// Version is the clex build version. It defaults to "dev" and is overridden at
// build time with -ldflags "-X github.com/reissui/clex/internal/version.Version=...".
var Version = "dev"
