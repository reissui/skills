package daemon

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/ipc"
)

// Handle implements ipc.Handler: it maps a control Request from the clex CLI to
// the daemon's serialized control actions and status reads. It is the daemon
// side of the #17 coupling point. All state-changing commands go through the
// loop via submitControl so they are ordered against dispatch decisions.
func (d *Daemon) Handle(ctx context.Context, req ipc.Request) (ipc.Response, error) {
	switch req.Command {
	case ipc.CmdStatus:
		return ipc.Response{OK: true, Status: d.statusSnapshot()}, nil
	case ipc.CmdPause:
		msg := d.submitControl(ctx, controlAction{kind: ctlPause, reply: make(chan string, 1)})
		return ipc.Response{OK: true, Message: msg}, nil
	case ipc.CmdResume:
		msg := d.submitControl(ctx, controlAction{kind: ctlResume, reply: make(chan string, 1)})
		return ipc.Response{OK: true, Message: msg}, nil
	case ipc.CmdStop:
		if req.Issue == 0 {
			return ipc.Response{OK: false, Error: "stop requires an issue number"}, nil
		}
		msg := d.submitControl(ctx, controlAction{kind: ctlStop, issue: req.Issue, reply: make(chan string, 1)})
		return ipc.Response{OK: true, Message: msg}, nil
	case ipc.CmdSteer:
		msg := d.submitControl(ctx, controlAction{kind: ctlSteer, issue: req.Issue, text: req.Text, reply: make(chan string, 1)})
		return ipc.Response{OK: true, Message: msg}, nil
	case ipc.CmdModels:
		return ipc.Response{OK: true, Models: d.modelsSnapshot()}, nil
	case ipc.CmdCosts:
		return ipc.Response{OK: true, Costs: d.costsSnapshot()}, nil
	default:
		return ipc.Response{OK: false, Error: fmt.Sprintf("unknown command %q", req.Command)}, nil
	}
}

// statusSnapshot builds an ipc.Status from current daemon state (lock-guarded).
func (d *Daemon) statusSnapshot() *ipc.Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := &ipc.Status{
		Version:     versionString(),
		Paused:      d.paused,
		Repo:        d.cfg.Repo.String(),
		PendingGate: d.pendingGate,
	}
	if !d.startedAt.IsZero() {
		st.Uptime = time.Since(d.startedAt).Round(time.Second).String()
	}
	for _, rs := range d.running {
		st.Running = append(st.Running, ipc.RunningIssue{
			Issue:    rs.issue,
			Provider: rs.provider,
			Model:    rs.model.ID,
			Stage:    rs.stage,
		})
	}
	return st
}

// modelsSnapshot reports registry health for each role's resolved models.
func (d *Daemon) modelsSnapshot() []ipc.ModelHealth {
	var out []ipc.ModelHealth
	seen := make(map[string]bool)
	for _, role := range []core.Role{core.RolePlan, core.RoleBuild, core.RoleReview, core.RoleLint, core.RoleBot} {
		opts, _ := d.deps.Registry.Available(role)
		for _, o := range opts {
			if seen[o.Model.ID] {
				continue
			}
			seen[o.Model.ID] = true
			out = append(out, ipc.ModelHealth{
				Model:    o.Model.ID,
				Provider: o.Model.Provider,
				Healthy:  true,
				Detail:   o.Tier,
			})
		}
	}
	return out
}

// costsSnapshot reports metered spend. It is a best-effort read from the store;
// on error it returns an empty summary rather than failing the command.
func (d *Daemon) costsSnapshot() *ipc.Costs {
	c := &ipc.Costs{}
	since := time.Now().Add(-24 * time.Hour)
	if v, err := d.deps.Store.SpendSince(d.epicStart(), ""); err == nil {
		c.SpentThisEpicUSD = v
	}
	if v, err := d.deps.Store.SpendSince(since, ""); err == nil {
		c.SpentTodayUSD = v
	}
	return c
}

