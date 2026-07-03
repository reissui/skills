package daemon

import (
	"context"

	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/pipeline"
)

// resolveEpic returns the epic issue number an issue belongs to. clex child
// issues link to their epic via a dependency on the epic issue; here we resolve
// it by finding, among the issue's DependsOn numbers, the one that is itself an
// epic (carries clex:epic). It returns 0 for a standalone issue with no epic
// parent, which Build/Review tolerate by operating on the issue branch alone.
//
// Resolution consults the live issue list so it works whether or not the epic is
// still open. It is best-effort: on a lookup error the dependency is skipped.
func (d *Daemon) resolveEpic(ctx context.Context, iss *gh.Issue) int {
	for _, dep := range iss.Meta.DependsOn {
		parent, err := d.deps.GH.GetIssue(ctx, d.cfg.Repo, dep)
		if err != nil {
			continue
		}
		if parent.IsEpic {
			return dep
		}
	}
	return 0
}

// epicChildren returns the child issue numbers of an epic (from the open set)
// and whether they have ALL landed. A child is an open issue that depends on the
// epic; the epic is ready to assemble only when it has children on record AND
// none of them remain open (every one has merged, leaving the open set).
//
// Because merged children are absent from the open list, the caller supplies the
// full set of children discovered earlier via the epic's own child roster; here
// we take the currently-open issues plus the known child roster so readiness is
// "roster non-empty AND no roster member still open".
func (d *Daemon) epicChildren(open []*gh.Issue, roster []int, epicNum int) (children []int, allLanded bool) {
	openByNum := make(map[int]bool, len(open))
	for _, iss := range open {
		openByNum[iss.Number] = true
	}
	// Merge the roster with any open children currently referencing the epic, so
	// callers that pass a nil roster still get the open children.
	set := make(map[int]bool)
	for _, n := range roster {
		set[n] = true
	}
	for _, iss := range open {
		if !iss.IsEpic && dependsOn(iss, epicNum) {
			set[iss.Number] = true
		}
	}
	stillOpen := 0
	for n := range set {
		children = append(children, n)
		if openByNum[n] {
			stillOpen++
		}
	}
	allLanded = len(children) > 0 && stillOpen == 0
	return children, allLanded
}

// dependsOn reports whether iss lists epicNum among its DependsOn numbers.
func dependsOn(iss *gh.Issue, epicNum int) bool {
	for _, dep := range iss.Meta.DependsOn {
		if dep == epicNum {
			return true
		}
	}
	return false
}

// knowledgeFor assembles the trimmed knowledge-file context for a build. On an
// escalation/retry the prior failed diff is threaded into the Log excerpt so the
// resumed runner sees "here is what the previous attempt produced" rather than
// starting cold (spec: escalation re-dispatch carries the failed diff + notes
// forward).
func (d *Daemon) knowledgeFor(_ *gh.Issue, carryDiff string) pipeline.KnowledgeExcerpts {
	k := pipeline.KnowledgeExcerpts{}
	if carryDiff != "" {
		k.Log = "Previous attempt diff (carried forward for escalation):\n" + carryDiff
	}
	return k
}
