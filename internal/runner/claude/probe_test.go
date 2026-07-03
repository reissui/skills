package claude

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestProbe drives Probe through the fake CLI for the healthy, rate-limited,
// auth-failed, and not-installed cases, asserting the resulting Availability.
// The probe fake reads its directives from CLEX_FAKE_* env vars, which the
// adapter's allowlist would normally strip — so the test injects them AFTER the
// strict filter via withEnvFunc/strictThenExtra, keeping the hygiene path real.
func TestProbe(t *testing.T) {
	probeFake := fixture(t, "fake-claude-probe.sh")

	tests := []struct {
		name        string
		binary      string
		fixture     string
		exit        string
		wantHealthy bool
		detailHas   string
	}{
		{
			name:        "healthy",
			binary:      probeFake,
			fixture:     fixture(t, "probe-healthy.jsonl"),
			exit:        "0",
			wantHealthy: true,
			detailHas:   "Claude Code",
		},
		{
			name:        "rate limited",
			binary:      probeFake,
			fixture:     fixture(t, "probe-ratelimited.jsonl"),
			exit:        "1",
			wantHealthy: false,
			detailHas:   "usage limit",
		},
		{
			name:        "auth failed",
			binary:      probeFake,
			fixture:     fixture(t, "probe-authfail.jsonl"),
			exit:        "1",
			wantHealthy: false,
			detailHas:   "login",
		},
		{
			name:        "not installed",
			binary:      filepath.Join(t.TempDir(), "no-such-claude"),
			fixture:     "",
			exit:        "0",
			wantHealthy: false,
			detailHas:   "not runnable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			extra := []string{"CLEX_FAKE_VERSION=2.1.0 (Claude Code)", "CLEX_FAKE_EXIT=" + tc.exit}
			if tc.fixture != "" {
				extra = append(extra, "CLEX_FAKE_FIXTURE="+tc.fixture)
			}
			a := New(WithBinary(tc.binary), withEnvFunc(strictThenExtra(extra...)))

			got, err := a.Probe(context.Background())
			if err != nil {
				t.Fatalf("Probe returned error (should classify instead): %v", err)
			}
			if got.Healthy != tc.wantHealthy {
				t.Errorf("Healthy = %v, want %v (detail %q)", got.Healthy, tc.wantHealthy, got.Detail)
			}
			if tc.detailHas != "" && !strings.Contains(strings.ToLower(got.Detail), strings.ToLower(tc.detailHas)) {
				t.Errorf("Detail = %q, want it to contain %q", got.Detail, tc.detailHas)
			}
		})
	}
}
