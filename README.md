# clex

clex is a self-hosted agentic development orchestrator. It turns a feature idea
into a researched PRD, dependency-aware GitHub issues, parallel agent-built PRs,
and one final human-reviewed PR to `main`. It runs on your own machine, controlled
from Telegram or the CLI, using Claude/Codex subscriptions and local Ollama
models without handing pipeline state to a hosted service.

```mermaid
flowchart LR
  A["Idea"] --> B["Plan PRD + child issues"]
  B --> C["Build in parallel worktrees"]
  C --> D["Top-tier review"]
  D --> E["Epic integration branch"]
  E --> F["One PR to main"]
```

The full design lives in
[docs/superpowers/specs/2026-07-03-clex-design.md](docs/superpowers/specs/2026-07-03-clex-design.md).

## Install

Homebrew:

```sh
brew tap reissui/tap
brew install --cask clex
```

Go install:

```sh
go install github.com/reissui/clex/cmd/clex@latest
go install github.com/reissui/clex/cmd/clexd@latest
```

Checksum-verifying install script:

```sh
curl -fsSL https://raw.githubusercontent.com/reissui/clex/main/install.sh | sh
```

To pin a version or install into a user-writable directory:

```sh
CLEX_INSTALL_VERSION=v0.1.0 CLEX_INSTALL_DIR="$HOME/.local/bin" \
  sh -c "$(curl -fsSL https://raw.githubusercontent.com/reissui/clex/main/install.sh)"
```

The installer downloads the matching macOS/Linux archive, verifies it against
the release `checksums.txt`, and installs both `clex` and `clexd`.

## Local Setup

Run setup from inside a GitHub-backed checkout:

```sh
clex init
```

Expected flow:

```text
Checking dependencies:
  ✓ claude  ...
  ✓ codex   ...
  ✓ gh      ...
  ! ollama  not found
      fix: install Ollama for local models: https://ollama.com (optional)

Repository: owner/repo
  ✓ labels ensured (pipeline states, epic marker, agent tags)

Telegram setup:
  Create a bot with @BotFather and paste its token (or blank to skip):
  ✓ token valid: @your_bot
  Now message your bot once so we can bind your chat id...
  ✓ bound chat id 123456789

Wrote config scaffold: ~/.clex/config.toml

✓ Setup complete for owner/repo.
  Next: start the daemon, then run `clex status`. File your first idea with:
        clex idea "add a health endpoint" --repo owner/repo
```

For non-interactive setup:

```sh
clex init --yes \
  --repo owner/repo \
  --telegram-token "$TELEGRAM_TOKEN" \
  --chat-id "$TELEGRAM_CHAT_ID"
```

Start the daemon in a terminal:

```sh
clexd --repo owner/repo
```

The daemon reads its GitHub token from `GITHUB_TOKEN` or `GH_TOKEN`, falling back
to the gh CLI (`gh auth token`) when neither is set — so a `gh auth login` is
enough for local use. For a server deployment, set `GITHUB_TOKEN` to a
fine-grained PAT scoped to the managed repos.

Submit work:

```sh
clex idea "add a health endpoint" --repo owner/repo
clex status
```

## Remote Hosting

Remote hosting uses the same config. Set up locally, copy `~/.clex/`, then
install the daemon service on the remote machine.

macOS:

```sh
brew tap reissui/tap
brew install --cask clex
rsync -a ~/.clex/ remote-mac:~/.clex/
ssh remote-mac 'clex service install --repo owner/repo && clex service status'
```

Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/reissui/clex/main/install.sh | sh
rsync -a ~/.clex/ remote-linux:~/.clex/
ssh remote-linux 'sudo env PATH=$PATH clex service install --repo owner/repo --user clex && clex service status'
```

Service units are generated from the templates in
[deploy/launchd](deploy/launchd) and [deploy/systemd](deploy/systemd).

## CLI Commands

```text
clex init       Guided setup wizard
clex doctor     Check binaries, auth, tokens, and role resolution
clex service    Install/uninstall/status the launchd or systemd unit
clex idea       File a feature idea as a labelled GitHub issue
clex plan       Queue an issue for planning (the daemon researches + plans it)
clex build      Approve an issue — or a whole epic's planned issues — for building
clex status     Show pipeline and daemon state
clex steer      Send steering guidance to an issue or epic
clex models     Show model registry health
clex costs      Show spend and estimate drift
clex pause      Pause new dispatches
clex resume     Resume dispatching
clex gc         Garbage-collect merged worktrees
clex update     Update the clex binary
```

Every command accepts `--json` where machine-readable output is implemented.

## Telegram

Telegram is a conversation, not just a command console. Any plain message is
chat: it runs on the configured chat model (`[routing.bot]`, Claude by
default) inside the repo checkout, with the conversation carried across
messages — ask questions, think out loud, explore the codebase. When the
conversation turns into something worth building, `/plan` hands it to the
pipeline.

The core loop:

1. **Chat** about what you want (optional but useful context).
2. **`/plan add per-user rate limits`** — files the idea; the planner (Claude,
   top tier) researches the repo and turns it into a PRD epic plus
   agent-ready sub-issues on GitHub, then reports back. A bare `/plan`
   distills the current chat conversation into the idea.
3. **Review the plan** on GitHub (or `/steer <epic> <text>` to adjust it).
4. **`/build <epic#>`** — approves every planned sub-issue; builders (GPT via
   the codex CLI by default) implement them in parallel worktrees on one
   integration branch, each PR reviewed by a top-tier model.
