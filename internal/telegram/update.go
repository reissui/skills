package telegram

import (
	"context"
	"sort"
	"strings"

	"github.com/go-telegram/bot/models"
)

// processUpdate is the single entry point for every inbound update. It enforces,
// in order: the chat-id hard filter, then per-user authorization, then dispatch.
// It is written to be called directly by tests (deterministic, no worker pool)
// and by the live long-poll loop in Run.
//
// Precedence is deliberate and security-critical:
//  1. Wrong chat id  → drop + count, never look further (spec: single enforced chat).
//  2. Wrong user id  → drop + count, even in the right chat, for messages AND
//     callbacks (spec: authorization is per-user, not per-chat).
//  3. Only then dispatch to command mux / callback registry / image spool / text.
func (t *Transport) processUpdate(ctx context.Context, u *models.Update) {
	switch {
	case u.CallbackQuery != nil:
		t.handleCallback(ctx, u.CallbackQuery)
	case u.Message != nil:
		t.handleMessage(ctx, u.Message)
	default:
		// Update kinds the transport does not consume (edits, reactions, chat
		// membership, …) are ignored silently.
	}
}

// handleMessage authorizes and dispatches a plain message update.
func (t *Transport) handleMessage(ctx context.Context, m *models.Message) {
	if m.Chat.ID != t.chatID {
		t.incWrongChat()
		return
	}
	if m.From == nil || !t.authorized(m.From.ID) {
		t.incWrongUser()
		return
	}

	// Images take priority and never block: a photo (optionally part of an
	// album) is spooled and reported via OnImages. A photo message is not also
	// treated as a command or an alter reply.
	if len(m.Photo) > 0 {
		t.handlePhoto(ctx, m)
		return
	}

	text := strings.TrimSpace(m.Text)

	// A pending Ask "alter" wants the next one-line reply from the user. If one
	// is waiting and this isn't a command, route the line to it and stop.
	if !strings.HasPrefix(text, "/") && t.asks.deliverAlter(text) {
		return
	}

	if strings.HasPrefix(text, "/") {
		t.dispatchCommand(ctx, text)
		return
	}
	// Non-command, non-image free text with no pending alter is chat: hand it to
	// the registered text callback. Empty text (e.g. stickers) is ignored, as is
	// everything when no callback is registered.
	t.mu.Lock()
	onText := t.onText
	t.mu.Unlock()
	if onText != nil && text != "" {
		onText(ctx, text, replyToID(m))
	}
}

// dispatchCommand routes a "/name args" message to a registered handler, or
// replies with a one-line usage string for unknown commands (spec: "unknown
// commands get a one-line usage reply").
func (t *Transport) dispatchCommand(ctx context.Context, text string) {
	name, args := splitCommand(text)
	if h, ok := t.commands[name]; ok {
		h(ctx, args)
		return
	}
	_, _ = t.SendLine(ctx, "unknown command /"+name+" — try: "+t.commandList())
}

// splitCommand parses "/name@bot args…" into ("name", "args…"). The optional
// "@botusername" suffix Telegram appends in groups is stripped.
func splitCommand(text string) (name, args string) {
	text = strings.TrimSpace(text)
	rest := strings.TrimPrefix(text, "/")
	first, remainder, _ := strings.Cut(rest, " ")
	if at := strings.IndexByte(first, '@'); at >= 0 {
		first = first[:at]
	}
	return first, strings.TrimSpace(remainder)
}

// commandList returns the registered commands as a compact, stable "/a /b /c"
// usage hint.
func (t *Transport) commandList() string {
	if len(t.commands) == 0 {
		return "(no commands registered)"
	}
	names := make([]string, 0, len(t.commands))
	for n := range t.commands {
		names = append(names, "/"+n)
	}
	sort.Strings(names)
	return strings.Join(names, " ")
}

// handleCallback authorizes and dispatches an inline-button callback. The user
// check here is the security crux the spec calls out explicitly: a callback from
// an unauthorized user id must be dropped and counted even inside the authorized
// chat.
func (t *Transport) handleCallback(ctx context.Context, cq *models.CallbackQuery) {
	if chatID, ok := callbackChatID(cq); ok && chatID != t.chatID {
		t.incWrongChat()
		return
	}
	if !t.authorized(cq.From.ID) {
		t.incWrongUser()
		return
	}
	// Acknowledge the tap so the client stops spinning (best-effort).
	_ = t.api.AnswerCallbackQuery(ctx, cq.ID)
	t.asks.deliverCallback(cq.Data)
}

// replyToID returns the id of the message m replies to, or 0 if it is not a
// reply. #18 uses this to decide which idea/issue inbound images attach to.
func replyToID(m *models.Message) int {
	if m.ReplyToMessage != nil {
		return m.ReplyToMessage.ID
	}
	return 0
}

// callbackChatID extracts the chat id a callback's originating message belongs
// to, when available. Callbacks on inline messages have no chat, so ok is false
// and the caller relies on the user-id check alone.
func callbackChatID(cq *models.CallbackQuery) (int64, bool) {
	if cq.Message.Message != nil {
		return cq.Message.Message.Chat.ID, true
	}
	if cq.Message.InaccessibleMessage != nil {
		return cq.Message.InaccessibleMessage.Chat.ID, true
	}
	return 0, false
}
