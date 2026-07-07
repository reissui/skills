package codex

import (
	"context"
	"os"
	"strings"
	"testing"
)

// A healthy fake (version ok, login ok) probes healthy with the version in
// Detail.
func TestProbe_Healthy(t *testing.T) {
	fake := fakeCodexPath(t)
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"FAKE_CODEX_VERSION=9.9.9",
	}
	a := New("gpt-5-5", WithBinary(fake), WithEnv(env))

	av, err := a.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !av.Healthy {
		t.Errorf("expected healthy, got %+v", av)
	}
	if !strings.Contains(av.Detail, "9.9.9") {
		t.Errorf("Detail should carry version, got %q", av.Detail)
	}
}

// A login failure (rate-limit/auth) probes unhealthy, not an error return.
func TestProbe_AuthFailure(t *testing.T) {
	fake := fakeCodexPath(t)
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"FAKE_CODEX_LOGIN_FAIL=1",
	}
	a := New("gpt-5-5", WithBinary(fake), WithEnv(env))

	av, err := a.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe returned error, want unhealthy availability: %v", err)
	}
	if av.Healthy {
		t.Errorf("expected unhealthy on auth failure, got %+v", av)
	}
	if !strings.Contains(av.Detail, "authenticated") {
		t.Errorf("Detail should mention auth, got %q", av.Detail)
	}
}

// A missing/broken binary probes unhealthy (version command fails).
func TestProbe_VersionFailure(t *testing.T) {
	a := New("gpt-5-5", WithBinary("/nonexistent/codex-binary"), WithEnv([]string{"PATH=/usr/bin"}))
	av, err := a.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe returned error, want unhealthy availability: %v", err)
	}
	if av.Healthy {
		t.Errorf("expected unhealthy for missing binary, got %+v", av)
	}
}

func TestProbe_NoBinary(t *testing.T) {
	a := &Adapter{}
	av, err := a.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if av.Healthy {
		t.Errorf("expected unhealthy with no binary, got %+v", av)
	}
}
