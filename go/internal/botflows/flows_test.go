package botflows

import (
	"context"
	"strings"
	"testing"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/registry"
	"github.com/reissui/clex/internal/telegram"
)

// testModels returns a deterministic set of intake/picker options, top first.
func testModels() []registry.RunOption {
	return []registry.RunOption{
		{Model: core.Model{ID: "fable-5", Provider: "codex"}, Tier: "top"},
		{Model: core.Model{ID: "codex-mini", Provider: "codex"}, Tier: "mid"},
		{Model: core.Model{ID: "qwen-local", Provider: "ollama"}, Tier: "local"},
	}
}

// --- intake ----------------------------------------------------------------

func TestIntake(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantRepo   string
		wantIdea   string
		goldenName string
	}{
		{
			name:       "plain idea",
			input:      "add a dark mode toggle to settings",
			wantRepo:   "",
			wantIdea:   "add a dark mode toggle to settings",
			goldenName: "intake_plain",
		},
		{
			name:       "repo prefix inline",
			input:      "repo: webapp add a dark mode toggle",
			wantRepo:   "webapp",
			wantIdea:   "add a dark mode toggle",
			goldenName: "intake_repo",
		},
		{
			name:       "repo prefix newline",
			input:      "repo: api\nrate-limit the login endpoint",
			wantRepo:   "api",
			wantIdea:   "rate-limit the login endpoint",
			goldenName: "intake_repo_newline",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, tg, dae := newTestFlows(testModels())
			if err := f.Intake(context.Background(), tc.input); err != nil {
				t.Fatalf("Intake: %v", err)
			}
			// Exactly one reply, the Research? line.
			got := tg.texts()
			if len(got) != 1 {
				t.Fatalf("want 1 reply, got %d: %q", len(got), got)
			}
			assertGolden(t, tc.goldenName, got[0])

			// The idea reached the daemon with the parsed repo + text.
			ideas := dae.callsFor("Idea")
			if len(ideas) != 1 {
				t.Fatalf("want 1 Idea call, got %d", len(ideas))
			}
			wantArg := tc.wantRepo + "|" + tc.wantIdea
			if ideas[0].Text != wantArg {
				t.Errorf("Idea arg = %q, want %q", ideas[0].Text, wantArg)
			}
			// Active idea is set to the filed issue for later image attach.
			if f.activeIdea != dae.nextIssue {
				t.Errorf("activeIdea = %d, want %d", f.activeIdea, dae.nextIssue)
			}
		})
	}
}

func TestIntakePicker(t *testing.T) {
	f, tg, _ := newTestFlows(testModels())
	if err := f.ShowPicker(context.Background()); err != nil {
		t.Fatalf("ShowPicker: %v", err)
	}
	got := tg.texts()
	if len(got) != 1 {
		t.Fatalf("want 1 line, got %d", len(got))
	}
	assertGolden(t, "intake_picker", got[0])
}

// --- plan gate -------------------------------------------------------------

func TestPlanGate(t *testing.T) {
	f, tg, _ := newTestFlows(testModels())
	v := planView{
		EpicNumber:   77,
		IssueCount:   6,
		Parallelism:  4,
		LocalCount:   5,
		MeteredCount: 1,
		MeteredLabel: "codex",
		EstUSD:       6.20,
	}
	questions := []batchQuestion{
		{Label: "auth strategy", Proposed: "magic link"},
		{Label: "db", Proposed: "postgres"},
	}
	answers, err := f.PlanGate(context.Background(), v, questions)
	if err != nil {
		t.Fatalf("PlanGate: %v", err)
	}
	// Golden the full human-visible gate text (header + batched block + footer).
	// Compose from the same inputs so the golden pins every byte the operator sees.
	full := planGateText(v, questions)
	assertGolden(t, "plan_gate", full)

	// The header and footer went out as messages; the batch prompt went to AskBatch.
	texts := tg.texts()
	if len(texts) != 2 {
		t.Fatalf("want header+footer = 2 SendLine, got %d: %q", len(texts), texts)
	}
	if !strings.HasPrefix(texts[0], "epic #77") {
		t.Errorf("header = %q, want epic link first", texts[0])
	}
	if texts[1] != "[✓ Build all] [adjust] [hold]" {
		t.Errorf("footer = %q", texts[1])
	}
	if len(tg.askedBatch) != 1 {
		t.Fatalf("want 1 AskBatch, got %d", len(tg.askedBatch))
	}
	// AskBatch items carry each label + proposal (feeds the numbered keyboard).
	items := tg.askedBatch[0].Items
	if len(items) != 2 || items[0].Label != "auth strategy" || items[0].Proposal != "magic link" {
		t.Errorf("batch items = %+v", items)
	}
	// Default fake answers Confirm-all: every proposal accepted.
	if len(answers) != 2 || answers[0].Text != "magic link" || answers[1].Text != "postgres" {
		t.Errorf("answers = %+v, want Confirm-all proposals", answers)
	}
}

