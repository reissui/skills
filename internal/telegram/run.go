package telegram

import "context"

// starter is the long-poll driver. The production Transport backs it with
// *bot.Bot.Start; tests leave it nil and call processUpdate directly, so the
// authorization and dispatch logic is exercised without a live poll loop.
type starter interface {
	Start(ctx context.Context)
}

// Run begins the long-polling update loop and blocks until ctx is cancelled.
// Every received update is routed through processUpdate, which applies the
// chat-id and per-user authorization filters before dispatch. Handlers and the
// OnImages callback should be registered before calling Run.
//
// Run is a no-op error for a Transport built without a live poller (e.g. one
// constructed with newWithAPI in tests); such Transports are driven by feeding
// updates to processUpdate directly.
func (t *Transport) Run(ctx context.Context) error {
	if t.poller == nil {
		return nil
	}
	t.poller.Start(ctx)
	return nil
}
