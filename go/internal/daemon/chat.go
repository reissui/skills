package daemon

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/registry"
)

// chatPreamble opens the very first turn of a chat session. Later turns resume
// the same CLI session, so the model keeps its own history — this is sent once
// per session, keeping every subsequent turn cheap.
const chatPreamble = "You are clex, a development copilot the owner talks to over Telegram. " +
	"You are running in a checkout of the repository you manage; read code and files as needed to answer. " +
	"Answer conversationally and concisely — this is a phone chat, so prefer a few short paragraphs over walls of text, and no markdown tables. " +
	"You cannot edit code in this session. When the owner wants something built, suggest they send /plan <idea> " +
	"(clex turns it into a PRD epic with agent-ready sub-issues) and then /build <epic#> to execute it.\n\n" +
	"Owner: "

// telegramMessageLimit is Telegram's hard per-message cap; replies are truncated
// just under it rather than erroring.
const telegramMessageLimit = 4096

// chatState is the daemon's single Telegram chat session: the model it runs on,
// the CLI session id that carries the conversation history, and a busy flag so
// turns never interleave on one session.
type chatState struct {
	// override, when set by /model, wins over the bot-role routing.
	override *registry.RunOption
	// sessionID chains turns into one conversation (Resume, don't restart).
	sessionID string
	busy      bool
	// gen increments whenever the conversation is reset (/model). A turn that
	// started under an older generation discards its session id instead of
	// resurrecting the pre-reset conversation.
	gen int
}

// claimChat atomically claims the single chat slot, returning the session to
// resume and the current generation. ok is false when a turn is already
// running — chat turns and /plan distillation share one slot because they
// share one CLI session.
func (d *Daemon) claimChat() (resume string, gen int, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.chat.busy {
		return "", 0, false
	}
	d.chat.busy = true
	return d.chat.sessionID, d.chat.gen, true
}

// releaseChat releases the chat slot and records the turn's terminal session id
// — unless the conversation was reset (generation moved) while the turn ran, in
// which case the stale session is discarded.
func (d *Daemon) releaseChat(gen int, sessionID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.chat.busy = false
	if sessionID != "" && gen == d.chat.gen {
		d.chat.sessionID = sessionID
	}
}

// registerChat wires free text to chat turns. runCtx is the daemon's Run
// context: turns are bounded by daemon lifetime, not by the transport's
// per-update context.
func (d *Daemon) registerChat(runCtx context.Context) {
	d.deps.TG.OnText(func(_ context.Context, text string, _ int) {
		d.onChatText(runCtx, text)
	})
}

// onChatText starts one chat turn, or reports busy if one is already running.
// The turn itself runs in its own goroutine — chat must never block the
// transport's update handling or the loop.
func (d *Daemon) onChatText(ctx context.Context, text string) {
	opt, err := d.chatOption()
	if err != nil {
		d.notify(ctx, "chat: "+err.Error())
		return
	}
	resume, gen, ok := d.claimChat()
	if !ok {
		d.notify(ctx, "⏳ still answering the previous message")
		return
	}
	go d.chatTurn(ctx, opt, resume, gen, text)
}

// chatTurn runs one conversational turn against the chat model in the repo
// checkout and relays the answer verbatim (redacted, then truncated to
// Telegram's cap). The caller must have claimed the chat slot.
func (d *Daemon) chatTurn(ctx context.Context, opt registry.RunOption, resume string, gen int, text string) {
	sessionID := ""
	defer func() { d.releaseChat(gen, sessionID) }()

	prompt := text
	if resume == "" {
		prompt = chatPreamble + text
	}
	runner, err := d.deps.RunnerFactory.RunnerFor(opt.Model)
	if err != nil {
		d.notify(ctx, "chat: no runner for "+opt.Model.ID)
		return
	}
	task := core.Task{
		Repo:     d.cfg.Repo.String(),
		Prompt:   prompt,
		Effort:   opt.Effort,
		Fast:     opt.Fast,
		ResumeID: resume,
	}
	answer, sid, err := drainRun(ctx, runner, task, repoDirFor(d.cfg))
	sessionID = sid
	if err != nil {
		d.log.Warn("chat turn", "model", opt.Model.ID, "err", d.red.Redact(err.Error()))
		d.notify(ctx, "chat: "+oneLineOf(d.red.Redact(err.Error())))
		return
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		answer = "(no answer)"
	}
	// Redact BEFORE truncating: truncating first could split a secret across
	// the cut and defeat the redactor's pattern match. notify re-redacts,
	// which is an idempotent no-op.
	d.notify(ctx, truncateForTelegram(d.red.Redact(answer)))
}

