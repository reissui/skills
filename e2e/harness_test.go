//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reissui/clex/internal/config"
	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/daemon"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/pipeline"
	"github.com/reissui/clex/internal/registry"
	"github.com/reissui/clex/internal/store"
	"github.com/reissui/clex/internal/telegram"
	"github.com/reissui/clex/internal/workspace"
)

// fakeModel ids and the fake provider. Two models let build route to a mid-tier
// model and review route to a mandatory top-tier reviewer (spec: Review policy —
// a below-top author gets a mandatory top-tier review), all executed by the same
// scripted fake runner.
const (
	fakeProvider  = "fake"
	fakeKind      = "fake"
	buildModelID  = "fake-build"  // mid tier → build pool
	reviewModelID = "fake-review" // top tier → reviewer + plan/lint
)

// testRepo is the single managed repository for the suite.
var testRepo = gh.Repo{Owner: "acme", Name: "widgets"}

// captureTG is a TelegramPort double that records every SendLine and answers
// cost-gate Asks by confirming (there are no metered models here, so Ask is not
// expected, but a confirm keeps the flow unblocked if one ever fires).
type captureTG struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureTG) SendLine(ctx context.Context, text string) (int, error) {
	c.mu.Lock()
	c.lines = append(c.lines, text)
	c.mu.Unlock()
	return len(c.lines), nil
}

func (c *captureTG) Ask(ctx context.Context, q telegram.Question) (telegram.Answer, error) {
	// Accept the proposed answer (✓). No metered models exist in the e2e config,
	// so a cost-gate Ask is not expected; this keeps the flow unblocked if one
	// ever fires.
	return telegram.Answer{Text: q.Proposal}, nil
}

func (c *captureTG) Handle(name string, h telegram.CommandHandler) {}

func (c *captureTG) OnText(fn func(ctx context.Context, text string, replyToMsgID int)) {}

func (c *captureTG) sent() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}

