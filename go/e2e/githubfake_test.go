//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub is an in-memory GitHub REST API double that backs clex's real
// *gh.Client (pointed at it via WithBaseURL) for the end-to-end suite. It
// implements exactly the endpoints the daemon and pipeline exercise — issues,
// labels, comments, the issue-events poller, and pull requests — with an
// in-memory store. Crucially, a pull-request MERGE is not simulated: it runs a
// real `git merge` of the head branch into the base branch in the primary
// checkout and pushes it to the bare origin, so the workspace manager's later
// rebase-onto-main sees genuine merged content. Merging a PR also closes its
// linked issue, which is how the daemon detects "all children landed" and
// assembles the epic (spec: Testing strategy — no live GitHub; real scratch git
// repo).
//
// It is concurrency-safe: the daemon drives builds from multiple goroutines, so
// every mutation is guarded by a single mutex.
type fakeGitHub struct {
	t        *testing.T
	repoDir  string // primary checkout (has "origin" → bare remote)
	mergeDir string // a SEPARATE checkout used only for server-side merges, so
	// merging never disturbs the branch state of the primary checkout the
	// workspace manager operates on.

	mu       sync.Mutex
	nextNum  int
	issues   map[int]*fakeIssue
	prs      map[int]*fakePR
	comments map[int][]string // issue/PR number → comment bodies
	reviews  map[int][]string // PR number → review events ("APPROVE"/...)
	merges   []int            // PR numbers merged to a base of main, in order

	// mergeMu serializes the real git merges (which share the merge checkout).
	mergeMu sync.Mutex
}

// fakeIssue is the mutable server-side state of one issue.
type fakeIssue struct {
	Number int
	Title  string
	Body   string
	Author string
	Labels []string
	Closed bool
	IsPR   bool
}

// fakePR is the mutable server-side state of one pull request.
type fakePR struct {
	Number int
	Title  string
	Head   string
	Base   string
	Body   string
	Merged bool
	State  string // "open" / "closed"
}

// newFakeGitHub builds a GitHub double bound to a primary checkout directory. It
// clones the same origin into a private merge checkout so server-side merges are
// isolated from the workspace manager's use of the primary checkout.
func newFakeGitHub(t *testing.T, repoDir, mergeDir string) *fakeGitHub {
	return &fakeGitHub{
		t:        t,
		repoDir:  repoDir,
		mergeDir: mergeDir,
		nextNum:  1,
		issues:   map[int]*fakeIssue{},
		prs:      map[int]*fakePR{},
		comments: map[int][]string{},
		reviews:  map[int][]string{},
	}
}

// issueBranchRe extracts an issue number from a clex issue branch (clex/<n>-slug).
var issueBranchRe = regexp.MustCompile(`^clex/(\d+)-`)

// pathRe matches the REST paths the fake serves. owner/name are captured but
// unused (single-repo test).
var (
	reIssues       = regexp.MustCompile(`^/repos/[^/]+/[^/]+/issues$`)
	reIssueNum     = regexp.MustCompile(`^/repos/[^/]+/[^/]+/issues/(\d+)$`)
	reIssueLabels  = regexp.MustCompile(`^/repos/[^/]+/[^/]+/issues/(\d+)/labels$`)
	reIssueComment = regexp.MustCompile(`^/repos/[^/]+/[^/]+/issues/(\d+)/comments$`)
	reIssueEvents  = regexp.MustCompile(`^/repos/[^/]+/[^/]+/issues/events$`)
	rePulls        = regexp.MustCompile(`^/repos/[^/]+/[^/]+/pulls$`)
	rePullNum      = regexp.MustCompile(`^/repos/[^/]+/[^/]+/pulls/(\d+)$`)
	rePullReviews  = regexp.MustCompile(`^/repos/[^/]+/[^/]+/pulls/(\d+)/reviews$`)
	rePullMerge    = regexp.MustCompile(`^/repos/[^/]+/[^/]+/pulls/(\d+)/merge$`)
	reLabels       = regexp.MustCompile(`^/repos/[^/]+/[^/]+/labels$`)
	reLabelName    = regexp.MustCompile(`^/repos/[^/]+/[^/]+/labels/.+$`)
)

