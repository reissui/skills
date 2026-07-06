// Package daemon is the clexd event loop: it composes the scheduler, pipeline,
// registry, store, GitHub poller, and Telegram transport into a single
// long-running supervisor. GitHub labels are the source of truth; the daemon is
// crash-safe because it re-derives all in-flight state from labels on startup
// and drives every dispatch through idempotent pipeline stages.
//
// Concurrency model. One goroutine owns all mutable pipeline state (the "loop"
// in Run). External inputs — GitHub poller changes, Telegram commands, IPC
// control requests, and runner-completion events — are funneled onto a single
// internal channel and processed serially, so the core state machine needs no
// locks. Runner executions happen in their own goroutines and report back by
// sending a completion event onto that same channel. A small mutex guards only
// the fields the IPC/Telegram handlers read concurrently for status (the pause
// flag and the running-set snapshot).
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/reissui/clex/internal/config"
	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/pipeline"
	"github.com/reissui/clex/internal/registry"
	"github.com/reissui/clex/internal/scheduler"
	"github.com/reissui/clex/internal/store"
)

// DefaultPollInterval is how often the daemon polls GitHub when the config does
// not override it. Conditional requests (ETags) keep unchanged polls cheap.
const DefaultPollInterval = 30 * time.Second

// DefaultMaxParallel bounds total concurrent runners when no provider caps or
// override constrain it further. Keeps a confused planner from fan-out storms.
const DefaultMaxParallel = 3

// maxAutoRetries is the number of automatic build retries before a build failure
// escalates to a stronger model (spec: Error handling — "capped automatic
// retries (1)"). After this many failed verifications the daemon escalates
// exactly once, carrying the failed diff forward.
const maxAutoRetries = 1

// Deps are the injected collaborators the daemon composes. Real scheduler /
// pipeline / store are used in production and in integration tests; gh and
// telegram are ports so tests supply in-memory fakes (spec: zero live
// services). All fields are required unless noted.
type Deps struct {
	// GH is the GitHub port (poller, issue reads, label writes).
	GH GitHubPort
	// TG is the Telegram port (status lines, cost-gate Ask, command handlers).
	TG TelegramPort
	// Stages is the pipeline (Plan/Build/Review/Assemble). Usually the real
	// *pipeline.Pipeline.
	Stages Stages
	// Registry picks models and adjudicates cost gates.
	Registry *registry.Registry
	// Store records sessions, usage, and the event log.
	Store *store.Store
	// RunnerFactory maps a model to its runner, used to execute a build/steer
	// turn. In production this is the daemon's runnerFactory; tests inject a
	// scripted factory whose fake runners assert the child env allowlist.
	RunnerFactory pipeline.RunnerFactory
}

// Config holds the daemon's per-run knobs, most sourced from the resolved
// clex config plus the pieces the loop needs directly.
type Config struct {
	// Repo is the single managed repository.
	Repo gh.Repo
	// Home is the resolved clex home directory (~/.clex), already created 0700.
	Home string
	// Owner is the trusted human GitHub login.
	Owner string
	// SelfLogin is clex's own GitHub login (trusted like the owner).
	SelfLogin string
	// PollInterval overrides DefaultPollInterval when non-zero.
	PollInterval time.Duration
	// MaxParallel overrides DefaultMaxParallel when non-zero.
	MaxParallel int
	// Caps are per-provider concurrency caps (provider name → max).
	Caps map[string]int
	// DefaultVerify is the repo default verification command.
	DefaultVerify string
	// EpicVerify is the epic-level verification command (repo default unless an
	// epic overrides it).
	EpicVerify string
	// AutoMergeFinalPR enables auto-merge of the final epic PR (default off).
	AutoMergeFinalPR bool
	// MaxUSDPerEpic mirrors config.Budget for gate bookkeeping display.
	MaxUSDPerEpic float64
}

// runState tracks one in-flight build: its cancel func (for /stop and shutdown),
// the session for resume-on-steer, and enough context to re-dispatch on
// escalation carrying the prior diff forward.
type runState struct {
	issue     int
	provider  string
	model     core.Model
	stage     string
	sessionID string // runner CLI session id, for resume on steer/escalation
	cancel    context.CancelFunc
	// failures counts verification failures observed for this issue in this
	// daemon lifetime; at maxAutoRetries+1 the daemon escalates.
	failures int
	// escalated reports that this issue has ALREADY been escalated once. It is
	// carried across re-dispatches so a failing escalated build does not escalate
	// a second time (spec: exactly one escalation re-dispatch) — it goes to a
	// human decision instead.
	escalated bool
}

