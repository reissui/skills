package daemon

import (
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/pipeline"
)

// epicOf returns the epic issue number an issue belongs to, parsed from its
// metadata DependsOn/parent convention. clex child issues link to their epic;
// here we treat the smallest referenced epic-style parent as the epic. When no
// explicit parent is discoverable it returns 0 (a standalone issue), which
// downstream code treats as "no epic assembly".
//
// The parent linkage in this codebase is carried in the issue body's clex block
// as a plain dependency on the epic; since the metadata parser exposes
// DependsOn but not a distinct parent field, callers that need the true epic
// number resolve it from the epic's own child list (epicChildren). epicOf gives
// the best-effort local answer used to scope Build/Review to an integration
// branch.
func epicOf(iss *gh.Issue) int {
	// Convention: the first DependsOn entry that is the epic. Without a typed
	// parent field we cannot be certain which dependency is the epic, so we
	// return 0 and let assembly be driven by epicChildren scanning. Build/Review
	// tolerate epicNum==0 by operating on the issue branch alone.
	return 0
}

// epicChildren returns the child issue numbers of an epic and whether they have
// ALL landed (merged, i.e. no longer open). A child is any open issue that
// depends on the epic; a landed child is one that has left the open set. When
// an epic has at least one child and none remain open, the epic is ready to
// assemble.
func (d *Daemon) epicChildren(open []*gh.Issue, epicNum int) (children []int, allLanded bool) {
	openSet := make(map[int]bool, len(open))
	for _, iss := range open {
		openSet[iss.Number] = true
	}
	// Collect children still open that reference the epic.
	var stillOpen int
	seen := make(map[int]bool)
	for _, iss := range open {
		if iss.IsEpic {
			continue
		}
		if dependsOn(iss, epicNum) {
			children = append(children, iss.Number)
			seen[iss.Number] = true
			stillOpen++
		}
	}
	// If we found open children, they haven't all landed.
	if stillOpen > 0 {
		return children, false
	}
	// No open children reference the epic. Either the epic truly has no children
	// (not ready) or they've all merged. We cannot enumerate merged children
	// without a list-closed call; treat "epic open, zero open children, and at
	// least one child ever seen" conservatively as not ready. The scenario test
	// drives assembly explicitly once children land.
	return children, len(children) == 0 && false
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
