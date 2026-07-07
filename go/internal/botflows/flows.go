package botflows

import (
	"context"
	"strconv"
	"strings"

	"github.com/reissui/clex/internal/registry"
	"github.com/reissui/clex/internal/telegram"
)

// Stage names a pipeline stage as shown on a progress line. It mirrors the
// human-facing verbs the spec uses ("building", "planning", …) rather than
// core.State so progress text reads naturally.
type Stage string

const (
	StagePlanning  Stage = "planning"
	StageBuilding  Stage = "building"
	StageReviewing Stage = "reviewing"
	StageFailed    Stage = "failed"
	StageDone      Stage = "done"
)

// ProgressEvent is botflows' own progress DTO, mapped from a core.Event / daemon
// snapshot by the integration seam (#20). It carries just what a one-line update
// needs. Defining our own type (rather than consuming core.Event directly) keeps
// the render contract stable and independently fakeable.
type ProgressEvent struct {
	// Issue is the issue the line is about.
	Issue int
	// Stage is the current stage verb.
	Stage Stage
	// Model is the model id running the stage ("" hides the parenthetical).
	Model string
	// Detail is the terse tail, e.g. "3/5 checks passing" or a failure reason.
	Detail string
	// Failed marks a terminal failure so the recovery action row is appended.
	Failed bool
}

// Flows is the conversation-policy driver. It owns the transport and daemon
// interfaces and threads operator intent to daemon actions while keeping every
// outbound message on-contract. It is constructed once and its Register wires the
// slash commands and image handler onto the transport.
type Flows struct {
	tg  Transport
	dae Daemon

	// intakeModels feeds the intake picker: the available models for the
	// bot-intake role, top option first. Injected so tests pin the picker without
	// a live registry.
	intakeModels []registry.RunOption

	// activeIdea is the issue newly filed idea/context attaches to when a message
	// does not reply to a specific issue (spec: "attach to the active idea").
	// Updated by intake; read by the image handler.
	activeIdea int

	// replyIssues maps a progress line's transport message id to the issue it is
	// about, so an image replying to that line attaches to the right issue rather
	// than the active idea. Populated by SetReplyIssue; read by imageTarget.
	replyIssues map[int]int
}

// New constructs a Flows over a transport and daemon. intakeModels is the
// bot-intake role's available options (top first) used for the intake picker;
// pass registry.Available(core.RoleBot)'s options.
func New(tg Transport, dae Daemon, intakeModels []registry.RunOption) *Flows {
	return &Flows{tg: tg, dae: dae, intakeModels: intakeModels}
}

// Register wires the flows onto the transport: slash commands and the image
// handler. Call once before the transport's Run loop starts.
func (f *Flows) Register() {
	f.tg.Handle("steer", f.onSteer)
	f.tg.Handle("stop", f.onStop)
	f.tg.OnImages(f.onImages)
}

// --- intake ---

// Intake handles a free-text idea message (optionally "repo: <name>\n<idea>").
// It files the idea via the daemon and sends the single Research? reply. The
// filed issue becomes the active idea so subsequently-sent images attach to it.
func (f *Flows) Intake(ctx context.Context, text string) error {
	repo, idea := parseRepoPrefix(text)
	issue, err := f.dae.Idea(ctx, repo, idea)
	if err != nil {
		return err
	}
	f.activeIdea = issue
	_, err = f.tg.SendLine(ctx, intakeReply(f.topModelID()))
	return err
}

// topModelID returns the id shown on the intake ✓ button (first available
// bot-intake option), or "default" if none is configured.
func (f *Flows) topModelID() string {
	if len(f.intakeModels) > 0 {
		return f.intakeModels[0].Model.ID
	}
	return "default"
}

// ShowPicker sends the model picker line (the "pick model" path). Kept separate
// so intake stays a single reply by default (spec: default path is one tap).
func (f *Flows) ShowPicker(ctx context.Context) error {
	_, err := f.tg.SendLine(ctx, pickerLine(f.intakeModels))
	return err
}

// parseRepoPrefix splits an optional leading "repo: <name>" line from the idea
// body. The prefix may be on its own first line or inline "repo:<name> <idea>".
// Returns ("", text) when no prefix is present.
func parseRepoPrefix(text string) (repo, idea string) {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "repo:") {
		return "", trimmed
	}
	rest := strings.TrimSpace(trimmed[len("repo:"):])
	// repo name is the first whitespace-delimited token; the remainder is the idea.
	if i := strings.IndexFunc(rest, func(r rune) bool { return r == ' ' || r == '\n' || r == '\t' }); i >= 0 {
		return strings.TrimSpace(rest[:i]), strings.TrimSpace(rest[i+1:])
	}
	return rest, ""
}

