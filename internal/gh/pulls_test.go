package gh

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// prHandler fakes the subset of the PR API used by clex.
type prHandler struct {
	created     map[string]any
	mergeCalled bool
}

func (h *prHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	// Create PR: POST /repos/o/r/pulls
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls"):
		body, _ := io.ReadAll(r.Body)
		h.created = map[string]any{}
		_ = json.Unmarshal(body, &h.created)
		resp := map[string]any{
			"number": 7,
			"title":  h.created["title"],
			"state":  "open",
			"head":   map[string]any{"ref": h.created["head"]},
			"base":   map[string]any{"ref": h.created["base"]},
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)

	// Get PR: GET /repos/o/r/pulls/7
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls/7"):
		w.Write(loadFixturePR())

	// Merge PR: PUT /repos/o/r/pulls/7/merge
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/merge"):
		h.mergeCalled = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sha":    "abc123",
			"merged": true,
		})

	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func loadFixturePR() []byte {
	return []byte(`{
		"number": 7,
		"title": "gh package",
		"state": "open",
		"merged": false,
		"mergeable": true,
		"mergeable_state": "clean",
		"head": {"ref": "clex/6-gh"},
		"base": {"ref": "clex/epic-1"}
	}`)
}

func TestOpenPRTargetsIntegrationBranch(t *testing.T) {
	h := &prHandler{}
	c := newTestClient(t, h)
	repo := Repo{Owner: "o", Name: "r"}

	pr, err := c.OpenPR(context.Background(), repo, "gh package", "clex/6-gh", "clex/epic-1", "body")
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if pr.Number != 7 {
		t.Errorf("PR number = %d, want 7", pr.Number)
	}
	// The base branch must be forwarded verbatim (integration branches are the
	// common case, not main).
	if h.created["base"] != "clex/epic-1" {
		t.Errorf("created base = %v, want clex/epic-1", h.created["base"])
	}
	if h.created["head"] != "clex/6-gh" {
		t.Errorf("created head = %v, want clex/6-gh", h.created["head"])
	}
}

func TestGetPRReportsMergeableState(t *testing.T) {
	h := &prHandler{}
	c := newTestClient(t, h)
	repo := Repo{Owner: "o", Name: "r"}

	pr, err := c.GetPR(context.Background(), repo, 7)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if pr.MergeableState != "clean" {
		t.Errorf("MergeableState = %q, want clean", pr.MergeableState)
	}
	if pr.Mergeable == nil || !*pr.Mergeable {
		t.Errorf("Mergeable = %v, want true", pr.Mergeable)
	}
	if pr.Base != "clex/epic-1" {
		t.Errorf("Base = %q, want clex/epic-1", pr.Base)
	}
}

func TestMergePRReturnsSHA(t *testing.T) {
	h := &prHandler{}
	c := newTestClient(t, h)
	repo := Repo{Owner: "o", Name: "r"}

	sha, err := c.MergePR(context.Background(), repo, 7, "squash", "merge it")
	if err != nil {
		t.Fatalf("MergePR: %v", err)
	}
	if !h.mergeCalled {
		t.Error("merge endpoint was not called")
	}
	if sha != "abc123" {
		t.Errorf("merge SHA = %q, want abc123", sha)
	}
}
