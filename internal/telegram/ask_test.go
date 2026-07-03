package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
)

// callbackUpdate builds an authorized callback update carrying data.
func callbackUpdate(data string) *models.Update {
	return &models.Update{CallbackQuery: &models.CallbackQuery{
		ID:   "cbq",
		From: models.User{ID: testUser},
		Data: data,
		Message: models.MaybeInaccessibleMessage{
			Type:    models.MaybeInaccessibleMessageTypeMessage,
			Message: &models.Message{ID: 1, Chat: models.Chat{ID: testChat}},
		},
	}}
}

// lastMarkup returns the inline keyboard of the most recently sent message.
func lastMarkup(t *testing.T, f *fakeTelegram) *models.InlineKeyboardMarkup {
	t.Helper()
	sent := f.sentMessages()
	if len(sent) == 0 || sent[len(sent)-1].Markup == nil {
		t.Fatal("no sent message with inline keyboard")
	}
	return sent[len(sent)-1].Markup
}

// buttonData finds the callback_data of the button at [row][col].
func buttonData(mk *models.InlineKeyboardMarkup, row, col int) string {
	return mk.InlineKeyboard[row][col].CallbackData
}

// --- AC: Ask returns the proposal on ✓ ---

func TestAskConfirmReturnsProposal(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)
	ctx := context.Background()

	resCh := make(chan Answer, 1)
	go func() {
		a, err := tr.Ask(ctx, Question{Prompt: "Auth?", Proposal: "magic link"})
		if err != nil {
			t.Errorf("Ask: %v", err)
		}
		resCh <- a
	}()

	data := waitButton(t, f, 0, 0) // first button is ✓ <proposal>
	tr.processUpdate(ctx, callbackUpdate(data))

	a := <-resCh
	if a.Text != "magic link" || a.Altered || a.Skipped {
		t.Fatalf("Answer = %+v, want {magic link}", a)
	}
	// First button text carries the checkmark + proposal.
	if got := lastMarkup(t, f).InlineKeyboard[0][0].Text; got != "✓ magic link" {
		t.Fatalf("first button = %q, want '✓ magic link'", got)
	}
}

// --- AC: Ask returns the typed reply on alter ---

func TestAskAlterReturnsTypedReply(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)
	ctx := context.Background()

	resCh := make(chan Answer, 1)
	go func() {
		a, err := tr.Ask(ctx, Question{Prompt: "Auth?", Proposal: "magic link"})
		if err != nil {
			t.Errorf("Ask: %v", err)
		}
		resCh <- a
	}()

	alterData := waitButton(t, f, 0, 1) // second button is alter…
	tr.processUpdate(ctx, callbackUpdate(alterData))

	// The bot now waits for a one-line reply; deliver it as a normal message.
	waitAltering(t, tr)
	tr.processUpdate(ctx, msgFrom(testChat, testUser, "use OAuth device flow"))

	a := <-resCh
	if !a.Altered || a.Text != "use OAuth device flow" {
		t.Fatalf("Answer = %+v, want altered 'use OAuth device flow'", a)
	}
}

// --- AC: Ask returns a skip sentinel on skip ---

func TestAskSkipReturnsSentinel(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)
	ctx := context.Background()

	resCh := make(chan Answer, 1)
	go func() {
		a, _ := tr.Ask(ctx, Question{Prompt: "Auth?", Proposal: "magic link"})
		resCh <- a
	}()

	skipData := waitButton(t, f, 0, 2) // third button is skip
	tr.processUpdate(ctx, callbackUpdate(skipData))

	a := <-resCh
	if !a.Skipped || a.Text != SkipAnswer {
		t.Fatalf("Answer = %+v, want skip sentinel", a)
	}
}

// --- Ask alter times out when no reply arrives ---