5. **One final PR to `main`** opens when everything lands — merge it yourself
   on GitHub, or reply **`/merge <pr#>`** to confirm from Telegram.

| Command | Purpose |
| --- | --- |
| *(any text)* | Chat with the bot model — repo-aware, conversation persists. |
| `/plan [idea]` | Turn an idea (or the chat so far) into a PRD epic + sub-issues. |
| `/build <epic\|issue>` | Approve an epic's planned issues (or one issue) for building. |
| `/merge <pr>` | Merge the final epic PR. |
| `/model [id]` | Show or switch the chat model. |
| `/status` | Show active issues, gates, and daemon state. |
| `/pause` | Hold new dispatches; running work continues. |
| `/resume` | Resume dispatching. |
| `/stop <issue>` | Cancel one running issue (build keeps its worktree). |
| `/steer <issue> <text>` | Send guidance to a running or idle issue, or re-arm a failed plan. |
| `/models` | Show available models and provider health. |
| `/costs` | Show current epic and daily spend estimates. |

Progress messages stay one line. Cost questions use confirm-or-alter buttons
so the default path is a single tap.

## Config

The global config is `~/.clex/config.toml`; a repo may add `.clex/config.toml`
as a shallow overlay.

```toml
[providers.claude]
kind = "claude-cli"

[providers.codex]
kind = "codex-cli"

[providers.ollama]
kind = "ollama"
autodetect = true

[models]
opus-4-8 = { provider = "claude", billing = "subscription" }
gpt-5-5 = { provider = "codex", billing = "subscription" }
qwen3-coder = { provider = "ollama", billing = "free" }

[tiers]
top = ["opus-4-8", "gpt-5-5"]
local = ["qwen3-coder"]

[routing.plan]
tier = "top"
effort = "max"

[routing.build]
policy = "auto"

[routing.review]
tier = "top"
```

See [docs/config-reference.md](docs/config-reference.md) for every key.

## Security Notes

- Only owner- or clex-authored GitHub content drives pipeline actions.
- Issue verification commands are honored only from trusted authors; otherwise
  the repo default command runs.
- Runner child processes receive allowlisted environments. Anthropic API
  credentials are stripped so subscription CLI auth cannot silently become
  metered API usage.
- Work runs in issue worktrees, never in the primary checkout, and clex never
  pushes `main`.
- Telegram checks the configured sender id on messages and callbacks.
- Config, database, IPC socket, and image spool paths use owner-only
  permissions.
- Full-permission runner modes are opt-in config choices. An authenticated `gh`
  CLI is the supported happy path and passes `clex doctor` clean. `clex doctor`
  warns about over-scoped classic tokens only when you supply one via
  `GITHUB_TOKEN`/`GH_TOKEN` (e.g. a server deployment), where a fine-grained PAT
  is the actionable fix; it reports branch protection as informational, not a
  requirement.

## Troubleshooting

Run:

```sh
clex doctor --repo owner/repo
```

Exit codes:

| Code | Meaning |
| --- | --- |
| `0` | Healthy, or warnings only. |
| `1` | Command usage or ordinary command failure. |
| `2` | Doctor found a blocking problem. |

Common fixes:

```sh
gh auth login
claude login
codex login
ollama list
clex service status
```

For deterministic integration tests and local release validation:

```sh
go test -tags e2e ./e2e/...
goreleaser check
sh packaging/test-install.sh
```
