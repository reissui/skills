package registry

import (
	"time"

	"github.com/reissui/clex/internal/core"
)

// History is the read-only slice of the runtime store that the router and cost
// gates depend on. The registry consumes it abstractly so that *store.Store
// satisfies it in production while tests inject a deterministic fake (spec:
// Routing — build routing weighs success, speed, and cost; SQLite runtime).
//
// *store.Store implements this interface directly (its SuccessRate, AvgDuration
// and SpendSince method set is a superset of History). Keeping the dependency
// narrow keeps the registry testable without opening a database.
type History interface {
	// SuccessRate reports the fraction (0..1) of past stages a model completed
	// successfully at a given difficulty. Implementations return 0 with no error
	// when there is no history (cold start).
	SuccessRate(model string, difficulty core.Difficulty) (float64, error)
	// AvgDuration reports the mean wall-clock time a model took on a stage.
	// Implementations return 0 with no error when there is no history.
	AvgDuration(model, stage string) (time.Duration, error)
	// SpendSince reports total USD spent on a model since t. A zero model means
	// "all models" (epic-wide spend).
	SpendSince(t time.Time, model string) (float64, error)
}

// staticHistory is a no-op History used when the registry is constructed without
// a store (e.g. a config-only doctor check). Every query returns the zero value,
// which drives the router and estimator onto their documented cold-start paths.
type staticHistory struct{}

func (staticHistory) SuccessRate(string, core.Difficulty) (float64, error) { return 0, nil }
func (staticHistory) AvgDuration(string, string) (time.Duration, error)    { return 0, nil }
func (staticHistory) SpendSince(time.Time, string) (float64, error)        { return 0, nil }
