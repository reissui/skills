// Command clexd is the long-running clex daemon: it polls GitHub and Telegram,
// schedules work, and supervises runner child processes.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/reissui/clex/internal/config"
	"github.com/reissui/clex/internal/daemon"
	"github.com/reissui/clex/internal/gh"
	"github.com/reissui/clex/internal/ipc"
	"github.com/reissui/clex/internal/store"
	"github.com/reissui/clex/internal/telegram"
	"github.com/reissui/clex/internal/version"
)

const usage = `clexd — self-hosted agentic development orchestrator (daemon)

Usage:
  clexd [flags]

Flags:
  --repo owner/name   Repository to manage (or set CLEX_REPO)
  --config path       Global config file (default ~/.clex/config.toml)
  --home path         clex home directory (default ~/.clex)
  --version           Print the clexd version and exit
  --help              Show this help

The daemon long-polls GitHub and Telegram (no public endpoint required),
schedules eligible issues, and supervises runner processes. The GitHub token is
read from GITHUB_TOKEN or GH_TOKEN, falling back to the gh CLI (gh auth token)
when neither is set — so authenticating gh is sufficient. For a server
deployment, prefer a fine-grained PAT scoped to the managed repos via
GITHUB_TOKEN. See docs/superpowers/specs/2026-07-03-clex-design.md for the full
design.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses flags and either prints version/usage (the pure, test-covered
// paths) or, when a repo is supplied, boots and runs the daemon until SIGTERM.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("clexd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	showVersion := fs.Bool("version", false, "print version and exit")
	repoFlag := fs.String("repo", os.Getenv("CLEX_REPO"), "repository owner/name to manage")
	configFlag := fs.String("config", "", "global config file path")
	homeFlag := fs.String("home", "", "clex home directory")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, version.Version)
		return 0
	}
	// No repo configured: print usage and exit success (the scaffold behavior,
	// so `clexd` with no arguments is a harmless help invocation).
	if strings.TrimSpace(*repoFlag) == "" {
		fmt.Fprint(stdout, usage)
		return 0
	}

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := boot(*repoFlag, *configFlag, *homeFlag, log); err != nil {
		fmt.Fprintf(stderr, "clexd: %v\n", err)
		return 1
	}
	return 0
}

// boot resolves configuration and dependencies, then runs the daemon until a
// termination signal. It is intentionally separate from run so run stays a thin,
// test-friendly flag parser.
func boot(repoArg, configPath, homeArg string, log *slog.Logger) error {
	repo, err := gh.ParseRepo(repoArg)
	if err != nil {
		return fmt.Errorf("parse --repo: %w", err)
	}

	home := homeArg
	if home == "" {
		hd, herr := os.UserHomeDir()
		if herr != nil {
			return fmt.Errorf("resolve home: %w", herr)
		}
		home = filepath.Join(hd, ".clex")
	}
	home, err = daemon.EnsureHome(home)
	if err != nil {
		return err
	}

	if configPath == "" {
		configPath = filepath.Join(home, "config.toml")
	}
	cfg, warns, err := config.Load(configPath, "")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	for _, w := range warns {
		log.Warn("config", "warning", w.Message)
	}

	token, err := resolveGitHubToken(os.Getenv, gh.TokenFromGH)
	if err != nil {
		return err
	}
	self := os.Getenv("CLEX_SELF_LOGIN")
	ghc, err := gh.New(token, gh.WithSelfLogin(self))
	if err != nil {
		return fmt.Errorf("github client: %w", err)
	}

	st, err := store.Open(daemon.DBPath(home))
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	tg, err := telegram.New(telegram.Config{
		Token:          cfg.TelegramToken,
		ChatID:         cfg.TelegramChatID,
		AllowedUserIDs: allowedUserIDs(cfg),
		SpoolDir:       daemon.SpoolDir(home),
	})
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}

	dcfg := daemon.Config{
		Repo:          repo,
		Home:          home,
		Owner:         repo.Owner,
		SelfLogin:     self,
		DefaultVerify: cfg.Verification,
		MaxUSDPerEpic: cfg.Budget.MaxUSDPerEpic,
		Caps:          capsFromConfig(cfg),
	}
	d, err := daemon.FromConfig(cfg, dcfg, ghc, tg, st, log, nil)
	if err != nil {
		return fmt.Errorf("build daemon: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Start the IPC control server (0600 socket under ~/.clex) for the CLI (#17).
	srv, err := ipc.Listen(ipc.SocketPath(home), d, log)
	if err != nil {
		return fmt.Errorf("ipc listen: %w", err)
	}
	go func() { _ = srv.Serve(ctx) }()
	defer srv.Close()

	// Drive the Telegram long-poll loop alongside the daemon.
	go func() {
		if rerr := tg.Run(ctx); rerr != nil && ctx.Err() == nil {
			log.Warn("telegram run", "err", rerr.Error())
		}
	}()

	return d.Run(ctx)
}

// resolveGitHubToken picks the daemon's GitHub token: an explicit GITHUB_TOKEN or
// GH_TOKEN env var wins (the server-deployment path, where a fine-grained PAT is
// recommended), otherwise it falls back to the gh CLI's own credentials via
// ghFallback (`gh auth token`) so simply authenticating gh is sufficient
// end-to-end (issue #40). getenv and ghFallback are parameters so this is unit
// tested without touching the real environment or shelling out.
func resolveGitHubToken(getenv func(string) string, ghFallback func(context.Context) (string, error)) (string, error) {
	if tok := getenv("GITHUB_TOKEN"); tok != "" {
		return tok, nil
	}
	if tok := getenv("GH_TOKEN"); tok != "" {
		return tok, nil
	}
	gctx, gcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer gcancel()
	tok, err := ghFallback(gctx)
	if err != nil {
		return "", fmt.Errorf("no GitHub token: set GITHUB_TOKEN or GH_TOKEN, or run `gh auth login` (%v)", err)
	}
	return tok, nil
}

// allowedUserIDs derives the authorized Telegram user id set. clex authorizes
// per-user; absent an explicit list, the owner's chat id doubles as the single
// authorized user id (the common single-operator case).
func allowedUserIDs(cfg *config.Config) []int64 {
	if cfg.TelegramChatID != 0 {
		return []int64{cfg.TelegramChatID}
	}
	return nil
}

// capsFromConfig flattens the per-provider concurrency caps.
func capsFromConfig(cfg *config.Config) map[string]int {
	if len(cfg.Caps) == 0 {
		return nil
	}
	out := make(map[string]int, len(cfg.Caps))
	for name, c := range cfg.Caps {
		out[name] = c.MaxConcurrent
	}
	return out
}
