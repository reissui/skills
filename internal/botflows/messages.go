package botflows

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/reissui/clex/internal/registry"
)

// This file holds the pure message-composition functions: input in, exact
// outbound string out, no I/O. They are the tested contract — golden files pin
// every byte — so the terse UX (spec: Telegram bot) cannot silently drift. Every
// composer here except planGateText and the batched question block returns a
// single line (no embedded '\n').

// --- intake ---

// intakeReply is the single reply to a filed idea:
//
//	Research? [✓ <top model>] [pick model] [later]
//
// The buttons are rendered by the caller; the recommended (top) model is the
// first available option for the bot-intake role. topModel is the model id shown
// on the ✓ button.
func intakeReply(topModel string) string {
	return "Research? [✓ " + topModel + "] [pick model] [later]"
}

// pickerLine lists the selectable models for the "pick model" path, one tap each,
// as a single line. It is shown only when the operator declines the default.
func pickerLine(opts []registry.RunOption) string {
	if len(opts) == 0 {
		return "no models available"
	}
	ids := make([]string, 0, len(opts))
	for _, o := range opts {
		ids = append(ids, "["+o.Model.ID+"]")
	}
	return "pick: " + strings.Join(ids, " ")
}

// --- plan gate ---

// planView is botflows' own render-ready view of a plan result. The caller maps
// a pipeline.PlanResult (plus a cost estimate) onto this so the gate rendering is
// decoupled from the pipeline's internal shape.
type planView struct {
	EpicNumber  int
	IssueCount  int
	Parallelism int
	// LocalCount / MeteredCount summarize where the work lands, e.g.
	// "5 local + 1 codex" (spec's cost summary line).
	LocalCount   int
	MeteredCount int
	// MeteredLabel is the provider shown for metered work ("codex"); empty when
	// MeteredCount is 0.
	MeteredLabel string
	// EstUSD is the estimated metered spend for the epic; 0 hides the "$" clause.
	EstUSD float64
}

// planSummaryLine is the one-line cost/parallelism summary shown under the epic
// link, e.g. "6 issues · 4 parallel · est. 5 local + 1 codex · $6.20". The "$"
// clause is omitted when EstUSD is 0 (no metered spend to confirm).
func planSummaryLine(v planView) string {
	parts := []string{
		plural(v.IssueCount, "issue", "issues"),
		strconv.Itoa(v.Parallelism) + " parallel",
	}
	parts = append(parts, "est. "+workSplit(v))
	if v.EstUSD > 0 {
		parts = append(parts, usd(v.EstUSD))
	}
	return strings.Join(parts, " · ")
}

// workSplit renders the "N local + M codex" fragment. With no metered work it is
// just "N local"; with no local work just "M codex".
func workSplit(v planView) string {
	var seg []string
	if v.LocalCount > 0 {
		seg = append(seg, strconv.Itoa(v.LocalCount)+" local")
	}
	if v.MeteredCount > 0 {
		label := v.MeteredLabel
		if label == "" {
			label = "metered"
		}
		seg = append(seg, strconv.Itoa(v.MeteredCount)+" "+label)
	}
	if len(seg) == 0 {
		return "0"
	}
	return strings.Join(seg, " + ")
}

// planGateHeader is the epic link + summary shown ABOVE the batched questions.
// It is two lines (link, then summary) — the plan gate is the documented
// exception to the one-line rule.
func planGateHeader(v planView) string {
	return "epic " + issueRef(v.EpicNumber) + "\n" + planSummaryLine(v)
}

// planGateFooter is the final action row shown after the batched questions.
func planGateFooter() string {
	return "[✓ Build all] [adjust] [hold]"
}

// planGateText assembles the full gate message body for goldening: header,
// blank line, then the batched question block if any questions exist, then the
// footer. The actual buttons are rendered by AskBatch; this is the human-visible
// text the operator reads.
func planGateText(v planView, questions []batchQuestion) string {
	var b strings.Builder
	b.WriteString(planGateHeader(v))
	b.WriteString("\n\n")
	if len(questions) > 0 {
		b.WriteString(batchQuestionBlock(questions))
		b.WriteString("\n\n")
	}
	b.WriteString(planGateFooter())
	return b.String()
}

// batchQuestion is one row of the plan gate's batched questions: a short label
// and its proposed answer (the ✓ default).
type batchQuestion struct {
	Label    string
	Proposed string
}

// batchQuestionBlock renders the numbered per-item confirm-or-alter block plus
// the Confirm-all affordance, mirroring what the transport's AskBatch shows so
// the goldened text matches the live keyboard's caption. Format per item:
// "N. <label>: <proposed>".
func batchQuestionBlock(qs []batchQuestion) string {
	var b strings.Builder
	b.WriteString("questions:")
	for i, q := range qs {
		b.WriteByte('\n')
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(q.Label)
		b.WriteString(": ")
		b.WriteString(q.Proposed)
	}
	b.WriteString("\n[Confirm all]")
	return b.String()
}

// --- progress ---

// progressLine renders one edited-in-place progress line for an issue, e.g.
// "#42 building (codex-mini) — 3/5 checks passing". Empty optional fields are
// dropped so the line stays terse.
func progressLine(p ProgressEvent) string {
	var b strings.Builder
	b.WriteString(issueRef(p.Issue))
	b.WriteByte(' ')
	b.WriteString(string(p.Stage))
	if p.Model != "" {
		b.WriteString(" (")
		b.WriteString(p.Model)
		b.WriteByte(')')
	}
	if p.Detail != "" {
		b.WriteString(" — ")
		b.WriteString(p.Detail)
	}
	return b.String()
}

// failureLine renders a failed issue's line with the recovery action row
// appended, e.g. "#42 failed — build error [retry] [escalate model] [skip]".
func failureLine(p ProgressEvent) string {
	head := issueRef(p.Issue) + " failed"
	if p.Detail != "" {
		head += " — " + p.Detail
	}
	return head + " [retry] [escalate model] [skip]"
}

// prLine renders the terse PR-opened notification, e.g. "#42 PR opened → <url>".
func prLine(issue int, url string) string {
	return issueRef(issue) + " PR opened → " + url
}

// --- cost confirm ---

// costGate is the render input for a metered-spend confirmation.
type costGate struct {
	Issue  int
	Stage  string // "plan", "build", …
	Model  string // model id the spend runs on
	EstUSD float64
}

// costConfirmLine renders a cost gate, e.g.
// "#42 plan on fable-5 · est. $6.20 — [✓ proceed] [swap model] [hold]".
func costConfirmLine(g costGate) string {
	return fmt.Sprintf("%s %s on %s · est. %s — [✓ proceed] [swap model] [hold]",
		issueRef(g.Issue), g.Stage, g.Model, usd(g.EstUSD))
}

// --- acks / confirmations (all one line) ---

// imagesAck acknowledges queued images for a target, e.g. "2 images queued for
// #42". target is the human ref ("#42" or "the active idea").
func imagesAck(n int, target string) string {
	return plural(n, "image", "images") + " queued for " + target
}

// steerAck confirms a steer was forwarded, e.g. "steering #42".
func steerAck(issue int) string {
	return "steering " + issueRef(issue)
}

// stopAck confirms a stop was forwarded, e.g. "stopped #42".
func stopAck(issue int) string {
	return "stopped " + issueRef(issue)
}

// --- small shared helpers ---

// issueRef formats an issue number as "#N".
func issueRef(n int) string { return "#" + strconv.Itoa(n) }

// usd formats a dollar amount as "$X.XX".
func usd(v float64) string { return fmt.Sprintf("$%.2f", v) }

// plural renders "1 <one>" or "N <many>".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