func (c *captureTG) contains(sub string) bool {
	for _, l := range c.sent() {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// harness bundles everything an end-to-end scenario drives.
type harness struct {
	t        *testing.T
	home     string // clex home (~/.clex equivalent): worktree root + db
	repoDir  string // primary checkout
	bareDir  string // bare origin
	gh       *fakeGitHub
	ghc      *gh.Client
	cfg      *config.Config
	st       *store.Store
	tg       *captureTG
	fakeBin  string
	spoolDir string
	log      *slog.Logger
}

// newHarness sets up the git repos, the fake GitHub server + real gh client,
// config, store, and the compiled fake runner. The daemon is started separately
// (startDaemon) so a test can seed plan output first.
func newHarness(t *testing.T) *harness {
	t.Helper()

	root := t.TempDir()
	home := filepath.Join(root, "home")
	bareDir := filepath.Join(root, "origin.git")
	// The daemon's pipeline resolves the primary checkout to <home>/repos/<name>
	// (daemon.repoDirFor); the scratch checkout MUST live there so the workspace
	// manager's worktree/branch operations and the fake GitHub's merges act on the
	// same repository.
	repoDir := filepath.Join(home, "repos", testRepo.Name)
	mergeDir := filepath.Join(root, "merge-checkout")
	spoolDir := filepath.Join(root, "scripts")

	if _, err := daemon.EnsureHome(home); err != nil {
		t.Fatalf("ensure home: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(repoDir), 0o700); err != nil {
		t.Fatalf("mkdir repos: %v", err)
	}
	initGitRepos(t, bareDir, repoDir)
	if err := os.MkdirAll(mergeDir, 0o700); err != nil {
		t.Fatalf("mkdir merge: %v", err)
	}

	fg := newFakeGitHub(t, repoDir, mergeDir)
	srv := httptest.NewServer(fg)
	t.Cleanup(srv.Close)

	ghc, err := gh.New("e2e-token", gh.WithBaseURL(srv.URL+"/"), gh.WithSelfLogin("clex-bot"))
	if err != nil {
		t.Fatalf("gh.New: %v", err)
	}

	logLevel := slog.LevelWarn
	if os.Getenv("CLEX_E2E_DEBUG") != "" {
		logLevel = slog.LevelInfo
	}
	logh := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))

	st, err := store.Open(daemon.DBPath(home))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	h := &harness{
		t:        t,
		home:     home,
		repoDir:  repoDir,
		bareDir:  bareDir,
		gh:       fg,
		ghc:      ghc,
		cfg:      e2eConfig(),
		st:       st,
		tg:       &captureTG{},
		fakeBin:  buildFakeRunner(t),
		spoolDir: spoolDir,
		log:      logh,
	}
	return h
}

// e2eConfig builds a config whose single fake provider backs two models: a
// mid-tier build model and a top-tier review/plan/lint model. This satisfies the
// registry's routing (build pool = local+mid; review/plan need a top tier).
func e2eConfig() *config.Config {
	c := &config.Config{
		WorktreeRoot: "",
		HeadBranch:   "main",
		Verification: "true",
		Providers: map[string]config.Provider{
			fakeProvider: {Kind: fakeKind},
		},
		Models: map[string]config.Model{
			buildModelID:  {Provider: fakeProvider, Billing: core.BillingSubscription},
			reviewModelID: {Provider: fakeProvider, Billing: core.BillingSubscription},
		},
		Tiers: core.TierMap{
			"mid": {buildModelID},
			"top": {reviewModelID},
		},
		Routing: map[string]config.Routing{
			string(core.RolePlan):   {Tier: "top"},
			string(core.RoleBuild):  {Tier: "mid"},
			string(core.RoleReview): {Tier: "top"},
			string(core.RoleLint):   {Tier: "top"},
			string(core.RoleBot):    {Tier: "top"},
		},
	}
	// Validate prunes dangling references and surfaces role-resolution warnings;
	// the config is already complete, so warnings are ignored here.
	_ = c.Validate()
	return c
}

// runnerBuilder returns a daemon.RunnerBuilder that hands every model a scripted
// fake runner. The script is chosen by the task (build vs review) via
// scriptForTask. This is the injection seam the brief mandates: FromConfig takes
// a custom RunnerBuilder so the daemon routes any role to the fake without
// editing internal/daemon.
func (h *harness) runnerBuilder() daemon.RunnerBuilder {
	return func(modelID string, prov config.Provider) (core.Runner, error) {
		return &scriptRunner{
			bin:       h.fakeBin,
			spool:     h.spoolDir,
			scriptFor: scriptForTask,
		}, nil
	}
}

// planPipeline builds a real *pipeline.Pipeline wired to the fake GitHub, a real
// workspace manager over the scratch repo, a real registry, real skills, and a
// planner/lint fake runner. It is used to drive the Plan stage directly, because
// the daemon loop only dispatches builds — Plan is a CLI-invoked stage in
// production. Everything downstream (build/review/assemble) runs through the
// daemon.
func (h *harness) planPipeline() *pipeline.Pipeline {
	reg := registry.New(h.cfg, map[string]core.Runner{
		fakeProvider: &scriptRunner{bin: h.fakeBin, spool: h.spoolDir, scriptFor: scriptForTask},
	})
	rf := pipeline.RunnerFactoryFunc(func(model core.Model) (pipeline.Runner, error) {
		return &scriptRunner{bin: h.fakeBin, spool: h.spoolDir, scriptFor: scriptForTask}, nil
	})
	return pipeline.New(pipeline.Deps{
		GH:      h.ghc,
		WS:      workspace.New(h.home, h.log),
		Router:  reg,
		Skills:  pipeline.SkillsAdapter(),
		Runners: rf,
	}, pipeline.Config{
		Repo:          testRepo,
		RepoDir:       h.repoDir,
		Owner:         "acme",
		SelfLogin:     "clex-bot",
		DefaultVerify: "true",
		TopTier:       h.cfg.Tiers["top"],
	})
}

// startDaemon constructs the daemon via FromConfig with the injected fake
// RunnerBuilder and runs it until the returned cancel is called. A small poll
// interval makes the reconcile ticker dispatch quickly.
func (h *harness) startDaemon(ctx context.Context) (*daemon.Daemon, context.CancelFunc) {
	dcfg := daemon.Config{
		Repo:          testRepo,
		Home:          h.home,
		Owner:         "acme",
		SelfLogin:     "clex-bot",
		DefaultVerify: "true",
		PollInterval:  50 * time.Millisecond,
	}
	d, err := daemon.FromConfig(h.cfg, dcfg, h.ghc, h.tg, h.st, h.log, h.runnerBuilder())
	if err != nil {
		h.t.Fatalf("FromConfig: %v", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	go func() { _ = d.Run(runCtx) }()
	return d, cancel
}

// itoa avoids importing strconv for one call.
func itoa(n int) string { return fmt.Sprintf("%d", n) }

// --- git repo setup ---

// gitIn runs a git command in dir and returns combined output (test assertions
// on branch topology use it).
func gitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// initGitRepos creates a bare origin and a primary checkout with an initial
// commit on main, wired with origin so fetch/push work like a real remote.
func initGitRepos(t *testing.T, bareDir, repoDir string) {
	t.Helper()
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=clex-test", "GIT_AUTHOR_EMAIL=test@clex.test",
			"GIT_COMMITTER_NAME=clex-test", "GIT_COMMITTER_EMAIL=test@clex.test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}
	if err := os.MkdirAll(bareDir, 0o755); err != nil {
		t.Fatalf("mkdir bare: %v", err)
	}
	run(bareDir, "init", "--bare", "-b", "main", ".")

	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir checkout: %v", err)
	}
	run(repoDir, "init", "-b", "main", ".")
	// Local identity so commits in this repo work regardless of global config.
	run(repoDir, "config", "user.email", "test@clex.test")
	run(repoDir, "config", "user.name", "clex-test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# scratch\n"), 0o644); err != nil {
		t.Fatalf("seed readme: %v", err)
	}
	run(repoDir, "add", "-A")
	run(repoDir, "commit", "-m", "initial")
	run(repoDir, "remote", "add", "origin", bareDir)
	run(repoDir, "push", "-u", "origin", "main")
}