// registerCommands wires Telegram slash commands to the same serialized control
// path used by IPC (spec: Telegram — /status /stop /steer /pause /resume /models
// /costs). Every handler funnels through submitControl or a snapshot read, so
// Telegram and the CLI cannot race the loop.
func (d *Daemon) registerCommands(ctx context.Context) {
	d.deps.TG.Handle("pause", func(hctx context.Context, _ string) {
		d.submitControl(hctx, controlAction{kind: ctlPause})
	})
	d.deps.TG.Handle("resume", func(hctx context.Context, _ string) {
		d.submitControl(hctx, controlAction{kind: ctlResume})
	})
	d.deps.TG.Handle("stop", func(hctx context.Context, args string) {
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(args), "#")))
		if err != nil {
			d.notify(hctx, "usage: /stop <issue>")
			return
		}
		d.submitControl(hctx, controlAction{kind: ctlStop, issue: n})
	})
	d.deps.TG.Handle("steer", func(hctx context.Context, args string) {
		issue, text := parseSteerArgs(args)
		d.submitControl(hctx, controlAction{kind: ctlSteer, issue: issue, text: text})
	})
	d.deps.TG.Handle("status", func(hctx context.Context, _ string) {
		d.notify(hctx, renderStatus(d.statusSnapshot()))
	})
	d.deps.TG.Handle("models", func(hctx context.Context, _ string) {
		d.notify(hctx, renderModels(d.modelsSnapshot()))
	})
	d.deps.TG.Handle("costs", func(hctx context.Context, _ string) {
		c := d.costsSnapshot()
		d.notify(hctx, fmt.Sprintf("costs: epic $%.2f, today $%.2f", c.SpentThisEpicUSD, c.SpentTodayUSD))
	})
	d.deps.TG.Handle("plan", func(hctx context.Context, args string) {
		d.notify(hctx, d.planCommand(hctx, args))
		d.enqueue(loopEvent{kind: evTick})
	})
	d.deps.TG.Handle("build", func(hctx context.Context, args string) {
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(args), "#")))
		if err != nil {
			d.notify(hctx, "usage: /build <epic or issue number>")
			return
		}
		d.notify(hctx, d.buildCommand(hctx, n))
		d.enqueue(loopEvent{kind: evTick})
	})
	d.deps.TG.Handle("merge", func(hctx context.Context, args string) {
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(args), "#")))
		if err != nil {
			d.notify(hctx, "usage: /merge <pr number>")
			return
		}
		sha, merr := d.deps.GH.MergePR(hctx, d.cfg.Repo, n, "merge", "")
		if merr != nil {
			d.notify(hctx, fmt.Sprintf("✗ merge PR #%d: %s", n, oneLineOf(d.red.Redact(merr.Error()))))
			return
		}
		d.logEvent(hctx, 0, "merge", fmt.Sprintf("PR #%d merged (%s) via /merge", n, shortSHA(sha)))
		d.notify(hctx, fmt.Sprintf("✅ PR #%d merged (%s)", n, shortSHA(sha)))
	})
	d.deps.TG.Handle("model", func(hctx context.Context, args string) {
		if strings.TrimSpace(args) == "" {
			d.notify(hctx, d.chatModelLine())
			return
		}
		d.notify(hctx, d.setChatModel(args))
	})
}

// planCommand implements /plan: file an idea issue for the daemon to plan.
// With text, the text is the idea (first line becomes the title). Bare, it
// distills the current chat conversation into an idea brief — chat first, then
// /plan, is the natural handoff.
func (d *Daemon) planCommand(ctx context.Context, args string) string {
	text := strings.TrimSpace(args)
	if text == "" {
		distilled, err := d.distillIdea(ctx)
		if err != nil {
			return "usage: /plan <what you want built> — or chat about it first, then a bare /plan plans the conversation (" + err.Error() + ")"
		}
		text = distilled
	}
	title, body := splitIdeaText(text)
	iss, err := d.deps.GH.CreateIssue(ctx, d.cfg.Repo, title, body, []string{string(core.StateIdea)})
	if err != nil {
		return "✗ file idea: " + oneLineOf(d.red.Redact(err.Error()))
	}
	d.logEvent(ctx, iss.Number, "idea", "filed via /plan")
	return fmt.Sprintf("💡 idea #%d filed: %s — planning starts now", iss.Number, title)
}

// buildCommand implements /build: pass the plan gate. For an epic it approves
// every planned child (the scheduler then dispatches them in dependency order,
// parallel where Touches allow); for a single issue it approves just that one.
func (d *Daemon) buildCommand(ctx context.Context, n int) string {
	iss, err := d.deps.GH.GetIssue(ctx, d.cfg.Repo, n)
	if err != nil {
		return fmt.Sprintf("✗ #%d not found: %s", n, oneLineOf(d.red.Redact(err.Error())))
	}
	if !iss.IsEpic {
		if iss.State != core.StatePlanned {
			return fmt.Sprintf("#%d is %s — /build approves planned issues", n, stateOrNone(iss.State))
		}
		if err := d.deps.GH.SetState(ctx, d.cfg.Repo, n, core.StateApproved); err != nil {
			return fmt.Sprintf("✗ approve #%d: %s", n, oneLineOf(d.red.Redact(err.Error())))
		}
		d.logEvent(ctx, n, "gate", "approved via /build")
		return fmt.Sprintf("✅ #%d approved — building starts now", n)
	}

	issues, err := d.deps.GH.ListIssues(ctx, d.cfg.Repo)
	if err != nil {
		return "✗ list issues: " + oneLineOf(d.red.Redact(err.Error()))
	}
	var approved, already int
	for _, child := range issues {
		if child.IsEpic || !dependsOn(child, n) {
			continue
		}
		switch child.State {
		case core.StatePlanned:
			if err := d.deps.GH.SetState(ctx, d.cfg.Repo, child.Number, core.StateApproved); err != nil {
				d.log.Warn("approve child", "issue", child.Number, "err", d.red.Redact(err.Error()))
				continue
			}
			approved++
		case core.StateApproved, core.StateBuilding, core.StateReview:
			already++
		}
	}
	if approved == 0 && already == 0 {
		return fmt.Sprintf("epic #%d has no planned children — /plan an idea first", n)
	}
	d.logEvent(ctx, n, "gate", fmt.Sprintf("epic approved via /build: %d children approved, %d already in flight", approved, already))
	if already > 0 {
		return fmt.Sprintf("✅ epic #%d: %d issues approved (%d already in flight) — building starts now", n, approved, already)
	}
	return fmt.Sprintf("✅ epic #%d: %d issues approved — building starts now", n, approved)
}

