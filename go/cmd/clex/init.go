package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/reissui/clex/internal/config"
	"github.com/reissui/clex/internal/core"
	"github.com/reissui/clex/internal/gh"
)

// initFixes maps each probed dependency to the exact fix command printed when it
// is missing, so a ✗ line always tells the user what to run (spec: onboarding
// wizard — "✗ per line with the fix command").
var initFixes = map[string]string{
	"claude": "install the Claude CLI (https://docs.anthropic.com/claude/cli), then `claude login`",
	"codex":  "install the Codex CLI, then authenticate per its docs",
	"gh":     "install GitHub CLI (https://cli.github.com), then `gh auth login`",
	"ollama": "install Ollama for local models (https://ollama.com) — optional",
}

// initResult is the machine summary of a wizard run (emitted by --json).
type initResult struct {
	OK          bool     `json:"ok"`
	Repo        string   `json:"repo"`
	ConfigPath  string   `json:"config_path"`
	LabelsSet   bool     `json:"labels_set"`
	BotUsername string   `json:"bot_username,omitempty"`
	ChatID      int64    `json:"chat_id,omitempty"`
	Deps        []string `json:"missing_deps,omitempty"`
	Message     string   `json:"message"`
}

// cmdInit runs the guided setup wizard. Interactive by default; --yes runs
// non-interactively from flags for scripted setup (spec: onboarding wizard).
//
// Steps: (1) probe claude/codex/gh/ollama with live ✓/✗ and fix commands;
// (2) resolve the repo (flag or git origin) and ensure labels; (3) obtain a
// Telegram token (flag or prompt with a @BotFather pointer), verify it, and do
// the tap-to-bind handshake to capture the chat id; (4) write the config
// scaffold; (5) print a green summary and the first command to try.
//
// Idempotent: EnsureLabels is a no-op on re-run, and the config is rewritten in
// place — re-running never duplicates anything.
func cmdInit(e *env, args []string) int {
	fs, jsonOut := newFlagSet(e, "init", "guided setup wizard")
	yes := fs.Bool("yes", false, "non-interactive: take all values from flags, no prompts")
	repoFlag := fs.String("repo", "", "repository owner/name (defaults to the git origin)")
	tokenFlag := fs.String("telegram-token", "", "Telegram bot token (from @BotFather)")
	chatFlag := fs.Int64("chat-id", 0, "Telegram owner chat id (skips tap-to-bind in --yes mode)")
	skipTG := fs.Bool("skip-telegram", false, "skip Telegram setup entirely")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	ctx, cancel := e.cmdContext()
	defer cancel()

	w := &wizard{e: e, json: *jsonOut, yes: *yes}
	return w.runInit(ctx, initOpts{
		repo:      strings.TrimSpace(*repoFlag),
		token:     strings.TrimSpace(*tokenFlag),
		chatID:    *chatFlag,
		skipTG:    *skipTG,
		promptsIn: bufio.NewReader(e.stdin),
	})
}

// initOpts carries the resolved flag inputs into the wizard body.
type initOpts struct {
	repo      string
	token     string
	chatID    int64
	skipTG    bool
	promptsIn *bufio.Reader
}

// wizard holds the wizard's output/mode so step methods stay small. token holds
// the verified Telegram token once setupTelegram accepts one, so runInit can
// persist it in the config scaffold.
type wizard struct {
	e     *env
	json  bool
	yes   bool
	token string
}

// line prints a wizard line unless in JSON mode (JSON mode emits only the final
// object). It centralizes the human/JSON split.
func (w *wizard) line(format string, a ...any) {
	if w.json {
		return
	}
	fmt.Fprintf(w.e.stdout, format+"\n", a...)
}

