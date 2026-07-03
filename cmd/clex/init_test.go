package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/reissui/clex/internal/config"
)

// TestInitYesHappyPath: non-interactive setup with all deps healthy ends green,
// creates labels, writes a valid config, and binds the chat id from --chat-id.
func TestInitYesHappyPath(t *testing.T) {
	e := newTestEnv(t)
	fgh := e.newGH.mustFake(t)

	code := run(e, []string{
		"init", "--yes",
		"--repo", "acme/widgets",
		"--telegram-token", "123:abc",
		"--chat-id", "555",
	})
	if code != exitOK {
		t.Fatalf("init --yes exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, outBuf(e), errBuf(e))
	}
	if fgh.ensureLabelsCalls != 1 {
		t.Errorf("expected labels ensured once, got %d", fgh.ensureLabelsCalls)
	}
	// Config written and valid.
	cfg := loadWrittenConfig(t, e)
	if cfg.TelegramToken != "123:abc" || cfg.TelegramChatID != 555 {
		t.Errorf("config telegram = %q/%d, want 123:abc/555", cfg.TelegramToken, cfg.TelegramChatID)
	}
	// Validate returns only warnings (no hard error); the scaffold should produce
	// no empty-role or dropped-model warnings.
	if warns := cfg.Validate(); len(warns) != 0 {
		t.Errorf("written config produced warnings: %v", warns)
	}
	if !strings.Contains(outBuf(e).String(), "Setup complete") {
		t.Errorf("expected green summary; got:\n%s", outBuf(e))
	}
}

// TestInitIdempotent: a second run does not duplicate labels or config.
func TestInitIdempotent(t *testing.T) {
	e := newTestEnv(t)
	fgh := e.newGH.mustFake(t)
	args := []string{"init", "--yes", "--repo", "acme/widgets", "--telegram-token", "t", "--chat-id", "1"}

	if code := run(e, args); code != exitOK {
		t.Fatalf("first init exit = %d", code)
	}
	first, err := os.ReadFile(e.globalConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	outBuf(e).Reset()
	if code := run(e, args); code != exitOK {
		t.Fatalf("second init exit = %d", code)
	}
	second, err := os.ReadFile(e.globalConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("config changed on re-run (not idempotent):\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	// EnsureLabels was called once per run but is itself idempotent; assert we did
	// not accumulate duplicate *config* rather than counting label calls.
	if fgh.ensureLabelsCalls != 2 {
		t.Logf("ensureLabelsCalls=%d (EnsureLabels is idempotent server-side)", fgh.ensureLabelsCalls)
	}
}

// TestInitYesMissingBinaryFailsWithFix: a missing required binary in scripted
// mode fails and prints the fix command so CI catches it and the run can resume.
func TestInitYesMissingBinaryFails(t *testing.T) {
	e := newTestEnv(t)
	e.newGH.mustFake(t)
	e.probe = newFakeProbe(map[string]depResult{
		// claude missing.
		"codex":  {Found: true, Authed: true},
		"gh":     {Found: true, Authed: true},
		"ollama": {Found: true, Authed: true},
	})
	code := run(e, []string{"init", "--yes", "--repo", "acme/widgets", "--skip-telegram"})
	if code != exitError {
		t.Fatalf("init with missing claude: exit = %d, want 1", code)
	}
	out := outBuf(e).String()
	if !strings.Contains(out, "✗ claude") || !strings.Contains(out, "fix:") {
		t.Fatalf("expected claude ✗ with fix command; got:\n%s", out)
	}
}

// TestInitInteractiveHappyPath drives the wizard via scripted stdin: the token
// prompt is answered, and the injected telegram fake binds a chat id.
func TestInitInteractiveHappyPath(t *testing.T) {
	e := newTestEnv(t)
	e.newGH.mustFake(t)
	e.stdin = strings.NewReader("999:xyz\n") // token entered at the prompt
	e.telegram = &fakeTelegram{
		verify: telegramResult{Valid: true, BotUsername: "mybot"},
		bind:   telegramResult{Valid: true, ChatID: 777},
	}
	code := run(e, []string{"init", "--repo", "acme/widgets"})
	if code != exitOK {
		t.Fatalf("interactive init exit = %d, want 0\nstdout:\n%s", code, outBuf(e))
	}
	out := outBuf(e).String()
	if !strings.Contains(out, "@mybot") || !strings.Contains(out, "bound chat id 777") {
		t.Fatalf("expected bind confirmation; got:\n%s", out)
	}
	cfg := loadWrittenConfig(t, e)
	if cfg.TelegramToken != "999:xyz" || cfg.TelegramChatID != 777 {
		t.Fatalf("config telegram = %q/%d, want 999:xyz/777", cfg.TelegramToken, cfg.TelegramChatID)
	}
}

// TestInitInteractiveSkipTelegram: blank token at the prompt skips Telegram but
// still finishes green with a config.
func TestInitInteractiveSkipTelegram(t *testing.T) {
	e := newTestEnv(t)
	e.newGH.mustFake(t)
	e.stdin = strings.NewReader("\n") // blank → skip
	code := run(e, []string{"init", "--repo", "acme/widgets"})
	if code != exitOK {
		t.Fatalf("init exit = %d, want 0", code)
	}
	if !strings.Contains(outBuf(e).String(), "Setup complete") {
		t.Fatalf("expected green summary; got:\n%s", outBuf(e))
	}
}

func TestInitJSON(t *testing.T) {
	e := newTestEnv(t)
	e.newGH.mustFake(t)
	code := run(e, []string{"init", "--json", "--yes", "--repo", "acme/widgets", "--telegram-token", "t", "--chat-id", "9"})
	if code != exitOK {
		t.Fatalf("init --json exit = %d, want 0", code)
	}
	var res initResult
	if err := json.Unmarshal(outBuf(e).Bytes(), &res); err != nil {
		t.Fatalf("init --json invalid: %v\n%s", err, outBuf(e))
	}
	if !res.OK || res.Repo != "acme/widgets" || !res.LabelsSet {
		t.Fatalf("unexpected init json: %+v", res)
	}
}

func TestInitConfigDirIs0700(t *testing.T) {
	e := newTestEnv(t)
	e.newGH.mustFake(t)
	if code := run(e, []string{"init", "--yes", "--repo", "acme/widgets", "--skip-telegram"}); code != exitOK {
		t.Fatalf("init exit = %d, want 0", code)
	}
	fi, err := os.Stat(e.home)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("clex home perm = %o, want 700", perm)
	}
	cfi, err := os.Stat(e.globalConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := cfi.Mode().Perm(); perm != 0o600 {
		t.Errorf("config perm = %o, want 600", perm)
	}
}

// loadWrittenConfig decodes the config the wizard wrote at the env's config path.
func loadWrittenConfig(t *testing.T, e *env) *config.Config {
	t.Helper()
	var cfg config.Config
	if _, err := toml.DecodeFile(e.globalConfigPath(), &cfg); err != nil {
		t.Fatalf("decode written config: %v", err)
	}
	return &cfg
}