// ServeHTTP routes a request to the matching handler. Unhandled routes return
// 404 with a body so a missing endpoint surfaces loudly in test output rather
// than hanging.
func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case reIssueEvents.MatchString(p) && r.Method == http.MethodGet:
		// The poller relies on ETags + events; the reconcile ticker drives
		// progress, so an empty event list is sufficient and keeps the poll cheap.
		w.Header().Set("ETag", `"e2e-static"`)
		writeJSON(w, http.StatusOK, []any{})
	case reIssues.MatchString(p) && r.Method == http.MethodGet:
		f.listIssues(w, r)
	case reIssues.MatchString(p) && r.Method == http.MethodPost:
		f.createIssue(w, r)
	case reIssueNum.MatchString(p) && r.Method == http.MethodGet:
		f.getIssue(w, r, num(reIssueNum, p))
	case reIssueNum.MatchString(p) && r.Method == http.MethodPatch:
		f.editIssue(w, r, num(reIssueNum, p))
	case reIssueLabels.MatchString(p) && (r.Method == http.MethodPut || r.Method == http.MethodPost):
		f.setLabels(w, r, num(reIssueLabels, p), r.Method == http.MethodPut)
	case reIssueComment.MatchString(p) && r.Method == http.MethodPost:
		f.addComment(w, r, num(reIssueComment, p))
	case rePulls.MatchString(p) && r.Method == http.MethodPost:
		f.createPR(w, r)
	case rePullNum.MatchString(p) && r.Method == http.MethodGet:
		f.getPR(w, r, num(rePullNum, p))
	case rePullReviews.MatchString(p) && r.Method == http.MethodPost:
		f.addReview(w, r, num(rePullReviews, p))
	case rePullMerge.MatchString(p) && r.Method == http.MethodPut:
		f.mergePR(w, r, num(rePullMerge, p))
	case reLabels.MatchString(p) && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, []any{})
	case reLabels.MatchString(p) && r.Method == http.MethodPost:
		writeJSON(w, http.StatusCreated, map[string]any{"name": "ok"})
	case reLabelName.MatchString(p):
		// EnsureLabels may GET/PATCH individual labels; accept idempotently.
		writeJSON(w, http.StatusOK, map[string]any{"name": "ok"})
	default:
		http.Error(w, "fakeGitHub: unhandled "+r.Method+" "+p, http.StatusNotFound)
	}
}

// --- issue handlers ---

func (f *fakeGitHub) listIssues(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	nums := make([]int, 0, len(f.issues))
	for n := range f.issues {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	var out []map[string]any
	for _, n := range nums {
		iss := f.issues[n]
		if iss.Closed || iss.IsPR {
			continue // ListByRepo(state=open) skips closed; PRs excluded from issues
		}
		out = append(out, issueJSON(iss))
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *fakeGitHub) createIssue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title  string   `json:"title"`
		Body   string   `json:"body"`
		Labels []string `json:"labels"`
	}
	decode(r, &req)
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.nextNum
	f.nextNum++
	iss := &fakeIssue{Number: n, Title: req.Title, Body: req.Body, Author: "clex-bot", Labels: req.Labels}
	f.issues[n] = iss
	writeJSON(w, http.StatusCreated, issueJSON(iss))
}

func (f *fakeGitHub) getIssue(w http.ResponseWriter, r *http.Request, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	iss, ok := f.issues[n]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, issueJSON(iss))
}

func (f *fakeGitHub) editIssue(w http.ResponseWriter, r *http.Request, n int) {
	var req struct {
		Title *string `json:"title"`
		Body  *string `json:"body"`
	}
	decode(r, &req)
	f.mu.Lock()
	defer f.mu.Unlock()
	iss, ok := f.issues[n]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if req.Title != nil {
		iss.Title = *req.Title
	}
	if req.Body != nil {
		iss.Body = *req.Body
	}
	writeJSON(w, http.StatusOK, issueJSON(iss))
}

