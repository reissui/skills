# clex

clex is a self-hosted agentic development orchestrator. It automates the workflow of turning a feature idea into a researched PRD, a dependency-aware set of GitHub issues, and parallel agent-built pull requests — controlled from Telegram, running on the owner's own machine against their existing Claude Max and ChatGPT subscriptions plus local models.

It replaces the manual loop of prompting Claude and Codex apps separately, copying output between them, and hand-creating GitHub issues.

## Design

The authoritative design is [`docs/superpowers/specs/2026-07-03-clex-design.md`](docs/superpowers/specs/2026-07-03-clex-design.md).

## Layout

- `cmd/clex` — the `clex` CLI (setup, manual pipeline control, diagnostics).
- `cmd/clexd` — the `clexd` daemon (event loop, scheduler, runner supervisor).
- `internal/` — implementation packages.

## Building

```sh
make build      # builds ./bin/clex and ./bin/clexd
make test       # runs the test suite
make lint       # go vet + gofmt check
```

Requires Go 1.26+.

## Status

v1 is under active construction. See the epic and its child issues for scope.