// runInit executes the wizard steps and renders the outcome.
func (w *wizard) runInit(ctx context.Context, opts initOpts) int {
	res := initResult{}

	// Step 1 — dependencies.
	w.line("Checking dependencies:")
	var missing []string
	for _, name := range []string{"claude", "codex", "gh", "ollama"} {
		r := w.e.probe.Probe(ctx, name)
		if r.Found && r.Authed {
			detail := r.Version
			if detail == "" {
				detail = "found"
			}
			w.line("  ✓ %-7s %s", name, detail)
			continue
		}
		// ollama is optional; a miss is a soft note, not a blocker.
		missing = append(missing, name)
		reason := "not found"
		if r.Found && !r.Authed {
			reason = "found but not authenticated"
		}
		w.line("  ✗ %-7s %s", name, reason)
		w.line("      fix: %s", initFixes[name])
	}
	res.Deps = missing
	if blocker := firstRequiredMissing(missing); blocker != "" && w.yes {
		// In scripted mode a missing required binary is fatal so CI catches it; the
		// fix command was already printed, and the run can be resumed after install.
		res.Message = fmt.Sprintf("missing required dependency %q — install it and re-run `clex init --yes`", blocker)
		return w.finish(res, exitError)
	}

	// Step 2 — repository + labels.
	repo, ok := w.resolveRepo(opts.repo)
	if !ok {
		res.Message = "no repository: pass --repo owner/name or run inside a git repo with a GitHub origin"
		return w.finish(res, exitError)
	}
	res.Repo = repo.String()
	w.line("")
	w.line("Repository: %s", repo)
	if code := w.ensureLabels(ctx, repo, &res); code != exitOK {
		res.Message = "could not create labels (see error above)"
		return w.finish(res, code)
	}

	// Step 3 — Telegram (unless skipped).
	if !opts.skipTG {
		if code := w.setupTelegram(ctx, opts, &res); code != exitOK {
			return w.finish(res, code)
		}
	} else {
		w.line("")
		w.line("Telegram: skipped (--skip-telegram)")
	}

	// Step 4 — config scaffold.
	cfgPath := w.e.globalConfigPath()
	res.ConfigPath = cfgPath
	if err := writeConfigScaffold(cfgPath, w.token, res.ChatID, missing); err != nil {
		res.Message = fmt.Sprintf("write config: %v", err)
		return w.finish(res, exitError)
	}
	w.line("")
	w.line("Wrote config scaffold: %s", cfgPath)

	// Step 5 — green summary.
	res.OK = true
	res.Message = "clex is configured"
	w.line("")
	w.line("✓ Setup complete for %s.", repo)
	if res.BotUsername != "" {
		w.line("  Telegram bot: @%s (chat id %d)", res.BotUsername, res.ChatID)
	}
	w.line("  Next: start the daemon, then run `clex status`. File your first idea with:")
	w.line("        clex idea \"add a health endpoint\" --repo %s", repo)
	return w.finish(res, exitOK)
}

// finish emits the JSON result (JSON mode) and returns code.
func (w *wizard) finish(res initResult, code int) int {
	if w.json {
		writeJSON(w.e.stdout, res)
	}
	return code
}

// resolveRepo picks the repo from the flag or the git origin.
func (w *wizard) resolveRepo(flagRepo string) (gh.Repo, bool) {
	s := strings.TrimSpace(flagRepo)
	if s == "" {
		if r, ok := w.e.configuredRepo(); ok {
			s = r
		}
	}
	if s == "" {
		return gh.Repo{}, false
	}
	repo, err := gh.ParseRepo(s)
	if err != nil {
		w.line("  ✗ invalid repository %q: %v", s, err)
		return gh.Repo{}, false
	}
	return repo, true
}

// ensureLabels creates the clex label set on the repo (idempotent). Agent tags
// are seeded for the default providers so the board is complete on first run.
func (w *wizard) ensureLabels(ctx context.Context, repo gh.Repo, res *initResult) int {
	token, err := w.e.ghToken(ctx)
	if err != nil {
		w.line("  ✗ no GitHub token: run `gh auth login`")
		return exitError
	}
	client, err := w.e.newGH(token)
	if err != nil {
		w.line("  ✗ GitHub client: %v", err)
		return exitError
	}
	if err := client.EnsureLabels(ctx, repo, []string{"claude", "codex"}); err != nil {
		w.line("  ✗ ensure labels: %v", err)
		return exitError
	}
	res.LabelsSet = true
	w.line("  ✓ labels ensured (pipeline states, epic marker, agent tags)")
	return exitOK
}

