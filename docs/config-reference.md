# clex Config Reference

clex reads a global config from `~/.clex/config.toml` and an optional per-repo
overlay from `.clex/config.toml`. The per-repo file replaces configured keys
shallowly; maps are merged by key.

## Top-Level Keys

```toml
telegram_token = "123456:token"
telegram_chat_id = 123456789
worktree_root = "~/.clex/worktrees"
head_branch = "main"
verification = "go test ./..."
skills = ["repo-skill"]
```

- `telegram_token`: bot token from BotFather. Secret; never paste it into issues
  or prompts.
- `telegram_chat_id`: authorized owner chat id. Every message and callback is
  checked against this id.
- `worktree_root`: root for per-issue worktrees.
- `head_branch`: trunk branch for final PRs. Defaults to `main`.
- `verification`: repo-default verification command used when an issue command is
  absent or not trusted.
- `skills`: extra repo-level skills injected alongside bundled skills.

## Providers

```toml
[providers.claude]
kind = "claude-cli"

[providers.codex]
kind = "codex-cli"

[providers.ollama]
kind = "ollama"
autodetect = true

[providers.fake]
kind = "fake"
binary = "/path/to/clex-fake-runner"
script = "/path/to/script.json"
```

- `kind`: adapter kind. Supported values are `claude-cli`, `codex-cli`,
  `ollama`, and `fake`.
- `binary`: optional executable override for CLI-backed providers. Useful for
  tests and non-standard installs.
- `script`: fake-provider script path. Ignored by real providers.
- `autodetect`: for `ollama`, discovers installed local models.

Deleting a provider block is the supported way to remove that provider. Models
that reference it are dropped with a doctor warning.

## Models

```toml
[models]
opus-4-8 = { provider = "claude", billing = "subscription" }
fable-5 = { provider = "claude", billing = "metered" }
qwen3-coder = { provider = "ollama", billing = "free" }
```

- `provider`: provider block that runs the model.
- `billing`: `subscription`, `metered`, or `free`.
- `effort`: optional default thinking level for that model.
- `fast`: optional fast-output flag where the provider supports it.

## Tiers And Routing

```toml
[tiers]
top = ["opus-4-8"]
mid = ["sonnet-5"]
local = ["qwen3-coder"]

[routing.plan]
tier = "top"
effort = "max"

[routing.build]
policy = "auto"

[routing.review]
tier = "top"

[routing.lint]
tier = "mid"

[routing.bot]
model = "codex:best"
fast = true
```

Each role chooses exactly one selector:

- `tier`: ordered model tier.
- `model`: pinned model id or runtime shorthand such as `codex:best`.
- `policy`: dynamic policy. `build` normally uses `auto`, which scores success,
  speed, and cost across local and mid-tier models.

Role-level `effort` and `fast` override model defaults.

`bot` is the Telegram chat role: free text sent to the bot runs on this model
(with the conversation resumed across messages). The init wizard defaults it to
Claude when both CLIs are installed. `/model <id>` in Telegram overrides it for
the current daemon run without touching this file.

## Budget And Caps

```toml
[budget]
confirm_over_usd = 2.00
max_usd_per_epic = 25.00

[caps.claude]
max_concurrent = 2
```

- `confirm_over_usd`: metered dispatches above this estimate wait for owner
  confirmation.
- `max_usd_per_epic`: optional hard cap that pauses the epic.
- `[caps.<provider>].max_concurrent`: per-provider runner concurrency limit.

## Update

```toml
[update]
auto = "patch"
```

`auto = "patch"` lets patch releases auto-apply after checksum verification.
Set `auto = "off"` to disable automatic self-updates.
