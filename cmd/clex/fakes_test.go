package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
)

// newTestEnv builds a fully-faked env rooted at a temp home. Every outside-world
// hook is a fake so no command touches a live service. Tests tweak the returned
// env's fields (probe scripts, gh recorder, telegram result) before dispatching.
func newTestEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	fgh := newFakeGH()
	return &env{
		stdin:        &bytes.Buffer{},
		stdout:       &bytes.Buffer{},
		stderr:       &bytes.Buffer{},
		home:         home,
		now:          func() time.Time { return time.Unix(0, 0).UTC() },
		dialTimeout:  100 * time.Millisecond,
		probe:        newFakeProbe(allHealthy()),
		newGH:        fgh.factory(),
		telegram:     &fakeTelegram{verify: telegramResult{Valid: true, BotUsername: "clextestbot"}},
		service:      &fakeService{},
		goos:         "darwin",
		ghToken:      func(context.Context) (string, error) { return "test-token", nil },
		originRemote: func() (string, error) { return "git@github.com:acme/widgets.git", nil },
	}
}

// outBuf / errBuf return the env's buffers (they are always bytes.Buffer in tests).
func outBuf(e *env) *bytes.Buffer { return e.stdout.(*bytes.Buffer) }
func errBuf(e *env) *bytes.Buffer { return e.stderr.(*bytes.Buffer) }

// ---- fake dep probe ---------------------------------------------------------

// fakeProbe returns scripted depResults by name.
type fakeProbe struct {
	results map[string]depResult
}

func newFakeProbe(results map[string]depResult) *fakeProbe {
	return &fakeProbe{results: results}
}

func (p *fakeProbe) Probe(_ context.Context, name string) depResult {
	if r, ok := p.results[name]; ok {
		r.Name = name
		return r
	}
	return depResult{Name: name}
}

// allHealthy is the default probe script: every tool found, authed, versioned.
func allHealthy() map[string]depResult {
	return map[string]depResult{
		"claude": {Found: true, Authed: true, Version: "claude 1.2.3"},
		"codex":  {Found: true, Authed: true, Version: "codex 0.9.0"},
		"gh":     {Found: true, Authed: true, Version: "gh 2.40.0"},
		"ollama": {Found: true, Authed: true, Version: "ollama 0.1.0"},
	}
}

// ---- fake GitHub client -----------------------------------------------------

// fakeGH is an in-memory ghClient. It records label/issue/state calls for
// assertions and returns scripted token scopes and branch-protection answers.
type fakeGH struct {
	mu sync.Mutex

	ensureLabelsCalls int
	createdIssues     []createdIssue
	setStateCalls     []setStateCall
	nextIssueNumber   int

	scopes      []string
	scopesErr   error
	protected   bool
	protectErr  error
	createErr   error
	setStateErr error
	labelsErr   error
}

type createdIssue struct {
	repo   gh.Repo
	title  string
	body   string
	labels []string
}

type setStateCall struct {
	repo   gh.Repo
	number int
	to     core.State
}

func newFakeGH() *fakeGH {
	return &fakeGH{nextIssueNumber: 100}
}

// factory returns a ghFactory closing over this fake, ignoring the token.
func (f *fakeGH) factory() ghFactory {
	return func(string) (ghClient, error) { return f, nil }
}

func (f *fakeGH) EnsureLabels(_ context.Context, _ gh.Repo, _ []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureLabelsCalls++
	return f.labelsErr
}

func (f *fakeGH) CreateIssue(_ context.Context, repo gh.Repo, title, body string, labels []string) (*gh.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextIssueNumber++
	f.createdIssues = append(f.createdIssues, createdIssue{repo, title, body, labels})
	return &gh.Issue{Number: f.nextIssueNumber, Title: title, Body: body, Labels: labels}, nil
}

func (f *fakeGH) SetState(_ context.Context, repo gh.Repo, number int, to core.State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setStateErr != nil {
		return f.setStateErr
	}
	f.setStateCalls = append(f.setStateCalls, setStateCall{repo, number, to})
	return nil
}

func (f *fakeGH) TokenScopes(context.Context) ([]string, error) { return f.scopes, f.scopesErr }

func (f *fakeGH) BranchProtected(context.Context, gh.Repo, string) (bool, error) {
	return f.protected, f.protectErr
}

// ---- fake Telegram verifier -------------------------------------------------

// fakeTelegram returns scripted verify/bind results and records whether Bind ran.
type fakeTelegram struct {
	verify   telegramResult
	bind     telegramResult
	bindCall int
}

func (t *fakeTelegram) Verify(context.Context, string) telegramResult { return t.verify }

func (t *fakeTelegram) Bind(context.Context, string) telegramResult {
	t.bindCall++
	return t.bind
}

// ---- fake service manager ---------------------------------------------------

// fakeService records install/uninstall/status calls and the rendered content so
// golden tests can assert the unit while the load step is a no-op.
type fakeService struct {
	installCalls   int
	uninstallCalls int
	lastGOOS       string
	lastPath       string
	lastContent    string
	installErr     error
	loaded         bool
	statusErr      error
}

func (s *fakeService) Install(goos, path, content string) error {
	s.installCalls++
	s.lastGOOS, s.lastPath, s.lastContent = goos, path, content
	return s.installErr
}

func (s *fakeService) Uninstall(goos, path string) error {
	s.uninstallCalls++
	s.lastGOOS, s.lastPath = goos, path
	return nil
}

func (s *fakeService) Status(goos, path string) (bool, string, error) {
	s.lastGOOS, s.lastPath = goos, path
	return s.loaded, "", s.statusErr
}

// socketPathIn returns the socket path under home for tests that spin a stub
// daemon at a known location.
func socketPathIn(home string) string { return filepath.Join(home, "clexd.sock") }

// mustFake extracts the *fakeGH a test ghFactory closes over, so a test can tweak
// its scripted answers after newTestEnv. It fails the test if the factory does
// not yield a *fakeGH.
func (f ghFactory) mustFake(t *testing.T) *fakeGH {
	t.Helper()
	c, err := f("test-token")
	if err != nil {
		t.Fatalf("ghFactory returned error: %v", err)
	}
	fake, ok := c.(*fakeGH)
	if !ok {
		t.Fatalf("ghFactory did not yield *fakeGH, got %T", c)
	}
	return fake
}

// writeFile is a tiny test helper writing s to path (0600).
func writeFile(path, s string) error {
	return os.WriteFile(path, []byte(s), 0o600)
}