// setLabels handles both PUT (replace) and POST (add) on the labels endpoint.
// go-github's IssuesService sends the labels as a BARE JSON array
// (["clex:building"]), not an object, so decode into a []string.
func (f *fakeGitHub) setLabels(w http.ResponseWriter, r *http.Request, n int, replace bool) {
	var labels []string
	decode(r, &labels)
	f.mu.Lock()
	defer f.mu.Unlock()
	iss, ok := f.issues[n]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if replace {
		iss.Labels = append([]string(nil), labels...)
	} else {
		iss.Labels = append(iss.Labels, labels...)
	}
	// Respond with the label objects (go-github decodes but clex ignores them).
	out := make([]map[string]any, 0, len(iss.Labels))
	for _, l := range iss.Labels {
		out = append(out, map[string]any{"name": l})
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *fakeGitHub) addComment(w http.ResponseWriter, r *http.Request, n int) {
	var req struct {
		Body string `json:"body"`
	}
	decode(r, &req)
	f.mu.Lock()
	f.comments[n] = append(f.comments[n], req.Body)
	f.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"id": 1, "body": req.Body})
}

// --- pull-request handlers ---

func (f *fakeGitHub) createPR(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Body  string `json:"body"`
	}
	decode(r, &req)
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.nextNum
	f.nextNum++
	pr := &fakePR{Number: n, Title: req.Title, Head: req.Head, Base: req.Base, Body: req.Body, State: "open"}
	f.prs[n] = pr
	// Register a shadow issue entry so issue-level comments on the PR resolve,
	// but mark it as a PR so ListByRepo excludes it.
	f.issues[n] = &fakeIssue{Number: n, Title: req.Title, IsPR: true}
	writeJSON(w, http.StatusCreated, prJSON(pr))
}

func (f *fakeGitHub) getPR(w http.ResponseWriter, r *http.Request, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr, ok := f.prs[n]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, prJSON(pr))
}

func (f *fakeGitHub) addReview(w http.ResponseWriter, r *http.Request, n int) {
	var req struct {
		Event string `json:"event"`
		Body  string `json:"body"`
	}
	decode(r, &req)
	f.mu.Lock()
	f.reviews[n] = append(f.reviews[n], req.Event)
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"id": 1, "state": req.Event})
}

// mergePR performs a REAL git merge of the PR's head branch into its base
// branch in the primary checkout, pushes the result to origin, marks the PR
// merged, and closes the linked issue so the epic-readiness check advances.
func (f *fakeGitHub) mergePR(w http.ResponseWriter, r *http.Request, n int) {
	f.mu.Lock()
	pr, ok := f.prs[n]
	if !ok {
		f.mu.Unlock()
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	head, base := pr.Head, pr.Base
	f.mu.Unlock()

	sha, err := f.gitMerge(head, base)
	if err != nil {
		// Surface a merge failure as a 405 (GitHub's "not mergeable") with detail.
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"merged": false, "message": err.Error(),
		})
		return
	}

	f.mu.Lock()
	pr.Merged = true
	pr.State = "closed"
	if base == "main" {
		f.merges = append(f.merges, n)
	}
	// Close the linked issue (parsed from the head branch clex/<n>-slug) so it
	// leaves the open set — the daemon reads "landed" as "no longer open".
	if m := issueBranchRe.FindStringSubmatch(head); m != nil {
		if issNum, cerr := strconv.Atoi(m[1]); cerr == nil {
			if iss := f.issues[issNum]; iss != nil {
				iss.Closed = true
			}
		}
	}
	f.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"merged": true, "sha": sha, "message": "merged"})
}