func TestPlanGateConfirmAll(t *testing.T) {
	// Confirm all → every question resolves to its proposal (the single-tap path).
	f, tg, _ := newTestFlows(testModels())
	v := planView{EpicNumber: 5, IssueCount: 2, Parallelism: 2, LocalCount: 2}
	qs := []batchQuestion{
		{Label: "framework", Proposed: "gin"},
		{Label: "cache", Proposed: "redis"},
	}
	// Explicit Confirm-all script: all proposals.
	tg.batchAnswers = []telegram.Answer{{Text: "gin"}, {Text: "redis"}}
	answers, err := f.PlanGate(context.Background(), v, qs)
	if err != nil {
		t.Fatalf("PlanGate: %v", err)
	}
	for i, want := range []string{"gin", "redis"} {
		if answers[i].Text != want {
			t.Errorf("answers[%d] = %q, want %q", i, answers[i].Text, want)
		}
	}
	assertGolden(t, "plan_gate_confirm_all", planGateText(v, qs))
}

func TestPlanGateNoQuestions(t *testing.T) {
	// A gate with no open questions still renders header + footer (no AskBatch).
	f, tg, _ := newTestFlows(testModels())
	v := planView{EpicNumber: 9, IssueCount: 3, Parallelism: 3, LocalCount: 3}
	answers, err := f.PlanGate(context.Background(), v, nil)
	if err != nil {
		t.Fatalf("PlanGate: %v", err)
	}
	if len(answers) != 0 {
		t.Errorf("want no answers, got %+v", answers)
	}
	if len(tg.askedBatch) != 0 {
		t.Errorf("want no AskBatch when no questions, got %d", len(tg.askedBatch))
	}
	if len(tg.texts()) != 2 {
		t.Fatalf("want header+footer, got %q", tg.texts())
	}
}

// --- progress + failure ----------------------------------------------------