// setupTelegram obtains a token, verifies it, and binds the chat id. In --yes
// mode the token comes from the flag and the chat id from --chat-id (no
// tap-to-bind). Interactively it prompts (with a @BotFather pointer) and runs the
// tap-to-bind handshake.
//
// Each network step (Verify, Bind) runs under its own fresh deadline minted
// *after* the token is read, so time spent in @BotFather never consumes the
// outside-world budget (issue #40). The incoming ctx (the command context) may
// therefore already be near-expired from prompt time; it is deliberately not
// threaded into the network calls.
func (w *wizard) setupTelegram(_ context.Context, opts initOpts, res *initResult) int {
	w.line("")
	w.line("Telegram setup:")
	token := opts.token
	if token == "" && !w.yes {
		w.line("  Create a bot with @BotFather and paste its token (or blank to skip):")
		token = strings.TrimSpace(w.prompt(opts.promptsIn))
		if token == "" {
			w.line("  Telegram: skipped")
			return exitOK
		}
	}
	if token == "" {
		// --yes with no token: skip Telegram rather than fail the whole wizard.
		w.line("  Telegram: skipped (no --telegram-token)")
		return exitOK
	}

	// Fresh deadline for getMe, minted now that the token is in hand.
	vctx, vcancel := context.WithTimeout(context.Background(), telegramStepTimeout)
	vr := w.e.telegram.Verify(vctx, token)
	vcancel()
	if !vr.Valid {
		// Never echo the token: the raw getMe error embeds the bot<token> URL.
		w.line("  ✗ token rejected: %s", redactToken(vr.Detail, token))
		if w.yes {
			res.Message = "Telegram token rejected"
			return exitError
		}
		// Interactive: a network-restricted user shouldn't be dead-ended. Offer to
		// save the token unverified so doctor can re-verify later.
		if w.confirm(opts.promptsIn, "  Couldn't verify with Telegram — save the token anyway and verify later?") {
			w.token = token
			w.line("  Telegram: token saved unverified; run `clex doctor` to verify later")
			return exitOK
		}
		w.line("  Telegram: skipping (fix the token and re-run to enable it)")
		return exitOK
	}
	res.BotUsername = vr.BotUsername
	w.token = token
	w.line("  ✓ token valid: @%s", vr.BotUsername)

	// Chat id: from flag in --yes mode, else tap-to-bind.
	if w.yes {
		res.ChatID = opts.chatID
		if opts.chatID == 0 {
			w.line("  ! no --chat-id given; set telegram_chat_id later")
		}
		return exitOK
	}
	w.line("  Now message your bot once so we can bind your chat id…")
	// Fresh deadline again: the bind poll gets its own full budget, independent of
	// how long verification and the prompts took.
	bctx, bcancel := context.WithTimeout(context.Background(), telegramStepTimeout)
	br := w.e.telegram.Bind(bctx, token)
	bcancel()
	if !br.Valid {
		w.line("  ✗ bind failed: %s", redactToken(br.Detail, token))
		w.line("  Telegram: token saved; bind later by re-running init")
		return exitOK
	}
	res.ChatID = br.ChatID
	w.line("  ✓ bound chat id %d", br.ChatID)
	return exitOK
}

// redactToken replaces every occurrence of the bot token in s with a placeholder
// so a failure that echoes the getMe URL (…/bot<token>/getMe) never leaks the
// secret into the terminal or logs (issue #40). It is defensive against both the
// bare token and the URL-embedded form.
func redactToken(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "REDACTED")
}

// confirm prompts a yes/no question (default No) and reports whether the user
// answered affirmatively. A nil reader or EOF is treated as No.
func (w *wizard) confirm(r *bufio.Reader, question string) bool {
	w.line("%s [y/N]", question)
	ans := strings.ToLower(strings.TrimSpace(w.prompt(r)))
	return ans == "y" || ans == "yes"
}