// --- plan gate ---

// PlanGate presents the batched plan gate: epic link + summary, the numbered
// per-item confirm-or-alter questions (with Confirm all), then the Build-all
// action. It returns the operator's per-question answers (index-aligned with
// questions) so the caller can apply alterations; a nil/empty questions slice
// still renders the header+footer via SendLine.
//
// On the ✓ Build-all path the caller proceeds the gate; PlanGate itself only
// gathers the answers and does not decide — deciding is the daemon's job once
// the gate action arrives.
func (f *Flows) PlanGate(ctx context.Context, v planView, questions []batchQuestion) ([]telegram.Answer, error) {
	// Header + summary as a (multi-line) message.
	if _, err := f.tg.SendLine(ctx, planGateHeader(v)); err != nil {
		return nil, err
	}
	var answers []telegram.Answer
	if len(questions) > 0 {
		items := make([]telegram.BatchItem, len(questions))
		for i, q := range questions {
			items[i] = telegram.BatchItem{Label: q.Label, Proposal: q.Proposed}
		}
		var err error
		answers, err = f.tg.AskBatch(ctx, "questions:", items)
		if err != nil {
			return nil, err
		}
	}
	if _, err := f.tg.SendLine(ctx, planGateFooter()); err != nil {
		return answers, err
	}
	return answers, nil
}

// --- progress ---

// Progress sends a fresh progress line and returns its message id so the caller
// can edit it in place on the next update. Use Update to overwrite an existing
// line. A failed event renders the recovery action row instead of a plain line.
func (f *Flows) Progress(ctx context.Context, p ProgressEvent) (msgID int, err error) {
	return f.tg.SendLine(ctx, renderProgress(p))
}

// Update overwrites a previously sent progress line (msgID) in place with the
// new state — the edit-in-place primitive (spec: progress edited in place, not
// stacked).
func (f *Flows) Update(ctx context.Context, msgID int, p ProgressEvent) error {
	return f.tg.EditLine(ctx, msgID, renderProgress(p))
}

// renderProgress picks the plain or failure rendering for an event.
func renderProgress(p ProgressEvent) string {
	if p.Failed || p.Stage == StageFailed {
		return failureLine(p)
	}
	return progressLine(p)
}

// PROpened sends the terse PR-opened notification (a state change worth pushing).
func (f *Flows) PROpened(ctx context.Context, issue int, url string) error {
	_, err := f.tg.SendLine(ctx, prLine(issue, url))
	return err
}

// --- failure recovery actions (button callbacks) ---
//
// These are the seams the transport's inline buttons invoke; each forwards to
// exactly one daemon action for the given issue. Tests assert the (issue, action)
// pair reaches the fake daemon.

// Retry forwards a [retry] tap.
func (f *Flows) Retry(ctx context.Context, issue int) error { return f.dae.Retry(ctx, issue) }

// Escalate forwards an [escalate model] tap.
func (f *Flows) Escalate(ctx context.Context, issue int) error { return f.dae.Escalate(ctx, issue) }

// Skip forwards a [skip] tap.
func (f *Flows) Skip(ctx context.Context, issue int) error { return f.dae.Skip(ctx, issue) }

// ProceedGate forwards a gate ✓ (Build all / proceed) tap.
func (f *Flows) ProceedGate(ctx context.Context, issue int) error {
	return f.dae.ProceedGate(ctx, issue)
}

// SwapModel forwards a [swap model] tap with the newly chosen model id.
func (f *Flows) SwapModel(ctx context.Context, issue int, modelID string) error {
	return f.dae.SwapModel(ctx, issue, modelID)
}

// --- cost confirm ---

// CostConfirm renders a metered-spend gate as a one-line confirm. The buttons
// (✓ proceed / swap model / hold) route to ProceedGate / SwapModel; this call
// only sends the line.
func (f *Flows) CostConfirm(ctx context.Context, g costGate) error {
	_, err := f.tg.SendLine(ctx, costConfirmLine(g))
	return err
}

// --- Q&A ---

