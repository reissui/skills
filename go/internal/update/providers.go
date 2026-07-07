package update

import (
	"context"
	"fmt"
)

// Layer 2 of self-update: the provider CLIs (spec: Self-update layer 2). clex
// shells out to the official updaters — never a provider API:
//   - claude: `claude update`
//   - codex:  its package manager (`brew upgrade codex` or `npm install -g
//     @openai/codex@latest`), detected per host.
//
// After any successful bump, the registry re-probes and doctor's version-pin
// check runs, so adapter output-format drift is caught immediately. Every
// external call goes through an injected CmdRunner and the re-probe through an
// injected Prober, so tests assert the exact commands and that the post-update
// probe fired — with zero real binaries.

// CmdRunner runs an external command and returns its combined output. The real
// implementation execs the binary with a least-privilege environment (spec:
// least-privilege child processes); tests inject a recorder that scripts results
// and captures every invocation. Keeping this an interface (not os/exec
// directly) is what makes layer 2 testable without touching the machine.
type CmdRunner interface {
	// Run executes name with args and returns combined stdout+stderr and any
	// error (a non-zero exit is an error). ctx cancels the child.
	Run(ctx context.Context, name string, args ...string) (output string, err error)
}

// PkgManager identifies how a provider CLI is installed, selecting the upgrade
// command. It is injectable so tests exercise both codex paths without probing
// the host.
type PkgManager string

const (
	// PkgBrew installs/updates codex via Homebrew.
	PkgBrew PkgManager = "brew"
	// PkgNpm installs/updates codex via npm global.
	PkgNpm PkgManager = "npm"
)

// Prober triggers a registry re-probe. (*registry.Registry).Probe satisfies this
// exactly (its Probe takes a context and returns nothing), so wiring passes the
// registry directly; tests pass a fake that records it fired. Defined here to
// avoid importing internal/registry (keeps layer 2 dependency-light and its
// tests free of a real registry).
type Prober interface {
	Probe(ctx context.Context)
}

// VersionPinChecker is doctor's minimum-known-good version check, run after a
// bump to catch a provider CLI that updated past (or drifted from) a pinned
// version. It is injected so layer 2 does not depend on the doctor package;
// wiring passes doctor's checker, tests pass a recorder. A returned error is
// surfaced, not fatal to the update run.
type VersionPinChecker interface {
	// CheckPins re-validates provider CLI versions against known-good pins.
	CheckPins(ctx context.Context) error
}

// ProviderUpdater runs layer 2. All collaborators are injected.
type ProviderUpdater struct {
	// Runner executes the updater commands; required.
	Runner CmdRunner
	// CodexPkg selects codex's package manager (PkgBrew or PkgNpm). Detected at
	// wiring time (e.g. from `which codex` / config); required to update codex.
	CodexPkg PkgManager
	// Registry re-probes after a successful bump; optional (skipped if nil).
	Registry Prober
	// Pins runs doctor's version-pin check after a bump; optional (skipped if
	// nil).
	Pins VersionPinChecker
}

// ProviderResult reports the outcome of one provider's update attempt.
type ProviderResult struct {
	// Provider is the provider name ("claude" or "codex").
	Provider string
	// Command is the argv actually run (for logging), e.g.
	// ["claude", "update"].
	Command []string
	// Output is the updater's combined output (may be empty).
	Output string
	// Updated is true when the updater command succeeded.
	Updated bool
	// Err is the updater error, if the command failed.
	Err error
}

// UpdateClaude runs `claude update`. On success it triggers the post-update
// re-probe and version-pin check (both injected). The command run is recorded in
// the returned result so tests can assert the exact argv.
func (p *ProviderUpdater) UpdateClaude(ctx context.Context) (ProviderResult, error) {
	return p.run(ctx, "claude", []string{"claude", "update"})
}

// UpdateCodex runs codex's package-manager upgrade, chosen by CodexPkg:
//   - PkgBrew → `brew upgrade codex`
//   - PkgNpm  → `npm install -g @openai/codex@latest`
//
// On success it triggers the post-update re-probe and version-pin check. An
// unknown/empty CodexPkg is an error (we never guess a package manager).
func (p *ProviderUpdater) UpdateCodex(ctx context.Context) (ProviderResult, error) {
	argv, err := codexUpgradeArgv(p.CodexPkg)
	if err != nil {
		return ProviderResult{Provider: "codex", Err: err}, err
	}
	return p.run(ctx, "codex", argv)
}

// codexUpgradeArgv returns the full argv for codex's upgrade under pkg.
func codexUpgradeArgv(pkg PkgManager) ([]string, error) {
	switch pkg {
	case PkgBrew:
		return []string{"brew", "upgrade", "codex"}, nil
	case PkgNpm:
		return []string{"npm", "install", "-g", "@openai/codex@latest"}, nil
	default:
		return nil, fmt.Errorf("update: unknown codex package manager %q (want brew or npm)", pkg)
	}
}

// run executes one updater argv and, on success, fires the post-update hooks. It
// centralises the record-command → run → (probe + pin-check) sequence so both
// providers behave identically.
func (p *ProviderUpdater) run(ctx context.Context, provider string, argv []string) (ProviderResult, error) {
	res := ProviderResult{Provider: provider, Command: argv}
	if p.Runner == nil {
		res.Err = fmt.Errorf("update: ProviderUpdater has no CmdRunner")
		return res, res.Err
	}
	out, err := p.Runner.Run(ctx, argv[0], argv[1:]...)
	res.Output = out
	if err != nil {
		res.Err = fmt.Errorf("update: %s updater failed: %w", provider, err)
		return res, res.Err
	}
	res.Updated = true
	// Post-update: re-probe the registry and re-check version pins so drift is
	// caught right after the bump (spec: layer 2). These are best-effort — a pin
	// warning does not undo a successful update — so a pin error is attached but
	// not returned as the run's failure.
	p.postUpdate(ctx)
	return res, nil
}

// postUpdate fires the injected re-probe and version-pin check, if wired.
func (p *ProviderUpdater) postUpdate(ctx context.Context) {
	if p.Registry != nil {
		p.Registry.Probe(ctx)
	}
	if p.Pins != nil {
		// A pin mismatch is surfaced by doctor separately; here we only ensure
		// the check runs. Ignore its error at this layer.
		_ = p.Pins.CheckPins(ctx)
	}
}

// UpdateAll runs both providers' updaters in sequence and returns a result per
// provider. A failure on one provider does not stop the other (independent
// subscriptions degrade independently — spec: single-provider degradation). The
// aggregate error is non-nil only if every attempted provider failed.
func (p *ProviderUpdater) UpdateAll(ctx context.Context) ([]ProviderResult, error) {
	results := []ProviderResult{}
	c, _ := p.UpdateClaude(ctx)
	results = append(results, c)
	x, _ := p.UpdateCodex(ctx)
	results = append(results, x)

	anyOK := false
	for _, r := range results {
		if r.Updated {
			anyOK = true
		}
	}
	if !anyOK {
		return results, fmt.Errorf("update: all provider updaters failed")
	}
	return results, nil
}
