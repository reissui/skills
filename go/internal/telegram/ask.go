package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
)

// DefaultAlterTimeout bounds how long Ask waits for a typed one-line "alter"
// reply after the user taps [alter…] before giving up and returning ErrTimeout.
const DefaultAlterTimeout = 5 * time.Minute

// ErrTimeout is returned by Ask when the user tapped [alter…] but did not send a
// reply line within the configured AlterTimeout.
var ErrTimeout = errors.New("telegram: timed out waiting for reply")

// SkipAnswer is the sentinel Text returned when the user taps [skip]. Answer.Skipped
// is the reliable way to test for it.
const SkipAnswer = "\x00skip"

// Question is a single confirm-or-alter prompt. The proposal is the recommended
// answer and is always rendered as the FIRST inline button (a single tap
// accepts it); [alter…] lets the user type a replacement line; [skip] declines.
type Question struct {
	// Prompt is the one-line question text shown above the buttons.
	Prompt string
	// Proposal is the recommended answer — the first button's payload.
	Proposal string
}

// Answer is the outcome of a Question.
type Answer struct {
	// Text is the accepted proposal (on ✓), the user's typed line (on alter), or
	// SkipAnswer (on skip).
	Text string
	// Altered is true when the user chose [alter…] and typed a replacement.
	Altered bool
	// Skipped is true when the user chose [skip].
	Skipped bool
}

// callback data tokens. A unique per-question id namespaces them so concurrent
// questions never cross-signal.
const (
	tokConfirm = "c"
	tokAlter   = "a"
	tokSkip    = "s"
)

// Ask presents a confirm-or-alter question and blocks until the user resolves it
// or ctx is cancelled. Behavior:
//
//   - ✓ <proposal>  → Answer{Text: proposal}
//   - alter…        → the bot prompts for one line; the next message line the
//     user sends becomes Answer{Text: line, Altered: true} (ErrTimeout if none
//     arrives within AlterTimeout)
//   - skip          → Answer{Text: SkipAnswer, Skipped: true}
func (t *Transport) Ask(ctx context.Context, q Question) (Answer, error) {
	id := newToken()
	pend := t.asks.register(id)
	defer t.asks.unregister(id)

	markup := askKeyboard(id, q.Proposal)
	if _, err := t.api.SendMessage(ctx, t.chatID, q.Prompt, markup); err != nil {
		return Answer{}, err
	}

	select {
	case data := <-pend.cb:
		return t.resolve(ctx, q, pend, data)
	case <-ctx.Done():
		return Answer{}, ctx.Err()
	}
}

// resolve turns a tapped button into an Answer, handling the alter follow-up.
func (t *Transport) resolve(ctx context.Context, q Question, pend *pendingAsk, data string) (Answer, error) {
	switch buttonToken(data) {
	case tokConfirm:
		return Answer{Text: q.Proposal}, nil
	case tokSkip:
		return Answer{Text: SkipAnswer, Skipped: true}, nil
	case tokAlter:
		return t.awaitAlter(ctx, pend)
	default:
		// Unknown token: treat as skip rather than hang.
		return Answer{Text: SkipAnswer, Skipped: true}, nil
	}
}

// awaitAlter prompts for and waits on a single typed reply line, bounded by the
// alter timeout.
func (t *Transport) awaitAlter(ctx context.Context, pend *pendingAsk) (Answer, error) {
	pend.wantAlter()
	if _, err := t.SendLine(ctx, "reply with one line to change it:"); err != nil {
		return Answer{}, err
	}
	select {
	case line := <-pend.alter:
		return Answer{Text: line, Altered: true}, nil
	case <-t.clock.After(t.alterTimeout):
		return Answer{}, ErrTimeout
	case <-ctx.Done():
		return Answer{}, ctx.Err()
	}
}

// BatchItem is one row of a batched question (the plan gate). Each item gets its
// own [✓]/[alter…]/[skip] buttons; a single [Confirm all] accepts every proposal
// at once (spec: "confirmable with a single Confirm all tap or altered per item").
type BatchItem struct {
	// Label is the short description of the item (e.g. "auth strategy").
	Label string
	// Proposal is the recommended answer for this item.
	Proposal string
}