func TestProgressEditSequence(t *testing.T) {
	f, tg, _ := newTestFlows(testModels())
	ctx := context.Background()
	// First line, then two in-place edits, then a failure edit with actions.
	msgID, err := f.Progress(ctx, ProgressEvent{Issue: 42, Stage: StageBuilding, Model: "codex-mini", Detail: "1/5 checks passing"})
	if err != nil {
		t.Fatalf("Progress: %v", err)
	}
	steps := []ProgressEvent{
		{Issue: 42, Stage: StageBuilding, Model: "codex-mini", Detail: "3/5 checks passing"},
		{Issue: 42, Stage: StageBuilding, Model: "codex-mini", Detail: "5/5 checks passing"},
		{Issue: 42, Stage: StageFailed, Detail: "build error", Failed: true},
	}
	for _, s := range steps {
		if err := f.Update(ctx, msgID, s); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	// One SendLine + three EditLine, all against the same msgID.
	if len(tg.sent) != 1 {
		t.Fatalf("want 1 SendLine, got %d", len(tg.sent))
	}
	for _, e := range tg.edits {
		if e.MsgID != msgID {
			t.Errorf("edit msgID = %d, want %d (edit-in-place)", e.MsgID, msgID)
		}
	}
	seq := append([]string{tg.sent[0].Text}, tg.editTexts()...)
	assertGolden(t, "progress_sequence", joinLines(seq))
	// The final line must carry the recovery actions.
	last := seq[len(seq)-1]
	if !strings.Contains(last, "[retry] [escalate model] [skip]") {
		t.Errorf("failure line missing actions: %q", last)
	}
}

func TestProgressPROpened(t *testing.T) {
	f, tg, _ := newTestFlows(testModels())
	if err := f.PROpened(context.Background(), 42, "https://github.com/x/y/pull/9"); err != nil {
		t.Fatalf("PROpened: %v", err)
	}
	got := tg.texts()
	if len(got) != 1 {
		t.Fatalf("want 1 line, got %d", len(got))
	}
	assertGolden(t, "pr_opened", got[0])
}

// TestFailureActionsRouting asserts each recovery button reaches the daemon with
// the right issue + action.
func TestFailureActionsRouting(t *testing.T) {
	tests := []struct {
		name   string
		call   func(f *Flows, ctx context.Context) error
		action string
		issue  int
	}{
		{"retry", func(f *Flows, ctx context.Context) error { return f.Retry(ctx, 42) }, "Retry", 42},
		{"escalate", func(f *Flows, ctx context.Context) error { return f.Escalate(ctx, 42) }, "Escalate", 42},
		{"skip", func(f *Flows, ctx context.Context) error { return f.Skip(ctx, 42) }, "Skip", 42},
		{"proceed", func(f *Flows, ctx context.Context) error { return f.ProceedGate(ctx, 42) }, "ProceedGate", 42},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, _, dae := newTestFlows(testModels())
			if err := tc.call(f, context.Background()); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			c, ok := dae.lastCall()
			if !ok || c.Action != tc.action || c.Issue != tc.issue {
				t.Errorf("got %+v, want action=%s issue=%d", c, tc.action, tc.issue)
			}
		})
	}
}

func TestSwapModelRouting(t *testing.T) {
	f, _, dae := newTestFlows(testModels())
	if err := f.SwapModel(context.Background(), 42, "fable-5"); err != nil {
		t.Fatalf("SwapModel: %v", err)
	}
	c, _ := dae.lastCall()
	if c.Action != "SwapModel" || c.Issue != 42 || c.Text != "fable-5" {
		t.Errorf("got %+v, want SwapModel #42 fable-5", c)
	}
}

// --- cost confirm ----------------------------------------------------------

func TestCostConfirm(t *testing.T) {
	f, tg, _ := newTestFlows(testModels())
	g := costGate{Issue: 42, Stage: "plan", Model: "fable-5", EstUSD: 6.20}
	if err := f.CostConfirm(context.Background(), g); err != nil {
		t.Fatalf("CostConfirm: %v", err)
	}
	got := tg.texts()
	if len(got) != 1 {
		t.Fatalf("want 1 line, got %d", len(got))
	}
	assertGolden(t, "cost_confirm", got[0])
}

// --- Q&A -------------------------------------------------------------------

func TestQAVerbatim(t *testing.T) {
	f, tg, dae := newTestFlows(testModels())
	dae.answer = "#42 escalated because the local model failed lint twice."
	routed, err := f.MaybeAnswer(context.Background(), "why did #42 escalate?")
	if err != nil {
		t.Fatalf("MaybeAnswer: %v", err)
	}
	if !routed {
		t.Fatal("question should route to Q&A")
	}
	// Answer relayed verbatim — no framing added.
	got := tg.texts()
	if len(got) != 1 || got[0] != dae.answer {
		t.Errorf("relay = %q, want verbatim %q", got, dae.answer)
	}
	// The daemon saw the trimmed question.
	asks := dae.callsFor("Ask")
	if len(asks) != 1 || asks[0].Text != "why did #42 escalate?" {
		t.Errorf("Ask calls = %+v", asks)
	}
}

