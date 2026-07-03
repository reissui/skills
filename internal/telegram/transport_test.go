package telegram

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
)

const (
	testChat  int64 = 555
	testUser  int64 = 42
	otherUser int64 = 99
	otherChat int64 = 777
)

// newTestTransport builds a Transport wired to the fake server, authorized for
// testUser in testChat, with a spool dir under t.TempDir().
func newTestTransport(t *testing.T, f *fakeTelegram) *Transport {
	t.Helper()
	tr, err := newWithAPI(f.api(t), Config{
		ChatID:         testChat,
		AllowedUserIDs: []int64{testUser},
		SpoolDir:       filepath.Join(t.TempDir(), "spool"),
		MaxImageBytes:  1 << 20,
		AlterTimeout:   time.Minute,
		AlbumWindow:    20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("newWithAPI: %v", err)
	}
	return tr
}

func msgFrom(chatID, userID int64, text string) *models.Update {
	return &models.Update{Message: &models.Message{
		ID:   1,
		Text: text,
		From: &models.User{ID: userID},
		Chat: models.Chat{ID: chatID},
	}}
}

// --- AC: updates from a non-configured chat id are ignored ---

func TestNonConfiguredChatIgnored(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)
	called := false
	tr.Handle("status", func(context.Context, string) { called = true })

	tr.processUpdate(context.Background(), msgFrom(otherChat, testUser, "/status"))

	if called {
		t.Fatal("handler ran for wrong-chat update")
	}
	wrongChat, _ := tr.DroppedCounts()
	if wrongChat != 1 {
		t.Fatalf("droppedWrongChat = %d, want 1", wrongChat)
	}
}

// --- AC: callback from a non-configured user id is dropped + counted, even in
// the right chat ---

func TestCallbackFromWrongUserDropped(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)

	// Callback in the RIGHT chat but from an unauthorized user.
	up := &models.Update{CallbackQuery: &models.CallbackQuery{
		ID:   "cbq1",
		From: models.User{ID: otherUser},
		Data: "abc:c",
		Message: models.MaybeInaccessibleMessage{
			Type:    models.MaybeInaccessibleMessageTypeMessage,
			Message: &models.Message{ID: 5, Chat: models.Chat{ID: testChat}},
		},
	}}
	tr.processUpdate(context.Background(), up)

	_, wrongUser := tr.DroppedCounts()
	if wrongUser != 1 {
		t.Fatalf("droppedWrongUser = %d, want 1", wrongUser)
	}
	// The callback must not have been answered (it was dropped before dispatch).
	if len(f.answers) != 0 {
		t.Fatalf("answered %d callbacks, want 0 (dropped)", len(f.answers))
	}
}

func TestMessageFromWrongUserDropped(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)
	called := false
	tr.Handle("status", func(context.Context, string) { called = true })

	tr.processUpdate(context.Background(), msgFrom(testChat, otherUser, "/status"))

	if called {
		t.Fatal("handler ran for unauthorized user")
	}
	_, wrongUser := tr.DroppedCounts()
	if wrongUser != 1 {
		t.Fatalf("droppedWrongUser = %d, want 1", wrongUser)
	}
}

// --- AC: SendLine/EditLine round-trip; edits reuse the message id ---

func TestSendLineEditLineRoundTrip(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)
	ctx := context.Background()

	id, err := tr.SendLine(ctx, "#42 building — 2/5")
	if err != nil {
		t.Fatalf("SendLine: %v", err)
	}
	if id == 0 {
		t.Fatal("SendLine returned msg id 0")
	}
	if err := tr.EditLine(ctx, id, "#42 building — 3/5"); err != nil {
		t.Fatalf("EditLine: %v", err)
	}

	sent := f.sentMessages()
	if len(sent) != 1 || sent[0].ChatID != testChat || sent[0].Text != "#42 building — 2/5" {
		t.Fatalf("sent = %+v", sent)
	}
	edits := f.editCalls()
	if len(edits) != 1 {
		t.Fatalf("edits = %d, want 1", len(edits))
	}
	if edits[0].MsgID != id {
		t.Fatalf("edit reused id %d, want %d", edits[0].MsgID, id)
	}
	if edits[0].Text != "#42 building — 3/5" {
		t.Fatalf("edit text = %q", edits[0].Text)
	}
}

// --- AC: command mux dispatches each registered command (table test) ---

func TestCommandMuxDispatch(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)
	commands := []string{"status", "stop", "steer", "pause", "resume", "models", "costs"}

	got := make(map[string]string)
	for _, name := range commands {
		name := name
		tr.Handle(name, func(_ context.Context, args string) { got[name] = args })
	}

	cases := []struct {
		text     string
		wantCmd  string
		wantArgs string
	}{
		{"/status", "status", ""},
		{"/stop 42", "stop", "42"},
		{"/steer 42 rethink the auth flow", "steer", "42 rethink the auth flow"},
		{"/pause", "pause", ""},
		{"/resume", "resume", ""},
		{"/models", "models", ""},
		{"/costs", "costs", ""},
		{"/status@clexbot", "status", ""}, // @botname suffix stripped
	}
	for _, tc := range cases {
		delete(got, tc.wantCmd)
		tr.processUpdate(context.Background(), msgFrom(testChat, testUser, tc.text))
		v, ok := got[tc.wantCmd]
		if !ok {
			t.Errorf("%q: handler not dispatched", tc.text)
			continue
		}
		if v != tc.wantArgs {
			t.Errorf("%q: args = %q, want %q", tc.text, v, tc.wantArgs)
		}
	}
}

func TestUnknownCommandUsageReply(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)
	tr.Handle("status", func(context.Context, string) {})

	tr.processUpdate(context.Background(), msgFrom(testChat, testUser, "/bogus now"))

	sent := f.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1 usage reply", len(sent))
	}
	if !strings.Contains(sent[0].Text, "unknown command /bogus") {
		t.Fatalf("usage reply = %q", sent[0].Text)
	}
	if !strings.Contains(sent[0].Text, "/status") {
		t.Fatalf("usage reply should list known commands, got %q", sent[0].Text)
	}
}

func TestSpoolDirCreated0700(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)
	f.stageFile("fid", "photos/x.jpg", []byte("JPEGDATA"), 8)

	if _, err := tr.spoolFile(context.Background(), "fid"); err != nil {
		t.Fatalf("spoolFile: %v", err)
	}
	info, err := os.Stat(tr.spoolDir)
	if err != nil {
		t.Fatalf("stat spool dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("spool dir perm = %o, want 700", perm)
	}
}
