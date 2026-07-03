package core

// State is a pipeline state, materialized on GitHub as an issue label. GitHub is
// the source of truth; the daemon enforces valid transitions and re-reads any
// unknown/hand-edited state rather than assuming it (spec: Source of truth:
// GitHub, Error handling & safety).
type State string

const (
	StateIdea        State = "clex:idea"
	StateResearching State = "clex:researching"
	StatePlanned     State = "clex:planned"
	StateApproved    State = "clex:approved"
	StateBuilding    State = "clex:building"
	StateReview      State = "clex:review"
	StateFailed      State = "clex:failed"

	// StateEpic marks a PRD epic issue. It is a marker, not a pipeline state:
	// epics carry no build/review lifecycle and participate in no transitions.
	StateEpic State = "clex:epic"
)

// pipelineStates is the set of labels that are real pipeline states (excludes
// the clex:epic marker).
var pipelineStates = map[State]bool{
	StateIdea:        true,
	StateResearching: true,
	StatePlanned:     true,
	StateApproved:    true,
	StateBuilding:    true,
	StateReview:      true,
	StateFailed:      true,
}

// IsPipelineState reports whether s is a recognized pipeline state (not the epic
// marker, not an unknown/hand-edited label).
func IsPipelineState(s State) bool {
	return pipelineStates[s]
}

// transitions is the adjacency set of legal from→to pipeline moves.
//
// Happy path (spec: Labels state machine):
//
//	idea → researching → planned → approved → building → review → (merged/closed)
//
// Plus recovery edges required by spec: Error handling & safety and the Telegram
// stop/steer semantics:
//
//   - A running stage can fail: building/review → failed.
//   - A runner failure/timeout, or a stop, reverts the issue to approved so it
//     can be retried or re-dispatched: building/review → approved, failed →
//     approved.
//   - Re-planning after a scope-changing steer sends a planned/approved issue
//     back to researching (spec: Steering).
//
// The terminal step "review → closed via merged PR" is modeled by GitHub issue
// closure, not a label, so there is no outgoing edge from review here other
// than the failure/revert edges.
var transitions = map[State]map[State]bool{
	StateIdea: {
		StateResearching: true,
	},
	StateResearching: {
		StatePlanned: true,
		StateFailed:  true,
	},
	StatePlanned: {
		StateApproved:    true,
		StateResearching: true, // re-plan after a scope-changing steer
	},
	StateApproved: {
		StateBuilding:    true,
		StateResearching: true, // re-plan after a scope-changing steer
	},
	StateBuilding: {
		StateReview:   true,
		StateApproved: true, // failure/timeout/stop reverts to approved
		StateFailed:   true,
	},
	StateReview: {
		StateApproved: true, // review-driven rework / stop reverts to approved
		StateFailed:   true,
	},
	StateFailed: {
		StateApproved: true, // retry re-dispatches from approved
	},
}

// CanTransition reports whether moving an issue from state from to state to is a
// legal pipeline transition. Unknown states, the epic marker, and identity
// moves (from == to) are never legal.
func CanTransition(from, to State) bool {
	if from == to {
		return false
	}
	dsts, ok := transitions[from]
	if !ok {
		return false
	}
	return dsts[to]
}