// prompt reads one line of input from the wizard's reader.
func (w *wizard) prompt(r *bufio.Reader) string {
	if r == nil {
		return ""
	}
	s, _ := r.ReadString('\n')
	return strings.TrimRight(s, "\r\n")
}

// requiredDeps are the dependencies whose absence blocks a scripted setup
// (ollama is optional and excluded).
var requiredDeps = map[string]bool{"claude": true, "codex": true, "gh": true}

// firstRequiredMissing returns the first blocking dependency in missing, or "".
func firstRequiredMissing(missing []string) string {
	for _, m := range missing {
		if requiredDeps[m] {
			return m
		}
	}
	return ""
}

// writeConfigScaffold writes a minimal runnable global config to path, creating
// ~/.clex (0700). It uses config.Default so the result passes Validate, then
// serializes it as TOML. Idempotent: an existing file is overwritten in place
// with the same shape, so re-running init never duplicates config.
func writeConfigScaffold(path, token string, chatID int64, missing []string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	// Enforce 0700 even if the directory pre-existed with looser perms: the clex
	// home holds the socket, config, and spool and must not be group/world
	// readable (spec: Security model — config/DB/spool 0700/0600).
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure %s: %w", dir, err)
	}
	// Prefer a provider that is actually installed for the single default model.
	provider, kind, model := defaultProviderChoice(missing)
	cfg := config.Default(token, chatID, provider, kind, model)
	// With BOTH claude and codex installed, split the roles the way the process
	// is designed to run: Claude plans, reviews, and chats (top tier); codex
	// (GPT) is the builder. A single-provider setup keeps the Default shape.
	missingSet := make(map[string]bool, len(missing))
	for _, m := range missing {
		missingSet[m] = true
	}
	if !missingSet["claude"] && !missingSet["codex"] {
		applyDualProviderRouting(cfg)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return nil
}

// applyDualProviderRouting rewrites a Default (single-provider) scaffold into
// the two-provider split: claude's opus on the "top" tier planning, reviewing,
// linting, and chatting; codex's gpt-5-codex on the "build" tier building.
// This encodes the intended division of labor — Claude as the planner, GPT as
// the developer — while every knob stays owner-editable TOML.
func applyDualProviderRouting(cfg *config.Config) {
	cfg.Providers = map[string]config.Provider{
		"claude": {Kind: "claude-cli"},
		"codex":  {Kind: "codex-cli"},
	}
	cfg.Models = map[string]config.Model{
		"opus":        {Provider: "claude", Billing: core.BillingSubscription},
		"gpt-5-codex": {Provider: "codex", Billing: core.BillingSubscription},
	}
	cfg.Tiers = core.TierMap{
		"top":   {"opus"},
		"build": {"gpt-5-codex"},
	}
	cfg.Routing = map[string]config.Routing{
		string(core.RolePlan):   {Tier: "top"},
		string(core.RoleReview): {Tier: "top"},
		string(core.RoleLint):   {Tier: "top"},
		string(core.RoleBot):    {Tier: "top"},
		string(core.RoleBuild):  {Tier: "build"},
	}
}

// defaultProviderChoice picks the provider/kind/model for the scaffold's single
// default model, preferring claude, then codex, based on what is installed. It
// always returns a runnable triple so config.Default yields a valid config.
func defaultProviderChoice(missing []string) (provider, kind, model string) {
	missingSet := make(map[string]bool, len(missing))
	for _, m := range missing {
		missingSet[m] = true
	}
	if !missingSet["claude"] {
		return "claude", "claude-cli", "opus"
	}
	if !missingSet["codex"] {
		return "codex", "codex-cli", "gpt-5-codex"
	}
	// Nothing installed yet: still write a claude-shaped scaffold the user can
	// complete after installing a provider.
	return "claude", "claude-cli", "opus"
}