// chatOption resolves the model a chat turn runs on: the /model override when
// set, else the first bot-role option from the registry ([routing.bot]).
func (d *Daemon) chatOption() (registry.RunOption, error) {
	d.mu.Lock()
	override := d.chat.override
	d.mu.Unlock()
	if override != nil {
		return *override, nil
	}
	opts, _ := d.deps.Registry.Available(core.RoleBot)
	if len(opts) == 0 {
		return registry.RunOption{}, fmt.Errorf("no chat model available — configure [routing.bot]")
	}
	return opts[0], nil
}

// setChatModel re-points chat at a declared model id for this daemon run and
// resets the conversation (a CLI session belongs to one provider/model). The
// generation bump makes any in-flight turn discard its session id instead of
// resurrecting the old conversation. Returns a human-readable result line.
func (d *Daemon) setChatModel(id string) string {
	id = strings.TrimSpace(id)
	opt, ok := d.findModelOption(id)
	if !ok {
		return fmt.Sprintf("unknown model %q — /models lists what is available", id)
	}
	d.mu.Lock()
	d.chat.override = &opt
	d.chat.sessionID = ""
	d.chat.gen++
	d.mu.Unlock()
	return fmt.Sprintf("chat model set to %s (new conversation)", opt.Model.ID)
}

// chatModelLine renders the current chat model and the available options.
func (d *Daemon) chatModelLine() string {
	opt, err := d.chatOption()
	if err != nil {
		return "chat: " + err.Error()
	}
	var ids []string
	seen := map[string]bool{}
	for _, role := range []core.Role{core.RoleBot, core.RolePlan, core.RoleBuild, core.RoleReview, core.RoleLint} {
		opts, _ := d.deps.Registry.Available(role)
		for _, o := range opts {
			if !seen[o.Model.ID] {
				seen[o.Model.ID] = true
				ids = append(ids, o.Model.ID)
			}
		}
	}
	return fmt.Sprintf("chat model: %s — /model <id> to switch (%s)", opt.Model.ID, strings.Join(ids, ", "))
}

// findModelOption looks a model id up across every role's resolved options, so
// /model can pick any declared, healthy model — not just bot-role ones. The
// bot role is searched first so its effort/fast resolution wins when present.
func (d *Daemon) findModelOption(id string) (registry.RunOption, bool) {
	for _, role := range []core.Role{core.RoleBot, core.RolePlan, core.RoleBuild, core.RoleReview, core.RoleLint} {
		opts, _ := d.deps.Registry.Available(role)
		for _, o := range opts {
			if o.Model.ID == id {
				return o, true
			}
		}
	}
	return registry.RunOption{}, false
}

// drainRun executes one runner task to completion and returns the accumulated
// assistant text and terminal session id. It mirrors the pipeline's own event
// draining (that helper is unexported and pipeline-scoped).
func drainRun(ctx context.Context, r core.Runner, task core.Task, dir string) (text, sessionID string, err error) {
	ch, err := r.Run(ctx, task, dir)
	if err != nil {
		return "", "", err
	}
	var out strings.Builder
	var runErr string
	for ev := range ch {
		switch ev.Type {
		case core.EventText:
			out.WriteString(ev.Text)
		case core.EventResult:
			sessionID = ev.SessionID
			if ev.Err != "" {
				runErr = ev.Err
			}
		case core.EventError:
			runErr = ev.Err
		}
	}
	if runErr != "" {
		return out.String(), sessionID, fmt.Errorf("%s", runErr)
	}
	return out.String(), sessionID, nil
}

// truncateForTelegram caps a reply under Telegram's message limit, cutting on a
// rune boundary so the result stays valid UTF-8 (a mid-rune cut makes Telegram
// reject the whole send). Telegram's limit is 4096 UTF-16 code units; capping
// bytes is strictly conservative since UTF-8 never uses fewer bytes than the
// UTF-16 unit count.
func truncateForTelegram(s string) string {
	if len(s) <= telegramMessageLimit {
		return s
	}
	return cutAtRune(s, telegramMessageLimit-len("…")) + "…"
}

// cutAtRune returns s truncated to at most max bytes without splitting a rune.
func cutAtRune(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}
