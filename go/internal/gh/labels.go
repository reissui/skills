package gh

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-github/v66/github"
	"github.com/reissui/clex/internal/core"
)

// AgentLabelPrefix is the prefix for routing tags that record which runner owns
// or built an issue, e.g. "clex:agent/codex" (spec: Source of truth: GitHub).
const AgentLabelPrefix = "clex:agent/"

// LabelSpec describes a single clex label: its name, hex color (no leading '#'),
// and description. EnsureLabels reconciles the repo against this set.
type LabelSpec struct {
	Name        string
	Color       string
	Description string
}

// AgentLabel returns the routing-tag label name for a runner, e.g.
// AgentLabel("codex") == "clex:agent/codex".
func AgentLabel(name string) string { return AgentLabelPrefix + name }

// pipelineStateColors assigns each pipeline state a distinct hex color so the
// board is legible on GitHub. Colors are cosmetic; EnsureLabels updates them if
// they drift.
var pipelineStateColors = map[core.State]string{
	core.StateIdea:        "ededed",
	core.StateResearching: "fbca04",
	core.StatePlanned:     "0e8a16",
	core.StateApproved:    "1d76db",
	core.StateBuilding:    "5319e7",
	core.StateReview:      "b60205",
	core.StateFailed:      "e11d21",
}

// pipelineStateDescriptions documents each state on the label itself.
var pipelineStateDescriptions = map[core.State]string{
	core.StateIdea:        "clex: feature idea filed, not yet researched",
	core.StateResearching: "clex: researching / planning in progress",
	core.StatePlanned:     "clex: planned, awaiting approval at the plan gate",
	core.StateApproved:    "clex: approved to build (eligible for dispatch)",
	core.StateBuilding:    "clex: a runner is building this issue",
	core.StateReview:      "clex: built, under model review before merge",
	core.StateFailed:      "clex: a stage failed; see the failure comment",
}

// clexLabelSet returns the full set of labels clex manages: every pipeline
// state, the epic marker, plus any additional agent tags requested. Agent tags
// are supplied by the caller because runner names come from config.
func clexLabelSet(agents []string) []LabelSpec {
	specs := make([]LabelSpec, 0, len(pipelineStateColors)+1+len(agents))
	// Pipeline states, in canonical order.
	order := []core.State{
		core.StateIdea, core.StateResearching, core.StatePlanned,
		core.StateApproved, core.StateBuilding, core.StateReview, core.StateFailed,
	}
	for _, s := range order {
		specs = append(specs, LabelSpec{
			Name:        string(s),
			Color:       pipelineStateColors[s],
			Description: pipelineStateDescriptions[s],
		})
	}
	// Epic marker.
	specs = append(specs, LabelSpec{
		Name:        string(core.StateEpic),
		Color:       "c5def5",
		Description: "clex: PRD epic issue (carries no pipeline lifecycle)",
	})
	// Agent routing tags.
	for _, a := range agents {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		specs = append(specs, LabelSpec{
			Name:        AgentLabel(a),
			Color:       "d4c5f9",
			Description: fmt.Sprintf("clex: issue owned/built by runner %q", a),
		})
	}
	return specs
}

// EnsureLabels idempotently creates (or updates) the clex label set on repo.
// Existing labels with matching name are left alone unless their color or
// description drifted, in which case they are edited. Running it twice is a
// no-op on the second run — the acceptance criterion for issue #6.
//
// agents lists runner names to materialize as clex:agent/<name> tags; pass nil
// to manage only the pipeline states and the epic marker.
func (c *Client) EnsureLabels(ctx context.Context, repo Repo, agents []string) error {
	existing, err := c.listAllLabels(ctx, repo)
	if err != nil {
		return fmt.Errorf("list labels for %s: %w", repo, err)
	}
	for _, spec := range clexLabelSet(agents) {
		cur, ok := existing[strings.ToLower(spec.Name)]
		if !ok {
			// Create it.
			_, _, err := c.gh.Issues.CreateLabel(ctx, repo.Owner, repo.Name, &github.Label{
				Name:        github.String(spec.Name),
				Color:       github.String(spec.Color),
				Description: github.String(spec.Description),
			})
			if err != nil {
				return fmt.Errorf("create label %q: %w", spec.Name, err)
			}
			continue
		}
		// Exists: only edit if color or description drifted, so a second run is
		// a true no-op (no writes).
		if !labelMatches(cur, spec) {
			_, _, err := c.gh.Issues.EditLabel(ctx, repo.Owner, repo.Name, spec.Name, &github.Label{
				Name:        github.String(spec.Name),
				Color:       github.String(spec.Color),
				Description: github.String(spec.Description),
			})
			if err != nil {
				return fmt.Errorf("edit label %q: %w", spec.Name, err)
			}
		}
	}
	return nil
}

// labelMatches reports whether an existing label already equals the desired
// spec (case-insensitive color, exact description).
func labelMatches(cur *github.Label, spec LabelSpec) bool {
	return strings.EqualFold(cur.GetColor(), spec.Color) &&
		cur.GetDescription() == spec.Description
}

// listAllLabels returns every label on repo keyed by lower-cased name, paging
// through all results.
func (c *Client) listAllLabels(ctx context.Context, repo Repo) (map[string]*github.Label, error) {
	out := map[string]*github.Label{}
	opts := &github.ListOptions{PerPage: 100}
	for {
		labels, resp, err := c.gh.Issues.ListLabels(ctx, repo.Owner, repo.Name, opts)
		if err != nil {
			return nil, err
		}
		for _, l := range labels {
			out[strings.ToLower(l.GetName())] = l
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}
