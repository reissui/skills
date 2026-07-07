package daemon

import (
	"context"
	"strings"
	"testing"
	"time"
)

// --- Criterion: free text is a chat turn — answered by the bot-role model,
// with the conversation resumed across turns ---

func TestChatTurnAnswersAndResumes(t *testing.T) {
	stages := newFakeStages()
	h := newHarness(t, stages)
	h.runDaemon(t)

	ctx := context.Background()
	if !waitFor(time.Second, func() bool { h.tg.mu.Lock(); defer h.tg.mu.Unlock(); return h.tg.onText != nil }) {
		t.Fatal("OnText never registered")
	}

	h.tg.text(ctx, "what is the state of the widget work?")
	// The fake runner streams "working" as its answer text.
	if !waitFor(2*time.Second, func() bool { return h.tg.sentContains("working") }) {
		t.Fatal("chat answer not relayed")
	}

	h.tg.text(ctx, "and what about the API?")
	if !waitFor(2*time.Second, func() bool { return len(h.rf.runner.tasks()) >= 2 }) {
		t.Fatal("second chat turn did not run")
	}

	tasks := h.rf.runner.tasks()
	// First turn opens with the preamble; the second resumes the session bare.
	if !strings.Contains(tasks[0].Prompt, "You are clex") {
		t.Errorf("first turn missing preamble: %q", tasks[0].Prompt)
	}
	if tasks[0].ResumeID != "" {
		t.Errorf("first turn ResumeID = %q, want empty", tasks[0].ResumeID)
	}
	if tasks[1].ResumeID != "sess-1" {
		t.Errorf("second turn ResumeID = %q, want sess-1 (conversation continues)", tasks[1].ResumeID)
	}
	if strings.Contains(tasks[1].Prompt, "You are clex") {
		t.Error("resumed turn repeated the preamble")
	}
	if !strings.Contains(tasks[1].Prompt, "and what about the API?") {
		t.Errorf("second turn prompt = %q", tasks[1].Prompt)
	}
}

// --- Criterion: /model shows and switches the chat model, resetting the
// conversation ---

func TestModelCommand(t *testing.T) {
	stages := newFakeStages()
	h := newHarness(t, stages)
	h.runDaemon(t)
	ctx := context.Background()

	h.tg.command(ctx, "model", "")
	if !waitFor(time.Second, func() bool { return h.tg.sentContains("chat model: fake-model") }) {
		t.Fatalf("bare /model line = %q", h.tg.lastLine())
	}

	h.tg.command(ctx, "model", "no-such-model")
	if !h.tg.sentContains("unknown model") {
		t.Fatalf("bad /model line = %q", h.tg.lastLine())
	}

	// Seed a conversation, then switch: the session must reset.
	if !waitFor(time.Second, func() bool { h.tg.mu.Lock(); defer h.tg.mu.Unlock(); return h.tg.onText != nil }) {
		t.Fatal("OnText never registered")
	}
	h.tg.text(ctx, "hello")
	if !waitFor(2*time.Second, func() bool {
		h.d.mu.Lock()
		defer h.d.mu.Unlock()
		return h.d.chat.sessionID != ""
	}) {
		t.Fatal("chat session never recorded")
	}
	h.tg.command(ctx, "model", "fake-model")
	if !h.tg.sentContains("chat model set to fake-model") {
		t.Fatalf("set /model line = %q", h.tg.lastLine())
	}
	h.d.mu.Lock()
	sess := h.d.chat.sessionID
	h.d.mu.Unlock()
	if sess != "" {
		t.Errorf("chat session = %q after /model, want reset", sess)
	}
}
