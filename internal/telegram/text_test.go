package telegram

import (
	"context"
	"testing"

	"github.com/go-telegram/bot/models"
)

// --- AC: authorized non-command free text reaches the OnText callback ---

func TestFreeTextRoutedToOnText(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)
	var got string
	var gotReply int
	tr.OnText(func(_ context.Context, text string, replyTo int) {
		got, gotReply = text, replyTo
	})

	tr.processUpdate(context.Background(), msgFrom(testChat, testUser, "  hello there  "))

	if got != "hello there" {
		t.Fatalf("OnText text = %q, want %q", got, "hello there")
	}
	if gotReply != 0 {
		t.Fatalf("OnText replyTo = %d, want 0", gotReply)
	}
}

// --- AC: replies carry the replied-to message id ---

func TestFreeTextReplyCarriesMsgID(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)
	var gotReply int
	tr.OnText(func(_ context.Context, _ string, replyTo int) { gotReply = replyTo })

	up := msgFrom(testChat, testUser, "about that line")
	up.Message.ReplyToMessage = &models.Message{ID: 77}
	tr.processUpdate(context.Background(), up)

	if gotReply != 77 {
		t.Fatalf("OnText replyTo = %d, want 77", gotReply)
	}
}

// --- AC: commands, unauthorized senders, and empty text never reach OnText ---

func TestOnTextExclusions(t *testing.T) {
	cases := []struct {
		name string
		up   *models.Update
	}{
		{"command", msgFrom(testChat, testUser, "/status")},
		{"wrong user", msgFrom(testChat, otherUser, "hi")},
		{"wrong chat", msgFrom(otherChat, testUser, "hi")},
		{"empty", msgFrom(testChat, testUser, "   ")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeTelegram(t)
			tr := newTestTransport(t, f)
			tr.Handle("status", func(context.Context, string) {})
			called := false
			tr.OnText(func(context.Context, string, int) { called = true })

			tr.processUpdate(context.Background(), tc.up)

			if called {
				t.Fatal("OnText ran; want excluded")
			}
		})
	}
}

// --- AC: a pending alter reply is consumed by the ask, not chat ---

func TestPendingAlterWinsOverOnText(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)
	called := false
	tr.OnText(func(context.Context, string, int) { called = true })

	p := tr.asks.register("q1")
	p.wantAlter()
	tr.processUpdate(context.Background(), msgFrom(testChat, testUser, "new value"))

	if called {
		t.Fatal("OnText ran; alter reply should have consumed the line")
	}
	select {
	case line := <-p.alter:
		if line != "new value" {
			t.Fatalf("alter line = %q, want %q", line, "new value")
		}
	default:
		t.Fatal("alter reply not delivered")
	}
}

// --- AC: unregistered OnText leaves free text ignored (pre-chat behavior) ---

func TestFreeTextIgnoredWithoutOnText(t *testing.T) {
	f := newFakeTelegram(t)
	tr := newTestTransport(t, f)

	// Must not panic or send anything.
	tr.processUpdate(context.Background(), msgFrom(testChat, testUser, "hello"))

	if n := len(f.sent); n != 0 {
		t.Fatalf("transport sent %d messages, want 0", n)
	}
}