// gitMerge merges headBranch into baseBranch. Both branches live locally in the
// primary checkout (the workspace manager creates them there and nothing pushes
// them to origin — the real runner would, but the fake keeps everything local).
// The merge happens in a detached throwaway worktree, then the base branch ref is
// updated to the merge commit. Keeping the temp worktree detached avoids flaky
// "same branch checked out twice" behavior while parallel issue reviews are
// merging into the same epic branch. Merges are serialized on mergeMu.
func (f *fakeGitHub) gitMerge(headBranch, baseBranch string) (string, error) {
	f.mergeMu.Lock()
	defer f.mergeMu.Unlock()

	// A unique temp worktree path under the primary checkout's parent.
	tmp := filepath.Join(f.mergeDir, fmt.Sprintf("mw-%d-%d", os.Getpid(), time.Now().UnixNano()))
	// Add a detached worktree at baseBranch so no branch is checked out twice.
	if out, err := f.gitPrimary("worktree", "add", "--detach", "--force", tmp, baseBranch); err != nil {
		return "", fmt.Errorf("merge: add detached worktree on %s: %v: %s", baseBranch, err, out)
	}
	defer func() {
		_, _ = f.gitPrimary("worktree", "remove", "--force", tmp)
		_, _ = f.gitPrimary("worktree", "prune")
	}()

	if out, err := gitDir(tmp, "-c", "user.email=merge@clex.test", "-c", "user.name=clex-merge",
		"merge", "--no-ff", "-m", fmt.Sprintf("Merge %s into %s", headBranch, baseBranch), headBranch); err != nil {
		return "", fmt.Errorf("merge %s into %s: %v: %s", headBranch, baseBranch, err, out)
	}
	sha, err := gitDir(tmp, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	sha = strings.TrimSpace(sha)
	if out, err := f.gitPrimary("update-ref", "refs/heads/"+baseBranch, sha); err != nil {
		return "", fmt.Errorf("update %s to %s: %v: %s", baseBranch, sha, err, out)
	}
	return sha, nil
}

// gitPrimary runs a git command in the primary checkout (used to add/remove the
// throwaway merge worktree).
func (f *fakeGitHub) gitPrimary(args ...string) (string, error) {
	return gitDir(f.repoDir, args...)
}

// gitDir runs a git command in dir and returns combined output.
func gitDir(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// --- test assertions surface ---

// issueState returns the derived clex pipeline state label of an issue, or "".
func (f *fakeGitHub) issueLabels(n int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if iss, ok := f.issues[n]; ok {
		return append([]string(nil), iss.Labels...)
	}
	return nil
}

// prsToMain returns all PRs (merged or not) whose base is main.
func (f *fakeGitHub) prsToMain() []*fakePR {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*fakePR
	for _, pr := range f.prs {
		if pr.Base == "main" {
			cp := *pr
			out = append(out, &cp)
		}
	}
	return out
}

// commentsOn returns the comment bodies posted on an issue/PR number.
func (f *fakeGitHub) commentsOn(n int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.comments[n]...)
}

// --- JSON helpers ---

func issueJSON(iss *fakeIssue) map[string]any {
	labels := make([]map[string]any, 0, len(iss.Labels))
	for _, l := range iss.Labels {
		labels = append(labels, map[string]any{"name": l})
	}
	state := "open"
	if iss.Closed {
		state = "closed"
	}
	m := map[string]any{
		"number": iss.Number,
		"title":  iss.Title,
		"body":   iss.Body,
		"state":  state,
		"labels": labels,
		"user":   map[string]any{"login": iss.Author},
	}
	if iss.IsPR {
		m["pull_request"] = map[string]any{"url": "http://example/pr"}
	}
	return m
}

func prJSON(pr *fakePR) map[string]any {
	return map[string]any{
		"number":          pr.Number,
		"title":           pr.Title,
		"state":           pr.State,
		"merged":          pr.Merged,
		"mergeable":       true,
		"mergeable_state": "clean",
		"head":            map[string]any{"ref": pr.Head},
		"base":            map[string]any{"ref": pr.Base},
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func decode(r *http.Request, v any) {
	_ = json.NewDecoder(r.Body).Decode(v)
}

func num(re *regexp.Regexp, path string) int {
	m := re.FindStringSubmatch(path)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// waitFor polls cond until it is true or the timeout elapses.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}
