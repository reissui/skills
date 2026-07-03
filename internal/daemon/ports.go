package daemon

import (
	"context"
	"time"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/pipeline"
	"github.com/reissui/clex/internal/telegram"
)

// GitHubPort is the subset of GitHub operations the daemon itself needs, over
// and above what it passes to the pipeline. The daemon uses it to poll for
// changes, enumerate issues for scheduler state and crash recovery, and to
// revert orphaned labels with a comment. It is an interface so integration
// tests can drive the whole loop against an in-memory fake with zero network
// (spec: Testing strategy — no live GitHub).
//
// The production implementation (ghAdapter) wraps *gh.Client. ListIssues is
// backed by the raw go-github client because *gh.Client exposes no list method;
// keeping it behind this port means the daemon never imports go-github.
type GitHubPort interface {
	// Poll starts the change poller and returns its event channel. It mirrors
	// gh.Client.Poll so the concrete client satisfies it directly.
	Poll(ctx context.Context, repos []gh.Repo, every time.Duration, opts gh.PollOptions) <-chan gh.Change
	// ListIssues returns every open issue carrying at least one clex:* label,
	// with pipeline state derived from labels. It is the source-of-truth read
	// used to (re)build scheduler state and to reconstruct in-flight work after
	// a crash (spec: Source of truth: GitHub).
	ListIssues(ctx context.Context, repo gh.Repo) ([]*gh.Issue, error)
	// GetIssue reads one issue.
	GetIssue(ctx context.Context, repo gh.Repo, number int) (*gh.Issue, error)
	// SetState moves an issue to a new pipeline state (label swap).
	SetState(ctx context.Context, repo gh.Repo, number int, to core.State) error
	// Comment posts a comment.
	Comment(ctx context.Context, repo gh.Repo, number int, body string) error
	// UpdateIssue edits an issue's title/body (used by idle/epic steer).
	UpdateIssue(ctx context.Context, repo gh.Repo, number int, title, body *string) (*gh.Issue, error)
}

// TelegramPort is the subset of the Telegram transport the daemon drives: it
// sends status lines, asks the owner to confirm a cost gate, and registers
// slash-command handlers. Interface so tests assert what the daemon would send
// without a live bot (spec: Telegram — handler tests, no live Telegram in CI).
type TelegramPort interface {
	// SendLine posts a one-line message and returns its message id.
	SendLine(ctx context.Context, text string) (int, error)
	// Ask poses a confirm-or-alter question and blocks for the answer. Used to
	// route a cost-gate GateConfirm to the owner before dispatch.
	Ask(ctx context.Context, q telegram.Question) (telegram.Answer, error)
	// Handle registers a slash-command handler (/status, /pause, ...).
	Handle(name string, h telegram.CommandHandler)
}

// Stages is the subset of *pipeline.Pipeline the daemon invokes. Declaring it as
// an interface lets integration tests substitute a lightweight stage double for
// scenarios that exercise loop mechanics (stop, steer, escalation bookkeeping)
// without running a full real pipeline, while the scenario test uses the real
// *pipeline.Pipeline. *pipeline.Pipeline satisfies this by construction.
type Stages interface {
	Plan(ctx context.Context, ideaIssue *gh.Issue, in pipeline.PlanInputs, existingEpicNumber int) (pipeline.PlanResult, error)
	Build(ctx context.Context, epicNum int, issue *gh.Issue, k pipeline.KnowledgeExcerpts, existingPRNumber int) (pipeline.BuildResult, error)
	Review(ctx context.Context, epicNum int, issue *gh.Issue, prNumber int, authorModel core.Model, diff string, verificationGreen bool) (pipeline.ReviewResult, error)
	Assemble(ctx context.Context, epicNum int, allLanded bool, in pipeline.AssembleInput, epicVerify string, existingPRNumber int) (pipeline.AssembleResult, error)
	EscalateModel(current core.Model) (core.Model, bool)
}

// compile-time assurance that the real pipeline satisfies Stages.
var _ Stages = (*pipeline.Pipeline)(nil)
