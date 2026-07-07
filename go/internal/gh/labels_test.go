package gh

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-github/v66/github"
	"github.com/reissui/clex/internal/core"
)

// labelServer is a minimal in-memory fake of the GitHub labels API, tracking how
// many create/edit calls it received so tests can assert idempotency.
type labelServer struct {
	mu      sync.Mutex
	labels  map[string]*github.Label // keyed by lower-cased name
	creates int
	edits   int
}

func newLabelServer(seed ...*github.Label) *labelServer {
	s := &labelServer{labels: map[string]*github.Label{}}
	for _, l := range seed {
		s.labels[strings.ToLower(l.GetName())] = l
	}
	return s
}

func (s *labelServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	switch {
	// List labels: GET /repos/o/r/labels
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/labels"):
		out := make([]*github.Label, 0, len(s.labels))
		for _, l := range s.labels {
			out = append(out, l)
		}
		_ = json.NewEncoder(w).Encode(out)

	// Create label: POST /repos/o/r/labels
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
		var in github.Label
		_ = json.NewDecoder(r.Body).Decode(&in)
		s.creates++
		s.labels[strings.ToLower(in.GetName())] = &in
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(&in)

	// Edit label: PATCH /repos/o/r/labels/{name}
	case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/labels/"):
		var in github.Label
		_ = json.NewDecoder(r.Body).Decode(&in)
		s.edits++
		s.labels[strings.ToLower(in.GetName())] = &in
		_ = json.NewEncoder(w).Encode(&in)

	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func TestEnsureLabelsCreatesFullSet(t *testing.T) {
	srv := newLabelServer()
	c := newTestClient(t, srv)
	repo := Repo{Owner: "o", Name: "r"}

	if err := c.EnsureLabels(context.Background(), repo, []string{"codex", "claude"}); err != nil {
		t.Fatalf("EnsureLabels: %v", err)
	}

	// 7 pipeline states + epic marker + 2 agent tags = 10 creates on a fresh repo.
	wantCreates := 7 + 1 + 2
	if srv.creates != wantCreates {
		t.Errorf("creates = %d, want %d", srv.creates, wantCreates)
	}
	if srv.edits != 0 {
		t.Errorf("edits = %d, want 0 on first run", srv.edits)
	}
	// Spot-check a couple of expected labels exist.
	for _, name := range []string{string(core.StateBuilding), string(core.StateEpic), AgentLabel("codex")} {
		if _, ok := srv.labels[strings.ToLower(name)]; !ok {
			t.Errorf("label %q was not created", name)
		}
	}
}

func TestEnsureLabelsIdempotentSecondRun(t *testing.T) {
	srv := newLabelServer()
	c := newTestClient(t, srv)
	repo := Repo{Owner: "o", Name: "r"}
	ctx := context.Background()

	if err := c.EnsureLabels(ctx, repo, []string{"codex"}); err != nil {
		t.Fatalf("first EnsureLabels: %v", err)
	}
	createsAfterFirst := srv.creates
	editsAfterFirst := srv.edits

	// Second run against the now-populated repo must make ZERO writes.
	if err := c.EnsureLabels(ctx, repo, []string{"codex"}); err != nil {
		t.Fatalf("second EnsureLabels: %v", err)
	}
	if srv.creates != createsAfterFirst {
		t.Errorf("second run created %d new labels, want 0 (not idempotent)", srv.creates-createsAfterFirst)
	}
	if srv.edits != editsAfterFirst {
		t.Errorf("second run edited %d labels, want 0 (not idempotent)", srv.edits-editsAfterFirst)
	}
}

func TestEnsureLabelsEditsDriftedLabel(t *testing.T) {
	// A pre-existing clex label with the wrong color must be corrected (edited),
	// not duplicated.
	drifted := &github.Label{
		Name:        github.String(string(core.StateBuilding)),
		Color:       github.String("000000"),
		Description: github.String("stale description"),
	}
	srv := newLabelServer(drifted)
	c := newTestClient(t, srv)
	repo := Repo{Owner: "o", Name: "r"}

	if err := c.EnsureLabels(context.Background(), repo, nil); err != nil {
		t.Fatalf("EnsureLabels: %v", err)
	}
	if srv.edits < 1 {
		t.Errorf("edits = %d, want >=1 (drifted label should be corrected)", srv.edits)
	}
	got := srv.labels[strings.ToLower(string(core.StateBuilding))]
	if got.GetColor() == "000000" {
		t.Error("drifted color was not corrected")
	}
}