// Daemon is the clexd supervisor. Construct with New; drive with Run.
type Daemon struct {
	deps Deps
	cfg  Config
	log  *slog.Logger
	red  *Redactor

	// events is the single serialized input channel to the loop.
	events chan loopEvent
	// stopped is closed when the loop is tearing down, so a backpressure fallback
	// send in enqueue can abandon a parked event instead of leaking forever.
	stopped chan struct{}

	// mu guards paused and running for concurrent status reads from IPC /
	// Telegram handlers. The loop goroutine mutates them under this lock too so
	// snapshots are consistent.
	mu      sync.Mutex
	paused  bool
	running map[int]*runState // issue number → in-flight build
	// chat is the Telegram free-text conversation state (model override, CLI
	// session id, busy flag), guarded by mu like the other handler-read fields.
	chat chatState
	// planFailed marks ideas whose planning failed this daemon lifetime, so
	// reconcile does not hot-loop a permanent failure. Cleared by /steer on the
	// idea (a deliberate retry) and by restart.
	planFailed map[int]bool
	// pendingGate is a human-readable description of a cost-gate confirm the
	// daemon is currently awaiting (empty when none). Consulted by the update
	// quiesce hook and status.
	pendingGate string

	startedAt time.Time
}

// New constructs a Daemon. It validates that required dependencies are present.
// The home directory must already exist (call EnsureHome first) so the daemon
// does not silently create it with the wrong permissions.
func New(deps Deps, cfg Config, log *slog.Logger, red *Redactor) (*Daemon, error) {
	if deps.GH == nil || deps.TG == nil || deps.Stages == nil || deps.Store == nil || deps.Registry == nil {
		return nil, fmt.Errorf("daemon: missing required dependency")
	}
	if cfg.Home == "" {
		return nil, fmt.Errorf("daemon: empty home")
	}
	if log == nil {
		log = slog.Default()
	}
	if red == nil {
		red = NewRedactor()
	}
	return &Daemon{
		deps:    deps,
		cfg:     cfg,
		log:     log,
		red:     red,
		events:  make(chan loopEvent, 64),
		stopped: make(chan struct{}),
		running: make(map[int]*runState),
	}, nil
}

// FromConfig builds the concrete production dependency graph (runner adapters,
// registry, pipeline, gh/telegram ports) from a resolved config, a GitHub
// client, a Telegram transport, and an open store. It is the wiring the clexd
// command uses; tests bypass it and inject Deps directly. build may be nil to
// use DefaultRunnerBuilder.
func FromConfig(cfg *config.Config, dcfg Config, ghc *gh.Client, tg TelegramPort, st *store.Store, log *slog.Logger, build RunnerBuilder) (*Daemon, error) {
	rf, err := newRunnerFactory(cfg, build)
	if err != nil {
		return nil, err
	}
	reg := registry.New(cfg, rf.providerRunners())
	pl := pipeline.New(pipeline.Deps{
		GH:      ghc,
		WS:      workspaceManager(dcfg.Home, log),
		Router:  reg,
		Skills:  pipeline.SkillsAdapter(),
		Runners: rf,
	}, pipeline.Config{
		Repo:           dcfg.Repo,
		RepoDir:        repoDirFor(dcfg),
		UserSkillsRoot: dcfg.Home + "/skills",
		Owner:          dcfg.Owner,
		SelfLogin:      dcfg.SelfLogin,
		DefaultVerify:  dcfg.DefaultVerify,
		TopTier:        cfg.Tiers["top"],
	})
	red := NewRedactor(cfg.TelegramToken)
	return New(Deps{
		GH:            NewGitHubPort(ghc),
		TG:            tg,
		Stages:        pl,
		Registry:      reg,
		Store:         st,
		RunnerFactory: rf,
	}, dcfg, log, red)
}

// pollInterval resolves the effective poll cadence.
func (d *Daemon) pollInterval() time.Duration {
	if d.cfg.PollInterval > 0 {
		return d.cfg.PollInterval
	}
	return DefaultPollInterval
}

// caps builds the scheduler caps from config: the global MaxParallel plus any
// per-provider limits.
func (d *Daemon) caps() scheduler.Caps {
	mp := d.cfg.MaxParallel
	if mp <= 0 {
		mp = DefaultMaxParallel
	}
	per := make(map[string]int, len(d.cfg.Caps))
	for k, v := range d.cfg.Caps {
		per[k] = v
	}
	return scheduler.Caps{MaxParallel: mp, PerProvider: per}
}

// isPaused reports the global pause flag (concurrency-safe).
func (d *Daemon) isPaused() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.paused
}

// setPaused sets the global pause flag and returns whether it changed.
func (d *Daemon) setPaused(p bool) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.paused == p {
		return false
	}
	d.paused = p
	return true
}