// AskBatch presents numbered items with per-item buttons plus one [Confirm all].
// Tapping [Confirm all] returns every item's proposal in order. Otherwise items
// are resolved individually (✓/alter/skip) and the call returns once all are
// resolved. The returned slice is index-aligned with items.
func (t *Transport) AskBatch(ctx context.Context, prompt string, items []BatchItem) ([]Answer, error) {
	if len(items) == 0 {
		return nil, nil
	}
	id := newToken()
	pend := t.asks.registerBatch(id, len(items))
	defer t.asks.unregister(id)

	text := renderBatch(prompt, items)
	markup := batchKeyboard(id, items)
	if _, err := t.api.SendMessage(ctx, t.chatID, text, markup); err != nil {
		return nil, err
	}

	answers := make([]Answer, len(items))
	resolved := make([]bool, len(items))
	remaining := len(items)

	for remaining > 0 {
		select {
		case data := <-pend.cb:
			tok, idx := batchToken(data)
			if tok == tokConfirmAll {
				for i := range items {
					if !resolved[i] {
						answers[i] = Answer{Text: items[i].Proposal}
					}
				}
				return answers, nil
			}
			if idx < 0 || idx >= len(items) || resolved[idx] {
				continue
			}
			switch tok {
			case tokConfirm:
				answers[idx] = Answer{Text: items[idx].Proposal}
			case tokSkip:
				answers[idx] = Answer{Text: SkipAnswer, Skipped: true}
			case tokAlter:
				a, err := t.awaitAlter(ctx, pend)
				if err != nil {
					return nil, err
				}
				answers[idx] = a
			default:
				continue
			}
			resolved[idx] = true
			remaining--
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return answers, nil
}

const tokConfirmAll = "A"

// --- keyboard rendering ---

func askKeyboard(id, proposal string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: "✓ " + proposal, CallbackData: cbData(id, tokConfirm)},
			{Text: "alter…", CallbackData: cbData(id, tokAlter)},
			{Text: "skip", CallbackData: cbData(id, tokSkip)},
		}},
	}
}

func batchKeyboard(id string, items []BatchItem) *models.InlineKeyboardMarkup {
	rows := make([][]models.InlineKeyboardButton, 0, len(items)+1)
	for i := range items {
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: itemLabel(i, "✓"), CallbackData: cbDataN(id, tokConfirm, i)},
			{Text: "alter…", CallbackData: cbDataN(id, tokAlter, i)},
			{Text: "skip", CallbackData: cbDataN(id, tokSkip, i)},
		})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "Confirm all", CallbackData: cbData(id, tokConfirmAll)},
	})
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func itemLabel(i int, prefix string) string {
	return prefix + " " + strconv.Itoa(i+1)
}

func renderBatch(prompt string, items []BatchItem) string {
	var b []byte
	b = append(b, prompt...)
	for i, it := range items {
		b = append(b, '\n')
		b = append(b, strconv.Itoa(i+1)...)
		b = append(b, '.', ' ')
		b = append(b, it.Label...)
		b = append(b, ':', ' ')
		b = append(b, it.Proposal...)
	}
	return string(b)
}

// --- callback data encoding: "<id>:<tok>" or "<id>:<tok>:<index>" ---

func cbData(id, tok string) string { return id + ":" + tok }

func cbDataN(id, tok string, idx int) string {
	return id + ":" + tok + ":" + strconv.Itoa(idx)
}

// questionID returns the leading id segment of a callback payload, used to route
// a tap to the owning question.
func questionID(data string) string {
	if i := strings.IndexByte(data, ':'); i >= 0 {
		return data[:i]
	}
	return data
}

// buttonToken returns the token segment of a single (non-batch) callback payload.
func buttonToken(data string) string {
	parts := strings.Split(data, ":")
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

// batchToken returns the token and item index of a batch callback payload. For
// [Confirm all] and any non-indexed token the index is -1.
func batchToken(data string) (tok string, idx int) {
	parts := strings.Split(data, ":")
	if len(parts) < 2 {
		return "", -1
	}
	tok = parts[1]
	if len(parts) >= 3 {
		if n, err := strconv.Atoi(parts[2]); err == nil {
			return tok, n
		}
	}
	return tok, -1
}

func newToken() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
