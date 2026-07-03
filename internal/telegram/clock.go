package telegram

import "time"

// clock abstracts the passage of time so Ask's alter-reply timeout can be tested
// deterministically without real sleeps.
type clock interface {
	// After returns a channel that receives once d has elapsed.
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