func TestAskAlterTimeout(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)
	fc := &fakeClock{fire: make(chan time.Time, 1)}
	tr.clock = fc
	ctx := context.Background()

	errCh := make(chan error, 1)
	go func() {
		_, err := tr.Ask(ctx, Question{Prompt: "Auth?", Proposal: "magic link"})
		errCh <- err
	}()

	alterData := waitButton(t, f, 0, 1)
	tr.processUpdate(ctx, callbackUpdate(alterData))
	waitAltering(t, tr)
	fc.fire <- time.Now() // trip the timeout

	if err := <-errCh; !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

// --- AC: batched form's Confirm all returns all proposals ---

func TestAskBatchConfirmAll(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)
	ctx := context.Background()

	items := []BatchItem{
		{Label: "auth", Proposal: "magic link"},
		{Label: "db", Proposal: "sqlite"},
		{Label: "queue", Proposal: "in-memory"},
	}

	resCh := make(chan []Answer, 1)
	go func() {
		as, err := tr.AskBatch(ctx, "Plan questions:", items)
		if err != nil {
			t.Errorf("AskBatch: %v", err)
		}
		resCh <- as
	}()

	mk := waitMarkup(t, f)
	// Last row is the single [Confirm all] button.
	confirmAll := mk.InlineKeyboard[len(mk.InlineKeyboard)-1][0].CallbackData
	tr.processUpdate(ctx, callbackUpdate(confirmAll))

	as := <-resCh
	if len(as) != len(items) {
		t.Fatalf("got %d answers, want %d", len(as), len(items))
	}
	for i, want := range []string{"magic link", "sqlite", "in-memory"} {
		if as[i].Text != want || as[i].Skipped || as[i].Altered {
			t.Fatalf("answers[%d] = %+v, want %q", i, as[i], want)
		}
	}
}

// TestAskBatchPerItem resolves items individually (confirm one, skip one, confirm
// the last) to prove per-item buttons work alongside Confirm all.
func TestAskBatchPerItem(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)
	ctx := context.Background()

	items := []BatchItem{
		{Label: "auth", Proposal: "magic link"},
		{Label: "db", Proposal: "sqlite"},
	}
	resCh := make(chan []Answer, 1)
	go func() {
		as, _ := tr.AskBatch(ctx, "Plan:", items)
		resCh <- as
	}()

	mk := waitMarkup(t, f)
	// Row i: [✓ i] [alter] [skip]; confirm item 0, skip item 1.
	tr.processUpdate(ctx, callbackUpdate(mk.InlineKeyboard[0][0].CallbackData)) // confirm 0
	tr.processUpdate(ctx, callbackUpdate(mk.InlineKeyboard[1][2].CallbackData)) // skip 1

	as := <-resCh
	if as[0].Text != "magic link" || as[0].Skipped {
		t.Fatalf("item0 = %+v", as[0])
	}
	if !as[1].Skipped {
		t.Fatalf("item1 = %+v, want skipped", as[1])
	}
}

// --- helpers ---

// waitButton polls until Ask has sent its keyboard, then returns the callback
// data at [row][col].
func waitButton(t *testing.T, f *fakeTelegram, row, col int) string {
	t.Helper()
	mk := waitMarkup(t, f)
	return buttonData(mk, row, col)
}

func waitMarkup(t *testing.T, f *fakeTelegram) *models.InlineKeyboardMarkup {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sent := f.sentMessages()
		if len(sent) > 0 && sent[len(sent)-1].Markup != nil {
			return sent[len(sent)-1].Markup
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("timed out waiting for Ask keyboard")
	return nil
}

// waitAltering polls until exactly one pending question is awaiting an alter line.
func waitAltering(t *testing.T, tr *Transport) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tr.asks.mu.Lock()
		altering := false
		for _, p := range tr.asks.pending {
			if p.isAltering() {
				altering = true
				break
			}
		}
		tr.asks.mu.Unlock()
		if altering {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("timed out waiting for alter state")
}

type fakeClock struct{ fire chan time.Time }

func (c *fakeClock) After(time.Duration) <-chan time.Time { return c.fire }
