package gh

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/reissui/clex/internal/core"
)

// issueHandler serves a fixed issue on GET and captures the label set sent to
// the ReplaceLabels endpoint (PUT /issues/{n}/labels).
type issueHandler struct {
	issueJSON     []byte
	replacedWith  []string
	replaceCalled bool
}

func (h *issueHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && hasSuffixIssuePath(r.URL.Path):
		w.Write(h.issueJSON)
	case (r.Method == http.MethodPut || r.Method == http.MethodPost) && strings.HasSuffix(r.URL.Path, "/labels"):
		h.replaceCalled = true
		body, _ := io.ReadAll(r.Body)
		// PUT labels accepts either a bare array or {"labels":[...]}.
		var arr []string
		if err := json.Unmarshal(body, &arr); err != nil {
			var wrap struct {
				Labels []string `json:"labels"`
			}
			_ = json.Unmarshal(body, &wrap)
			arr = wrap.Labels
		}
		h.replacedWith = arr
		// Echo back label objects.
		out := make([]map[string]string, 0, len(arr))
		for _, n := range arr {
			out = append(out, map[string]string{"name": n})
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

// hasSuffixIssuePath reports whether p is an issue GET path ending in
// /issues/{n} (no trailing segment).
func hasSuffixIssuePath(p string) bool {
	// path like /repos/o/r/issues/42
	i := strings.LastIndex(p, "/issues/")
	if i < 0 {
		return false
	}
	rest := p[i+len("/issues/"):]
	return rest != "" && !strings.Contains(rest, "/")
}

func TestGetIssueParsesStateAndMetadata(t *testing.T) {
	h := &issueHandler{issueJSON: loadFixture(t, "issue_building.json")}
	c := newTestClient(t, h)
	repo := Repo{Owner: "o", Name: "r"}

	iss, err := c.GetIssue(context.Background(), repo, 42)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if iss.State != core.StateBuilding {
		t.Errorf("State = %q, want %q", iss.State, core.StateBuilding)
	}
	if iss.AuthorLogin != "reissui" {
		t.Errorf("AuthorLogin = %q, want reissui", iss.AuthorLogin)
	}
	if iss.Meta.Difficulty != core.DifficultyStandard {
		t.Errorf("Meta.Difficulty = %q, want standard", iss.Meta.Difficulty)
	}
	if want := []string{"internal/gh/**"}; len(iss.Meta.Touches) != 1 || iss.Meta.Touches[0] != want[0] {
		t.Errorf("Meta.Touches = %v, want %v", iss.Meta.Touches, want)
	}
	if iss.Meta.Verify != "go test ./internal/gh/..." {
		t.Errorf("Meta.Verify = %q", iss.Meta.Verify)
	}
}

func TestSetStateLegalTransitionSwapsLabel(t *testing.T) {
	// Fixture is in clex:building; building -> review is legal.
	h := &issueHandler{issueJSON: loadFixture(t, "issue_building.json")}
	c := newTestClient(t, h)
	repo := Repo{Owner: "o", Name: "r"}

	if err := c.SetState(context.Background(), repo, 42, core.StateReview); err != nil {
		t.Fatalf("SetState building->review: %v", err)
	}
	if !h.replaceCalled {
		t.Fatal("ReplaceLabels was not called for a legal transition")
	}
	// The new label set must contain review, keep the agent tag and the non-clex
	// label, and NOT contain building.
	got := map[string]bool{}
	for _, l := range h.replacedWith {
		got[l] = true
	}
	if !got[string(core.StateReview)] {
		t.Errorf("new labels %v missing %q", h.replacedWith, core.StateReview)
	}
	if got[string(core.StateBuilding)] {
		t.Errorf("new labels %v still contain old state %q", h.replacedWith, core.StateBuilding)
	}
	if !got[AgentLabel("codex")] {
		t.Errorf("new labels %v dropped the agent tag", h.replacedWith)
	}
	if !got["enhancement"] {
		t.Errorf("new labels %v dropped the non-clex label", h.replacedWith)
	}
}

func TestSetStateIllegalTransitionReturnsTypedErrorNoWrite(t *testing.T) {
	// building -> idea is illegal.
	h := &issueHandler{issueJSON: loadFixture(t, "issue_building.json")}
	c := newTestClient(t, h)
	repo := Repo{Owner: "o", Name: "r"}

	err := c.SetState(context.Background(), repo, 42, core.StateIdea)
	if err == nil {
		t.Fatal("SetState building->idea returned nil, want TransitionError")
	}
	if !IsTransitionError(err) {
		t.Fatalf("error %v is not a *TransitionError", err)
	}
	var te *TransitionError
	if !errors.As(err, &te) {
		t.Fatalf("errors.As failed for %v", err)
	}
	if te.From != core.StateBuilding || te.To != core.StateIdea || te.Issue != 42 {
		t.Errorf("TransitionError = %+v, want {42 building idea}", te)
	}
	if h.replaceCalled {
		t.Error("ReplaceLabels was called on an illegal transition; must make no writes")
	}
}

func TestSetStateSameStateIsNoOp(t *testing.T) {
	h := &issueHandler{issueJSON: loadFixture(t, "issue_building.json")}
	c := newTestClient(t, h)
	repo := Repo{Owner: "o", Name: "r"}

	if err := c.SetState(context.Background(), repo, 42, core.StateBuilding); err != nil {
		t.Fatalf("SetState building->building: %v", err)
	}
	if h.replaceCalled {
		t.Error("ReplaceLabels called for a same-state no-op transition")
	}
}
