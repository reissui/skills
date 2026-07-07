package scheduler

import "sort"

// topoOrder returns issue numbers in a topological order (dependencies before
// dependents). If a cycle exists it returns a *CycleError naming the members of
// (one) cycle. Only edges among issues present in the input are considered;
// dependencies on unknown issue numbers are ignored for ordering (they are
// handled by eligibility, which requires the dependency to be closed).
func topoOrder(issues []Issue) ([]int, error) {
	// Build adjacency: dep -> dependents, and in-degree per node.
	present := make(map[int]bool, len(issues))
	for _, is := range issues {
		present[is.Number] = true
	}
	deps := make(map[int][]int, len(issues)) // node -> its dependencies (present only)
	for _, is := range issues {
		for _, d := range is.DependsOn {
			if present[d] {
				deps[is.Number] = append(deps[is.Number], d)
			}
		}
	}

	const (
		white = 0 // unvisited
		gray  = 1 // on the current DFS stack
		black = 2 // fully explored
	)
	color := make(map[int]int, len(issues))
	var order []int
	var stack []int // current DFS path, for cycle reporting

	var visit func(n int) []int
	visit = func(n int) []int {
		color[n] = gray
		stack = append(stack, n)
		// Deterministic traversal.
		ds := append([]int(nil), deps[n]...)
		sort.Ints(ds)
		for _, d := range ds {
			switch color[d] {
			case white:
				if cyc := visit(d); cyc != nil {
					return cyc
				}
			case gray:
				// Found a back-edge: extract the cycle from the stack.
				return extractCycle(stack, d)
			}
		}
		color[n] = black
		stack = stack[:len(stack)-1]
		order = append(order, n)
		return nil
	}

	// Visit in ascending issue order for determinism.
	nums := make([]int, 0, len(issues))
	for _, is := range issues {
		nums = append(nums, is.Number)
	}
	sort.Ints(nums)
	for _, n := range nums {
		if color[n] == white {
			if cyc := visit(n); cyc != nil {
				sort.Ints(cyc)
				return nil, &CycleError{Members: cyc}
			}
		}
	}
	return order, nil
}

// extractCycle returns the members of the cycle that closes back to start,
// given the current DFS stack.
func extractCycle(stack []int, start int) []int {
	for i, n := range stack {
		if n == start {
			return append([]int(nil), stack[i:]...)
		}
	}
	// Should not happen: start is guaranteed on the stack when gray.
	return append([]int(nil), start)
}

// depsSatisfied reports whether every dependency of is (that exists in byNum) is
// closed. Dependencies on issues not present are considered satisfied only if
// there is no record of them being open — since the scheduler cannot see them,
// it conservatively treats an unknown dependency as satisfied (the daemon only
// feeds the scheduler issues within the epic; cross-epic deps are out of scope).
func depsSatisfied(is Issue, byNum map[int]Issue) bool {
	for _, d := range is.DependsOn {
		dep, ok := byNum[d]
		if !ok {
			continue // unknown to this epic; not a blocker here
		}
		if !dep.Closed {
			return false
		}
	}
	return true
}
