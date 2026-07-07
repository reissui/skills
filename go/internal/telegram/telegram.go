// Package telegram is the clex Telegram transport: the mechanics of talking to
// a single authorized chat over long-polling. It is deliberately thin — it moves
// bytes, edits progress lines in place, renders confirm-or-alter keyboards, and
// spools inbound images. It does NOT decide conversation flow, attachment, or
// pipeline actions; those live in the intake/gates layer (issue #18).
//
// Design choices (spec: Telegram bot → Interaction principles; Security model):
//
//   - Library: github.com/go-telegram/bot. Chosen over telebot for its explicit
//     context.Context threading (every call takes a ctx, matching the daemon's
//     cancellation model), its injectable HttpClient + custom server URL (so the
//     whole transport tests against an httptest fake with zero live Telegram),
//     and its plain update-struct handlers (no hidden global router state). The
//     transport owns update processing directly rather than leaning on the
//     library's worker pool, which keeps chat/user authorization and drop-counting
//     deterministic and unit-testable.
//   - Every update is hard-filtered to the single configured chat id; anything
//     else is dropped and counted, never processed.
//   - Authorization is per-USER, not per-chat: every message AND every inline
//     callback validates the sender's Telegram user id; failures are dropped and
//     counted (spec: "Telegram authorization is per-user, not per-chat").
//   - All Telegram I/O goes through the unexported api interface so tests can
//     substitute a fake; the production api is backed by *bot.Bot.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// DefaultMaxImageBytes is the fallback per-image size limit for spooled images
// when Config.MaxImageBytes is left zero (spec: images get size limits).
const DefaultMaxImageBytes int64 = 10 << 20 // 10 MiB

// api is the minimal surface of the Telegram Bot API the transport needs. It
// exists so tests run against a fake; the production implementation is botAPI,
// which wraps *bot.Bot. Keeping it small keeps the fake small.
type api interface {
	// SendMessage posts a message and returns the created message id.
	SendMessage(ctx context.Context, chatID int64, text string, markup models.ReplyMarkup) (msgID int, err error)
	// EditMessageText edits an existing message in place, reusing its id.
	EditMessageText(ctx context.Context, chatID int64, msgID int, text string, markup models.ReplyMarkup) error
	// AnswerCallbackQuery acknowledges a tapped inline button so the client
	// stops showing a spinner. Best-effort: errors are non-fatal.
	AnswerCallbackQuery(ctx context.Context, callbackID string) error
	// GetFile resolves a Telegram file id to a downloadable file descriptor.
	GetFile(ctx context.Context, fileID string) (*models.File, error)
	// Download fetches the bytes of a resolved file (bounded by the caller).
	Download(ctx context.Context, f *models.File) ([]byte, error)
}

// Config configures a Transport.
type Config struct {
	// Token is the bot token from @BotFather.
	Token string
	// ChatID is the single authorized chat id. Updates from any other chat are
	// dropped and counted (spec: "Single authorized chat id (enforced)").
	ChatID int64
	// AllowedUserIDs is the set of Telegram user ids permitted to drive the bot.
	// Every message and callback sender is checked against this set; others are
	// dropped and counted (spec: authorization is per-user, not per-chat). If
	// empty, no user is authorized (fail closed).
	AllowedUserIDs []int64
	// SpoolDir is the directory inbound images are written to. Created 0700 if
	// absent. If empty, image handling is disabled.
	SpoolDir string
	// MaxImageBytes caps a single spooled image; larger downloads are rejected.
	// Zero means DefaultMaxImageBytes.
	MaxImageBytes int64
	// AlterTimeout bounds how long Ask waits for a one-line "alter" reply before
	// giving up. Zero means DefaultAlterTimeout.
	AlterTimeout time.Duration
	// AlbumWindow is how long the transport waits to gather the remaining photos
	// of a media group (album) before invoking OnImages once with all of them.
	// Zero means DefaultAlbumWindow.
	AlbumWindow time.Duration
}

// CommandHandler handles a slash command. args is the message text with the
// leading "/command" token removed and surrounding space trimmed.
type CommandHandler func(ctx context.Context, args string)

// Transport is the Telegram transport. It is safe for concurrent use.
type Transport struct {
	api     api
	chatID  int64
	allowed map[int64]bool

	spoolDir     string
	maxImgBytes  int64
	alterTimeout time.Duration
	albumWindow  time.Duration

	// counters, guarded by mu.
	mu               sync.Mutex
	droppedWrongChat int64
	droppedWrongUser int64

	// commands maps a bare command name (without the leading slash) to its
	// handler. Registered before Run; read-only during Run.
	commands map[string]CommandHandler

	// onImages, if set, is invoked after an image (or album) is spooled.
	onImages func(files []string, replyToMsgID int)

	// onText, if set, is invoked for authorized non-command, non-image free text
	// that no pending alter consumed. Unset, such text is ignored (pre-chat
	// behavior).
	onText func(ctx context.Context, text string, replyToMsgID int)

	// pending tracks in-flight Ask questions awaiting a callback, keyed by the
	// question's callback token, and text "alter" replies awaiting a line.
	asks *askRegistry

	// albums aggregates multi-photo media groups keyed by media_group_id.
	albums *albumBuffer

	// poller drives the live long-poll loop (production only); nil in tests,
	// which feed processUpdate directly.
	poller starter

	// clock is injectable for deterministic timeout tests.
	clock clock
}