// MaybeAnswer routes a non-command, non-intake message that reads as a question
// to the bot role and relays the answer verbatim. It reports whether the message
// was treated as a question (routed). Non-questions are left for the caller to
// handle as intake, so Q&A never swallows an idea.
//
// The answer is sent as-is (concise, verbatim) — botflows adds no framing (spec:
// "the answer must be sent as-is").
func (f *Flows) MaybeAnswer(ctx context.Context, text string) (routed bool, err error) {
	if !isQuestion(text) {
		return false, nil
	}
	ans, err := f.dae.Ask(ctx, strings.TrimSpace(text))
	if err != nil {
		return true, err
	}
	if _, err := f.tg.SendLine(ctx, ans); err != nil {
		return true, err
	}
	return true, nil
}

// isQuestion reports whether free text reads as a direct question the bot should
// answer rather than an idea to file. Heuristic (spec: "reads as a question"): a
// trailing '?' or a leading interrogative word. Intentionally conservative so
// statements (ideas) fall through to intake.
func isQuestion(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if strings.HasSuffix(t, "?") {
		return true
	}
	first := strings.ToLower(firstWord(t))
	switch first {
	case "why", "what", "what's", "whats", "how", "when", "where", "who", "which",
		"is", "are", "can", "does", "did", "should", "will", "would":
		return true
	}
	return false
}

// firstWord returns the first whitespace-delimited token of s.
func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexFunc(s, func(r rune) bool { return r == ' ' || r == '\n' || r == '\t' }); i >= 0 {
		return s[:i]
	}
	return s
}

// --- images ---

// onImages is the transport image callback: spooled images attach to the
// replied-to issue, else the active idea, and are acked with one line. It never
// blocks or touches a running scenario (spec: "images queue, they don't
// interrupt") — the daemon queues them as next-stage context.
func (f *Flows) onImages(files []string, replyToMsgID int) {
	if len(files) == 0 {
		return
	}
	issue, target := f.imageTarget(replyToMsgID)
	ctx := context.Background()
	if issue > 0 {
		// Best-effort queue; failures are not surfaced as they must not disturb
		// anything running.
		_ = f.dae.AttachImages(ctx, issue, files)
	}
	_, _ = f.tg.SendLine(ctx, imagesAck(len(files), target))
}

// imageTarget resolves what images attach to: the issue a message replied to (if
// the caller mapped the reply to an issue via SetReplyIssue), else the active
// idea. Returns the issue number (0 if none) and the human target label.
func (f *Flows) imageTarget(replyToMsgID int) (issue int, target string) {
	if replyToMsgID > 0 {
		if iss, ok := f.replyIssues[replyToMsgID]; ok {
			return iss, issueRef(iss)
		}
	}
	if f.activeIdea > 0 {
		return f.activeIdea, "the active idea"
	}
	return 0, "the active idea"
}

// SetReplyIssue records that a given transport message id belongs to an issue, so
// images replying to that message attach to it rather than the active idea. The
// caller (progress sender) registers each progress line's msgID→issue here.
func (f *Flows) SetReplyIssue(msgID, issue int) {
	if f.replyIssues == nil {
		f.replyIssues = make(map[int]int)
	}
	f.replyIssues[msgID] = issue
}

// --- steer / stop commands ---

// onSteer handles "/steer <issue> <text>": it forwards the guidance to the
// daemon and confirms in one line. Malformed input is nacked tersely.
func (f *Flows) onSteer(ctx context.Context, args string) {
	issue, rest, ok := splitIssueArg(args)
	if !ok || strings.TrimSpace(rest) == "" {
		_, _ = f.tg.SendLine(ctx, "usage: /steer <issue> <text>")
		return
	}
	if err := f.dae.Steer(ctx, issue, strings.TrimSpace(rest)); err != nil {
		return
	}
	_, _ = f.tg.SendLine(ctx, steerAck(issue))
}

// onStop handles "/stop <issue>": cancels the runner and confirms in one line.
func (f *Flows) onStop(ctx context.Context, args string) {
	issue, _, ok := splitIssueArg(args)
	if !ok {
		_, _ = f.tg.SendLine(ctx, "usage: /stop <issue>")
		return
	}
	if err := f.dae.Stop(ctx, issue); err != nil {
		return
	}
	_, _ = f.tg.SendLine(ctx, stopAck(issue))
}

// splitIssueArg parses a leading issue number (optionally "#42") from a command
// argument string, returning the number, the remainder, and whether a number was
// found.
func splitIssueArg(args string) (issue int, rest string, ok bool) {
	s := strings.TrimSpace(args)
	tok := firstWord(s)
	rest = strings.TrimSpace(strings.TrimPrefix(s, tok))
	n, err := strconv.Atoi(strings.TrimPrefix(tok, "#"))
	if err != nil {
		return 0, s, false
	}
	return n, rest, true
}