// TestQARoutingDiscrimination asserts Q&A fires ONLY for question-like input and
// statements (ideas) fall through (routed=false) so intake can handle them.
func TestQARoutingDiscrimination(t *testing.T) {
	tests := []struct {
		text     string
		wantRoot bool
	}{
		{"why did #42 escalate?", true},
		{"what's left on the epic?", true},
		{"how many issues remain", true},
		{"is the build green?", true},
		{"add a dark mode toggle", false},
		{"repo: web add login", false},
		{"ship it", false},
		{"the tests are flaky", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(strings.ReplaceAll(tc.text, " ", "_"), func(t *testing.T) {
			f, _, dae := newTestFlows(testModels())
			dae.answer = "ok"
			routed, err := f.MaybeAnswer(context.Background(), tc.text)
			if err != nil {
				t.Fatalf("MaybeAnswer: %v", err)
			}
			if routed != tc.wantRoot {
				t.Errorf("routed = %v, want %v for %q", routed, tc.wantRoot, tc.text)
			}
			asks := dae.callsFor("Ask")
			if tc.wantRoot && len(asks) != 1 {
				t.Errorf("expected 1 Ask for question, got %d", len(asks))
			}
			if !tc.wantRoot && len(asks) != 0 {
				t.Errorf("non-question should not route, got %d Ask calls", len(asks))
			}
		})
	}
}

// --- images ----------------------------------------------------------------

func TestImagesAttachToActiveIdea(t *testing.T) {
	f, tg, dae := newTestFlows(testModels())
	// File an idea so there is an active idea, then drop images with no reply.
	if err := f.Intake(context.Background(), "build a login page"); err != nil {
		t.Fatalf("Intake: %v", err)
	}
	// Drive the transport's image callback exactly as the live transport would.
	tg.onImages([]string{"/spool/a.png", "/spool/b.png"}, 0)

	// Images attach to the active idea (#42) and ack once.
	att := dae.callsFor("AttachImages")
	if len(att) != 1 || att[0].Issue != dae.nextIssue || len(att[0].Files) != 2 {
		t.Fatalf("AttachImages = %+v, want active idea #%d with 2 files", att, dae.nextIssue)
	}
	// Last outbound line is the images ack (intake reply was first).
	texts := tg.texts()
	ack := texts[len(texts)-1]
	assertGolden(t, "images_ack_idea", ack)
}

func TestImagesAttachToRepliedIssue(t *testing.T) {
	f, tg, dae := newTestFlows(testModels())
	// Give the active idea a different number so the test proves reply wins.
	dae.nextIssue = 100
	if err := f.Intake(context.Background(), "some idea"); err != nil {
		t.Fatalf("Intake: %v", err)
	}
	// A progress line for #42 was sent with a known msgID; register the mapping.
	msgID, err := f.Progress(context.Background(), ProgressEvent{Issue: 42, Stage: StageBuilding})
	if err != nil {
		t.Fatalf("Progress: %v", err)
	}
	f.SetReplyIssue(msgID, 42)

	// Images replying to that progress line attach to #42, NOT the active idea 100.
	tg.onImages([]string{"/spool/c.png"}, msgID)

	att := dae.callsFor("AttachImages")
	if len(att) != 1 || att[0].Issue != 42 {
		t.Fatalf("AttachImages = %+v, want replied issue #42", att)
	}
	texts := tg.texts()
	ack := texts[len(texts)-1]
	assertGolden(t, "images_ack_issue", ack)
}

// TestImagesNeverBlock asserts image handling does not disturb a running scenario:
// even when the daemon errors, the ack is still sent and no action panics/blocks.
func TestImagesNeverBlock(t *testing.T) {
	f, tg, dae := newTestFlows(testModels())
	// Establish an active idea with the daemon healthy.
	if err := f.Intake(context.Background(), "idea"); err != nil {
		t.Fatalf("Intake: %v", err)
	}
	// Now make AttachImages fail; image handling must still ack and not block.
	dae.err = context.Canceled
	tg.onImages([]string{"/spool/x.png"}, 0)
	texts := tg.texts()
	if !strings.Contains(texts[len(texts)-1], "queued for") {
		t.Errorf("expected ack even on attach error, got %q", texts[len(texts)-1])
	}
}

// --- steer / stop ----------------------------------------------------------

func TestSteerCommand(t *testing.T) {
	_, tg, dae := newTestFlows(testModels())
	h := tg.handlers["steer"]
	if h == nil {
		t.Fatal("steer handler not registered")
	}
	h(context.Background(), "42 use the v2 auth flow")

	steers := dae.callsFor("Steer")
	if len(steers) != 1 || steers[0].Issue != 42 || steers[0].Text != "use the v2 auth flow" {
		t.Fatalf("Steer = %+v", steers)
	}
	got := tg.texts()
	if len(got) != 1 {
		t.Fatalf("want 1 confirmation, got %d", len(got))
	}
	assertGolden(t, "steer_ack", got[0])
}

func TestSteerCommandUsage(t *testing.T) {
	_, tg, dae := newTestFlows(testModels())
	tg.handlers["steer"](context.Background(), "notanumber")
	if len(dae.callsFor("Steer")) != 0 {
		t.Error("malformed steer should not reach daemon")
	}
	got := tg.texts()
	if len(got) != 1 || !strings.HasPrefix(got[0], "usage:") {
		t.Errorf("want usage line, got %q", got)
	}
}

func TestStopCommand(t *testing.T) {
	_, tg, dae := newTestFlows(testModels())
	tg.handlers["stop"](context.Background(), "#42")

	stops := dae.callsFor("Stop")
	if len(stops) != 1 || stops[0].Issue != 42 {
		t.Fatalf("Stop = %+v", stops)
	}
	got := tg.texts()
	if len(got) != 1 {
		t.Fatalf("want 1 confirmation, got %d", len(got))
	}
	assertGolden(t, "stop_ack", got[0])
}

// --- one-line invariant ----------------------------------------------------

// TestOneLineInvariant asserts every outbound message is a single line EXCEPT the
// plan gate header (epic link + summary) and the batched questions block.
func TestOneLineInvariant(t *testing.T) {
	// Collect one message from each single-line surface and assert no newline.
	lines := []string{
		intakeReply("fable-5"),
		pickerLine(testModels()),
		planGateFooter(),
		progressLine(ProgressEvent{Issue: 42, Stage: StageBuilding, Model: "codex-mini", Detail: "3/5 checks passing"}),
		failureLine(ProgressEvent{Issue: 42, Detail: "boom"}),
		prLine(42, "http://x"),
		costConfirmLine(costGate{Issue: 42, Stage: "plan", Model: "fable-5", EstUSD: 6.2}),
		imagesAck(2, "#42"),
		steerAck(42),
		stopAck(42),
	}
	for _, l := range lines {
		if strings.Contains(l, "\n") {
			t.Errorf("outbound line must be single-line, got multi-line: %q", l)
		}
	}
	// The two documented exceptions ARE allowed to be multi-line.
	v := planView{EpicNumber: 1, IssueCount: 2, Parallelism: 2, LocalCount: 2}
	if !strings.Contains(planGateHeader(v), "\n") {
		t.Error("plan gate header is expected to be multi-line (epic link + summary)")
	}
	block := batchQuestionBlock([]batchQuestion{{Label: "a", Proposed: "b"}})
	if !strings.Contains(block, "\n") {
		t.Error("batched question block is expected to be multi-line")
	}
}

// --- summary-line rendering variants ---------------------------------------

func TestPlanSummaryLine(t *testing.T) {
	tests := []struct {
		name string
		v    planView
	}{
		{"local only", planView{IssueCount: 5, Parallelism: 4, LocalCount: 5}},
		{"mixed with cost", planView{IssueCount: 6, Parallelism: 4, LocalCount: 5, MeteredCount: 1, MeteredLabel: "codex", EstUSD: 6.2}},
		{"single issue", planView{IssueCount: 1, Parallelism: 1, LocalCount: 1}},
		{"metered only", planView{IssueCount: 2, Parallelism: 2, MeteredCount: 2, MeteredLabel: "codex", EstUSD: 12.5}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertGolden(t, "summary_"+strings.ReplaceAll(tc.name, " ", "_"), planSummaryLine(tc.v))
		})
	}
}