// New constructs a Transport that talks to Telegram through the production API
// (a *bot.Bot) and drives a live long-poll loop when Run is called. Only the
// single configured chat's updates are requested and, regardless, hard-filtered
// on receipt. The default handler routes every update through processUpdate so
// authorization is enforced in one place.
func New(cfg Config) (*Transport, error) {
	var t *Transport
	b, err := bot.New(cfg.Token,
		bot.WithDefaultHandler(func(ctx context.Context, _ *bot.Bot, u *models.Update) {
			t.processUpdate(ctx, u)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("telegram: init bot: %w", err)
	}
	t, err = newWithAPI(&botAPI{b: b}, cfg)
	if err != nil {
		return nil, err
	}
	t.poller = b
	return t, nil
}

// newWithAPI is the shared constructor used by both New and tests. It performs
// no network I/O of its own.
func newWithAPI(a api, cfg Config) (*Transport, error) {
	if cfg.ChatID == 0 {
		return nil, errors.New("telegram: ChatID must be set")
	}
	allowed := make(map[int64]bool, len(cfg.AllowedUserIDs))
	for _, id := range cfg.AllowedUserIDs {
		allowed[id] = true
	}
	maxImg := cfg.MaxImageBytes
	if maxImg <= 0 {
		maxImg = DefaultMaxImageBytes
	}
	alterTO := cfg.AlterTimeout
	if alterTO <= 0 {
		alterTO = DefaultAlterTimeout
	}
	albumW := cfg.AlbumWindow
	if albumW <= 0 {
		albumW = DefaultAlbumWindow
	}
	t := &Transport{
		api:          a,
		chatID:       cfg.ChatID,
		allowed:      allowed,
		spoolDir:     cfg.SpoolDir,
		maxImgBytes:  maxImg,
		alterTimeout: alterTO,
		albumWindow:  albumW,
		commands:     make(map[string]CommandHandler),
		asks:         newAskRegistry(),
		clock:        realClock{},
	}
	t.albums = newAlbumBuffer(t.albumWindow, t.flushAlbum)
	return t, nil
}

// SendLine sends a one-line progress/notification message and returns its id so
// callers can later edit it in place (spec: progress messages are one line,
// edited in place). No markup.
func (t *Transport) SendLine(ctx context.Context, text string) (int, error) {
	return t.api.SendMessage(ctx, t.chatID, text, nil)
}

// EditLine edits a previously sent line in place, reusing msgID. This is the
// edit-in-place progress primitive: builders overwrite "#42 building — 2/5" with
// "#42 building — 3/5" instead of stacking messages.
func (t *Transport) EditLine(ctx context.Context, msgID int, text string) error {
	return t.api.EditMessageText(ctx, t.chatID, msgID, text, nil)
}

// OnImages registers the callback invoked when inbound images (a single photo or
// a whole album) finish spooling. files are absolute paths in the spool dir;
// replyToMsgID is the id of the message the images replied to (0 if none), which
// #18 uses to decide what they attach to. The transport itself never blocks or
// interrupts anything on images (spec: "Images queue, they don't interrupt").
func (t *Transport) OnImages(fn func(files []string, replyToMsgID int)) {
	t.mu.Lock()
	t.onImages = fn
	t.mu.Unlock()
}

// OnText registers the callback invoked for authorized free text that is not a
// command, not an image, and not a pending alter reply. text is trimmed;
// replyToMsgID is the id of the message the text replied to (0 if none). The
// chat layer (daemon) decides what a message means — the transport only moves
// it. Must be called before Run.
func (t *Transport) OnText(fn func(ctx context.Context, text string, replyToMsgID int)) {
	t.mu.Lock()
	t.onText = fn
	t.mu.Unlock()
}

// Handle registers a handler for a slash command. name is given without the
// leading slash ("status", not "/status"). Registering the same name twice
// replaces the earlier handler. Must be called before Run.
func (t *Transport) Handle(name string, h CommandHandler) {
	t.commands[strings.TrimPrefix(name, "/")] = h
}

// DroppedCounts returns how many updates were dropped because they came from the
// wrong chat id, and how many callbacks/messages were dropped because they came
// from an unauthorized user id. Exposed for observability and tests.
func (t *Transport) DroppedCounts() (wrongChat, wrongUser int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.droppedWrongChat, t.droppedWrongUser
}

func (t *Transport) incWrongChat() {
	t.mu.Lock()
	t.droppedWrongChat++
	t.mu.Unlock()
}

func (t *Transport) incWrongUser() {
	t.mu.Lock()
	t.droppedWrongUser++
	t.mu.Unlock()
}

func (t *Transport) authorized(userID int64) bool {
	return t.allowed[userID]
}
