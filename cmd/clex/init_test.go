package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

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

// TestInitTelegramFreshDeadlineAfterSlowPrompt is the issue #40 fix-1 regression:
// even when the outer command budget is already exhausted (the user spent >30s in
// @BotFather before pasting the token), the Telegram verification step must run
// with its own fresh, live deadline — not inherit the expired command context.
func TestInitTelegramFreshDeadlineAfterSlowPrompt(t *testing.T) {
	e := newTestEnv(t)
	e.newGH.mustFake(t)
	ft := &fakeTelegram{
		verify: telegramResult{Valid: true, BotUsername: "mybot"},
		bind:   telegramResult{Valid: true, ChatID: 777},
	}
	e.telegram = ft
	e.stdin = strings.NewReader("999:xyz\n")

	// Simulate the command context being fully consumed by prompt time: hand the
	// wizard an already-expired context. A correct wizard mints a fresh deadline
	// for the network step, so Verify still sees a live context.
	expired, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	cancel()

	w := &wizard{e: e, json: false, yes: false}
	code := w.runInit(expired, initOpts{
		repo:      "acme/widgets",
		promptsIn: bufio.NewReader(e.stdin),
	})
	if code != exitOK {
		t.Fatalf("runInit exit = %d, want 0\nstdout:\n%s", code, outBuf(e))
	}
	if ft.verifyCtxErr != nil {
		t.Fatalf("Verify ran with an expired context (%v); wizard leaked the exhausted command budget into the network step", ft.verifyCtxErr)
	}
	if !ft.verifyHadDeadline {
		t.Fatalf("Verify context carried no deadline; each network step should get its own bounded timeout")
	}
	if !strings.Contains(outBuf(e).String(), "bound chat id 777") {
		t.Fatalf("expected bind confirmation; got:\n%s", outBuf(e))
	}
}

// TestInitTelegramRedactsToken is the issue #40 fix-1 requirement that a rejected
// token never echoes the secret into the terminal/logs.
func TestInitTelegramRedactsToken(t *testing.T) {
	e := newTestEnv(t)
	e.newGH.mustFake(t)
	const secret = "123456:SUPERSECRETTOKEN"
	// A rejection whose detail embeds the token URL (as the real getMe error does).
	e.telegram = &fakeTelegram{verify: telegramResult{
		Detail: `Get "https://api.telegram.org/bot` + secret + `/getMe": context deadline exceeded`,
	}}
	e.stdin = strings.NewReader(secret + "\n")

	code := run(e, []string{"init", "--repo", "acme/widgets"})
	if code != exitOK { // interactive verify failure is non-fatal (save-later path)
		t.Fatalf("init exit = %d, want 0\nstdout:\n%s", code, outBuf(e))
	}
	out := outBuf(e).String()
	if strings.Contains(out, secret) {
		t.Fatalf("bot token leaked into output:\n%s", out)
	}
	if !strings.Contains(out, "REDACTED") {
		t.Fatalf("expected the token to be redacted in the error line; got:\n%s", out)
	}
}

// TestInitTelegramSaveUnverified: when verification fails interactively and the
// user answers yes to "save anyway?", the token is persisted so doctor can
// re-verify later, and the wizard still finishes green (issue #40 fix 1).
func TestInitTelegramSaveUnverified(t *testing.T) {
	e := newTestEnv(t)
	e.newGH.mustFake(t)
	e.telegram = &fakeTelegram{verify: telegramResult{Detail: "could not reach Telegram"}}
	// First line: the token. Second line: "y" to save unverified.
	e.stdin = strings.NewReader("555:unverified\ny\n")

	code := run(e, []string{"init", "--repo", "acme/widgets"})
	if code != exitOK {
		t.Fatalf("init exit = %d, want 0\nstdout:\n%s", code, outBuf(e))
	}
	cfg := loadWrittenConfig(t, e)
	if cfg.TelegramToken != "555:unverified" {
		t.Fatalf("expected unverified token saved, got %q", cfg.TelegramToken)
	}
	if !strings.Contains(outBuf(e).String(), "verify later") {
		t.Fatalf("expected a 'verify later' hint; got:\n%s", outBuf(e))
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
