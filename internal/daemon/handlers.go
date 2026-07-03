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