// distillIdea asks the current chat session to compress the conversation into
// an idea brief. It requires an existing session — with nothing discussed there
// is nothing to plan. It claims the chat slot for the duration so a concurrent
// chat turn can never race it onto the same CLI session.
func (d *Daemon) distillIdea(ctx context.Context) (string, error) {
	opt, err := d.chatOption()
	if err != nil {
		return "", err
	}
	resume, gen, ok := d.claimChat()
	if !ok {
		return "", fmt.Errorf("chat is busy")
	}
	sessionID := ""
	defer func() { d.releaseChat(gen, sessionID) }()
	if resume == "" {
		return "", fmt.Errorf("no chat to plan yet")
	}
	runner, err := d.deps.RunnerFactory.RunnerFor(opt.Model)
	if err != nil {
		return "", fmt.Errorf("no runner for %s", opt.Model.ID)
	}
	task := core.Task{
		Repo: d.cfg.Repo.String(),
		Prompt: "Distill our conversation so far into one buildable idea brief. " +
			"First line: a concise title. Then a blank line, then the brief itself: " +
			"goal, relevant context and constraints from the conversation, and what done looks like. " +
			"Output only the brief — no preamble.",
		Effort:   opt.Effort,
		Fast:     opt.Fast,
		ResumeID: resume,
	}
	text, sid, err := drainRun(ctx, runner, task, repoDirFor(d.cfg))
	sessionID = sid
	if err != nil {
		return "", fmt.Errorf("distill failed: %s", oneLineOf(d.red.Redact(err.Error())))
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("distill produced nothing")
	}
	return strings.TrimSpace(text), nil
}

// splitIdeaText splits idea text into a title (first line, trimmed, cut on a
// rune boundary) and the full text as body.
func splitIdeaText(text string) (title, body string) {
	title = strings.TrimSpace(text)
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = strings.TrimSpace(title[:i])
	}
	const maxTitle = 120
	if len(title) > maxTitle {
		title = cutAtRune(title, maxTitle-len("…")) + "…"
	}
	return title, text
}

// stateOrNone renders a state label for humans, with a fallback for issues
// carrying no pipeline label.
func stateOrNone(s core.State) string {
	if s == "" {
		return "unlabelled"
	}
	return string(s)
}

// parseSteerArgs splits "/steer <#issue> <text>" into issue and text. A leading
// token that parses as an issue number targets that issue; otherwise the whole
// argument steers the epic (issue 0).
func parseSteerArgs(args string) (int, string) {
	args = strings.TrimSpace(args)
	if args == "" {
		return 0, ""
	}
	fields := strings.SplitN(args, " ", 2)
	head := strings.TrimPrefix(fields[0], "#")
	if n, err := strconv.Atoi(head); err == nil {
		if len(fields) == 2 {
			return n, strings.TrimSpace(fields[1])
		}
		return n, ""
	}
	return 0, args
}

func renderStatus(st *ipc.Status) string {
	var b strings.Builder
	fmt.Fprintf(&b, "clexd %s — %s", st.Version, st.Repo)
	if st.Paused {
		b.WriteString(" [paused]")
	}
	if len(st.Running) == 0 {
		b.WriteString("\nidle")
	}
	for _, r := range st.Running {
		fmt.Fprintf(&b, "\n#%d %s (%s)", r.Issue, r.Model, r.Stage)
	}
	if st.PendingGate != "" {
		fmt.Fprintf(&b, "\ngate pending: %s", st.PendingGate)
	}
	return b.String()
}

func renderModels(models []ipc.ModelHealth) string {
	if len(models) == 0 {
		return "no models available"
	}
	var b strings.Builder
	b.WriteString("models:")
	for _, m := range models {
		mark := "✓"
		if !m.Healthy {
			mark = "✗"
		}
		fmt.Fprintf(&b, "\n%s %s (%s)", mark, m.Model, m.Provider)
	}
	return b.String()
}
