package gh

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// eventsHandler serves the issue-events fixture and records ETag behavior. The
// first request returns 200 with an ETag body; every subsequent request must
// carry If-None-Match (asserted), and returns 304 Not Modified.
type eventsHandler struct {
	mu             sync.Mutex
	fixture        []byte
	etag           string
	calls          int
	sawIfNoneMatch bool
	missingCond    int // count of polls after the first that lacked If-None-Match
}

func (h *eventsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !strings.HasSuffix(r.URL.Path, "/issues/events") {
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		return
	}
	h.calls++
	inm := r.Header.Get("If-None-Match")

	if h.calls == 1 {
		// First poll: no conditional header yet; return the events + ETag.
		w.Header().Set("ETag", h.etag)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(h.fixture)
		return
	}

	// Subsequent polls MUST send If-None-Match with the ETag we handed out.
	if inm == "" {
		h.missingCond++
	} else {
		h.sawIfNoneMatch = true
	}
	if inm == h.etag {
		w.Header().Set("ETag", h.etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	// Different/absent ETag: pretend nothing new anyway (empty array).
	w.Header().Set("ETag", h.etag)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte("[]"))
}

func TestPollerEmitsTypedEventsAndFiltersUntrustedActor(t *testing.T) {
	h := &eventsHandler{
		fixture: loadFixture(t, "issue_events.json"),
		etag:    `"etag-abc"`,
	}
	c := newTestClient(t, h)
	repo := Repo{Owner: "reissui", Name: "clex"}

	trusted := &TrustedActors{Owner: "reissui", Self: "clex-bot"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := c.Poll(ctx, []Repo{repo}, 10*time.Millisecond, PollOptions{Trusted: trusted})

	// The fixture has three events, oldest-first after ordering:
	//   1001 labeled clex:building by reissui  (trusted -> emitted)
	//   1002 labeled clex:approved by mallory  (UNTRUSTED -> dropped+counted)
	//   1003 merged by reissui                 (trusted -> emitted)
	// So we expect exactly two Change values, in chronological order.
	var got []Change
	timeout := time.After(3 * time.Second)
	for len(got) < 2 {
		select {
		case change, ok := <-ch:
			if !ok {
				t.Fatal("channel closed before 2 events received")
			}
			got = append(got, change)
		case <-timeout:
			t.Fatalf("timed out; got %d events: %+v", len(got), got)
		}
	}

	// First emitted: the building label from the owner.
	if got[0].Kind != ChangeLabeled || got[0].Label != "clex:building" || got[0].Issue != 42 {
		t.Errorf("event[0] = %+v, want labeled clex:building on #42", got[0])
	}
	if got[0].Actor != "reissui" {
		t.Errorf("event[0].Actor = %q, want reissui", got[0].Actor)
	}
	// Second emitted: the merge (mallory's approved label was dropped between).
	if got[1].Kind != ChangePRMerged || got[1].Issue != 7 {
		t.Errorf("event[1] = %+v, want pr_merged on #7", got[1])
	}

	// The untrusted actor's change must have been dropped and counted.
	if d := trusted.Dropped(); d != 1 {
		t.Errorf("Dropped() = %d, want 1 (mallory's labeled event)", d)
	}

	// Let at least one more poll cycle run so we can assert conditional requests.
	deadline := time.After(2 * time.Second)
	for {
		h.mu.Lock()
		calls := h.calls
		sawCond := h.sawIfNoneMatch
		missing := h.missingCond
		h.mu.Unlock()
		if calls >= 2 && sawCond {
			if missing > 0 {
				t.Errorf("%d poll(s) after the first omitted If-None-Match", missing)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("no conditional (If-None-Match) request observed; calls=%d sawCond=%v", calls, sawCond)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestTrustedActorsSelfAndCaseInsensitive(t *testing.T) {
	ta := &TrustedActors{Owner: "Reissui", Self: "clex-bot"}
	if !ta.Trusted("reissui") {
		t.Error("owner login should be trusted case-insensitively")
	}
	if !ta.Trusted("CLEX-BOT") {
		t.Error("self login should be trusted case-insensitively")
	}
	if ta.Trusted("mallory") {
		t.Error("unknown login must not be trusted")
	}
	if ta.Trusted("") {
		t.Error("empty login must never be trusted")
	}
}
