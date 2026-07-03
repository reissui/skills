package botflows

import (
	"context"
	"sync"

	"github.com/reissui/clex/internal/registry"
	"github.com/reissui/clex/internal/telegram"
)

// This file provides the in-memory doubles the golden tests run against. It does
// NOT import the telegram package's own _test.go fakes (those are package-private
// and unreachable); it implements the narrow botflows Transport/Daemon interfaces
// directly and records every outbound string and every daemon action so tests can
// assert both the exact text and the (issue, action) routing. Zero live services.

// fakeTransport records every outbound message and lets a test script the answer
// to the next Ask / AskBatch. It satisfies botflows.Transport.
type fakeTransport struct {
	mu sync.Mutex

	nextMsgID int
	sent      []sentLine // SendLine calls, in order
	edits     []editLine // EditLine calls, in order

	// asked / askedBatch record the prompts presented so tests can assert the
	// exact question text the operator saw.
	asked      []telegram.Question
	askedBatch []batchAsk

	// answer / batchAnswers are the scripted responses returned by the next Ask /
	// AskBatch call. A test sets these before invoking the flow.
	answer       telegram.Answer
	answerErr    error
	batchAnswers []telegram.Answer
	batchErr     error

	// handlers / onImages capture what Register wired, so tests can invoke a
	// command or an image drop exactly as the live transport would.
	handlers map[string]telegram.CommandHandler
	onImages func(files []string, replyToMsgID int)
}

type sentLine struct {
	MsgID int
	Text  string
}

type editLine struct {
	MsgID int
	Text  string
}

type batchAsk struct {
	Prompt string
	Items  []telegram.BatchItem
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{
		nextMsgID: 100,
		handlers:  make(map[string]telegram.CommandHandler),
	}
}

func (f *fakeTransport) SendLine(_ context.Context, text string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextMsgID++
	id := f.nextMsgID
	f.sent = append(f.sent, sentLine{MsgID: id, Text: text})
	return id, nil
}

func (f *fakeTransport) EditLine(_ context.Context, msgID int, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, editLine{MsgID: msgID, Text: text})
	return nil
}

func (f *fakeTransport) Ask(_ context.Context, q telegram.Question) (telegram.Answer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, q)
	return f.answer, f.answerErr
}

func (f *fakeTransport) AskBatch(_ context.Context, prompt string, items []telegram.BatchItem) ([]telegram.Answer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.askedBatch = append(f.askedBatch, batchAsk{Prompt: prompt, Items: items})
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	if f.batchAnswers != nil {
		return f.batchAnswers, nil
	}
	// Default: every item confirmed with its proposal (the Confirm-all path).
	out := make([]telegram.Answer, len(items))
	for i, it := range items {
		out[i] = telegram.Answer{Text: it.Proposal}
	}
	return out, nil
}

func (f *fakeTransport) Handle(name string, h telegram.CommandHandler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[name] = h
}

func (f *fakeTransport) OnImages(fn func(files []string, replyToMsgID int)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onImages = fn
}

// texts returns just the outbound SendLine strings, in order.
func (f *fakeTransport) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sent))
	for i, s := range f.sent {
		out[i] = s.Text
	}
	return out
}

// editTexts returns just the EditLine strings, in order.
func (f *fakeTransport) editTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.edits))
	for i, e := range f.edits {
		out[i] = e.Text
	}
	return out
}

// fakeDaemon records every action call so tests assert the (issue, action) each
// control reaches. It satisfies botflows.Daemon.
type fakeDaemon struct {
	mu sync.Mutex

	calls []daemonCall

	// nextIssue is the issue number Idea returns; scripted per test.
	nextIssue int
	// answer is what Ask relays; scripted per test.
	answer string
	// err, if set, is returned by every action (to test error paths).
	err error
}

// daemonCall is one recorded action: its name plus the salient arguments.
type daemonCall struct {
	Action string
	Issue  int
	Text   string // repo/model/guidance/question, per action
	Files  []string
}

func newFakeDaemon() *fakeDaemon { return &fakeDaemon{nextIssue: 42} }

func (d *fakeDaemon) record(c daemonCall) {
	d.mu.Lock()
	d.calls = append(d.calls, c)
	d.mu.Unlock()
}

func (d *fakeDaemon) Idea(_ context.Context, repo, text string) (int, error) {
	d.record(daemonCall{Action: "Idea", Issue: d.nextIssue, Text: repo + "|" + text})
	return d.nextIssue, d.err
}

func (d *fakeDaemon) Research(_ context.Context, issue int, modelID string) error {
	d.record(daemonCall{Action: "Research", Issue: issue, Text: modelID})
	return d.err
}

func (d *fakeDaemon) ProceedGate(_ context.Context, issue int) error {
	d.record(daemonCall{Action: "ProceedGate", Issue: issue})
	return d.err
}

func (d *fakeDaemon) SwapModel(_ context.Context, issue int, modelID string) error {
	d.record(daemonCall{Action: "SwapModel", Issue: issue, Text: modelID})
	return d.err
}

func (d *fakeDaemon) Steer(_ context.Context, issue int, text string) error {
	d.record(daemonCall{Action: "Steer", Issue: issue, Text: text})
	return d.err
}

func (d *fakeDaemon) Stop(_ context.Context, issue int) error {
	d.record(daemonCall{Action: "Stop", Issue: issue})
	return d.err
}

func (d *fakeDaemon) Retry(_ context.Context, issue int) error {
	d.record(daemonCall{Action: "Retry", Issue: issue})
	return d.err
}

func (d *fakeDaemon) Escalate(_ context.Context, issue int) error {
	d.record(daemonCall{Action: "Escalate", Issue: issue})
	return d.err
}

func (d *fakeDaemon) Skip(_ context.Context, issue int) error {
	d.record(daemonCall{Action: "Skip", Issue: issue})
	return d.err
}

func (d *fakeDaemon) Ask(_ context.Context, question string) (string, error) {
	d.record(daemonCall{Action: "Ask", Text: question})
	return d.answer, d.err
}

func (d *fakeDaemon) AttachImages(_ context.Context, issue int, files []string) error {
	d.record(daemonCall{Action: "AttachImages", Issue: issue, Files: files})
	return d.err
}

// callsFor returns every recorded call matching an action name.
func (d *fakeDaemon) callsFor(action string) []daemonCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []daemonCall
	for _, c := range d.calls {
		if c.Action == action {
			out = append(out, c)
		}
	}
	return out
}

// lastCall returns the most recent recorded call and whether any exist.
func (d *fakeDaemon) lastCall() (daemonCall, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.calls) == 0 {
		return daemonCall{}, false
	}
	return d.calls[len(d.calls)-1], true
}

// --- shared test constructors ---

// newTestFlows builds a Flows over fresh fakes with the given intake models
// (top first). Register is applied so command/image handlers are live.
func newTestFlows(models []registry.RunOption) (*Flows, *fakeTransport, *fakeDaemon) {
	tg := newFakeTransport()
	dae := newFakeDaemon()
	f := New(tg, dae, models)
	f.Register()
	return f, tg, dae
}
