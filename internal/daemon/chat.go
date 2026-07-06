package daemon

import (
	"context"
	"fmt"
	"strings"

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
	d.mu.Lock()
	if d.chat.busy {
		d.mu.Unlock()
		d.notify(ctx, "⏳ still answering the previous message")
		return
	}
	d.chat.busy = true
	resume := d.chat.sessionID
	d.mu.Unlock()

	go d.chatTurn(ctx, opt, resume, text)
}

// chatTurn runs one conversational turn against the chat model in the repo
// checkout and relays the answer verbatim (truncated to Telegram's cap).
func (d *Daemon) chatTurn(ctx context.Context, opt registry.RunOption, resume, text string) {
	defer func() {
		d.mu.Lock()
		d.chat.busy = false
		d.mu.Unlock()
	}()

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
	answer, sessionID, err := drainRun(ctx, runner, task, repoDirFor(d.cfg))
	if err != nil {
		d.log.Warn("chat turn", "model", opt.Model.ID, "err", d.red.Redact(err.Error()))
		d.notify(ctx, "chat: "+oneLineOf(d.red.Redact(err.Error())))
		return
	}
	if sessionID != "" {
		d.mu.Lock()
		d.chat.sessionID = sessionID
		d.mu.Unlock()
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		answer = "(no answer)"
	}
	d.notify(ctx, truncateForTelegram(answer))
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
// resets the conversation (a CLI session belongs to one provider/model). It
// returns a human-readable result line.
func (d *Daemon) setChatModel(id string) string {
	id = strings.TrimSpace(id)
	opt, ok := d.findModelOption(id)
	if !ok {
		return fmt.Sprintf("unknown model %q — /models lists what is available", id)
	}
	d.mu.Lock()
	d.chat.override = &opt
	d.chat.sessionID = ""
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

// truncateForTelegram caps a reply under Telegram's message limit.
func truncateForTelegram(s string) string {
	if len(s) <= telegramMessageLimit {
		return s
	}
	return s[:telegramMessageLimit-1] + "…"
}
