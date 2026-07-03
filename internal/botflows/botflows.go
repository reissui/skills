// Package botflows is the clex Telegram conversation-policy layer. It sits on top
// of the thin transport (internal/telegram) and decides WHAT to say and WHEN,
// leaving the mechanics of moving bytes to the transport and the actual pipeline
// work to the daemon.
//
// Design (spec: Telegram bot → Interaction principles):
//
//   - Progress messages are one line, edited in place. No greetings, no filler.
//   - Every question ships with a proposed answer as the first button; the plan
//     gate batches questions into one numbered message (Confirm all).
//   - Silence is the default: only state changes worth acting on are pushed.
//   - It answers when asked: a direct question routes to the bot role and the
//     answer is relayed verbatim, never touching the running pipeline.
//   - Images queue, they don't interrupt; anything can be stopped.
//
// The exact outbound strings this package emits are a tested contract (golden
// files), not a vibe — the terseness is the product.
//
// # Boundaries
//
// botflows depends only on small interfaces it defines here (Transport, Daemon),
// so it needs neither a live bot nor the real daemon to test. The real
// *telegram.Transport satisfies Transport by structural match, so production
// wiring is a direct pass; the real daemon→botflows seam is additive and left for
// integration (#20). Nothing here dials a network, spawns a process, or logs a
// secret.
package botflows

import (
	"context"

	"github.com/reissui/clex/internal/telegram"
)

// Transport is the narrow slice of *telegram.Transport that botflows drives. The
// real transport satisfies this by structural match (same method signatures), so
// production wiring passes the concrete transport directly; tests substitute a
// fake that records every outbound string. Keeping the surface small keeps the
// fake small and the policy layer honest about what it touches.
type Transport interface {
	// SendLine posts a one-line message and returns its id for later editing.
	SendLine(ctx context.Context, text string) (msgID int, err error)
	// EditLine overwrites a previously sent line in place (progress primitive).
	EditLine(ctx context.Context, msgID int, text string) error
	// Ask presents a single confirm-or-alter question and blocks for the answer.
	Ask(ctx context.Context, q telegram.Question) (telegram.Answer, error)
	// AskBatch presents numbered items with per-item buttons plus [Confirm all]
	// and returns index-aligned answers (the plan gate's question block).
	AskBatch(ctx context.Context, prompt string, items []telegram.BatchItem) ([]telegram.Answer, error)
	// Handle registers a slash-command handler (name without the leading slash).
	Handle(name string, h telegram.CommandHandler)
	// OnImages registers the callback invoked when inbound images finish spooling.
	OnImages(fn func(files []string, replyToMsgID int))
}

// Daemon is the set of pipeline actions botflows triggers on the operator's
// behalf. Every button tap and command ultimately calls exactly one of these
// with the target issue and the operator's intent; botflows never mutates
// pipeline state itself. The real daemon implements this via a thin additive
// adapter (#20); tests fake it and assert the (issue, action) that each control
// reaches.
//
// Methods are intentionally coarse and side-effecting: botflows is policy, the
// daemon is mechanism.
type Daemon interface {
	// Idea files a free-text idea (optional repo override) as a clex:idea issue
	// and returns the created issue number so the intake reply can address it.
	Idea(ctx context.Context, repo, text string) (issue int, err error)
	// Research kicks off the research/planning stage for an idea issue with the
	// chosen model id ("" means the recommended top model).
	Research(ctx context.Context, issue int, modelID string) error
	// ProceedGate approves a plan/cost gate for an issue (the ✓ path).
	ProceedGate(ctx context.Context, issue int) error
	// SwapModel re-points an issue's next dispatch at a different model id.
	SwapModel(ctx context.Context, issue int, modelID string) error
	// Steer injects mid-flight guidance for an issue (resumed turn if running,
	// else a Steering note that is re-linted).
	Steer(ctx context.Context, issue int, text string) error
	// Stop cancels an issue's runner, reverting its label and preserving the
	// worktree for a later resume.
	Stop(ctx context.Context, issue int) error
	// Retry re-dispatches a failed issue as-is.
	Retry(ctx context.Context, issue int) error
	// Escalate re-dispatches a failed issue one model tier up.
	Escalate(ctx context.Context, issue int) error
	// Skip abandons a failed issue and moves on.
	Skip(ctx context.Context, issue int) error
	// Ask answers a direct question from the operator using the bot role with
	// pipeline state + LOG.md as context. The returned text is relayed verbatim.
	Ask(ctx context.Context, question string) (answer string, err error)
	// AttachImages queues spooled images as context for an issue's next stage.
	// It never blocks or disturbs a running scenario.
	AttachImages(ctx context.Context, issue int, files []string) error
}
