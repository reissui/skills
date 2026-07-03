package claude

import "os"

// withEnvFunc overrides how the child environment is built. Test-only: the
// production allowlist would strip the fake binary's control variables, so
// Probe tests (whose fake runs outside a controlled working directory) append
// those control vars AFTER the strict filter, preserving real hygiene while
// still steering the fake.
func withEnvFunc(fn func() []string) Option {
	return func(a *Adapter) { a.envFunc = fn }
}

// strictThenExtra returns the production child env with extra entries appended,
// so a fake can still be driven while the allowlist behavior stays exercised.
func strictThenExtra(extra ...string) func() []string {
	return func() []string {
		return append(childEnv(os.Environ()), extra...)
	}
}
