# Fake Runner

`cmd/clex-fake-runner` is a deterministic replacement for model CLIs in tests.
It emits the same newline-delimited `core.Event` JSON protocol that real runner
adapters normalize from Claude/Codex streams.

## Run

```sh
clex-fake-runner -script ./script.json
```

The script path may also come from `CLEX_FAKE_SCRIPT`.

## Event Protocol

Each stdout line is a JSON object:

```json
{"type":"text","text":"working"}
{"type":"usage","tokens":{"in":100,"out":20}}
{"type":"result","session_id":"sess-1"}
```

Supported event types match `internal/core.EventType`: `text`, `tool_use`,
`usage`, `result`, and `error`.

## Script Format

```json
{
  "delay_ms": 0,
  "session_id": "sess-abc",
  "exit_code": 0,
  "writes": [
    {"path": "clex_built_42.txt", "content": "built\n"}
  ],
  "commit": true,
  "commit_message": "clex build #42",
  "events": [
    {"type": "text", "text": "Implemented."},
    {"type": "usage", "in": 1000, "out": 20},
    {"type": "result"}
  ]
}
```

- `delay_ms`: sleep before each event, useful for proving parallel sessions
  overlap.
- `session_id`: inherited by a `result` event that omits its own session id.
- `exit_code`: process exit code after emitting events.
- `writes`: files materialized relative to the runner working directory.
- `commit`: when true, runs `git add -A && git commit` in the working directory.
- `commit_message`: commit subject used when `commit` is true.
- `events`: emitted in order. If omitted, the runner emits a text line and a
  terminal result.

## Config Provider

```toml
[providers.fake]
kind = "fake"
binary = "/absolute/path/to/clex-fake-runner"
script = "/absolute/path/to/script.json"

[models]
fake-build = { provider = "fake", billing = "subscription" }

[tiers]
mid = ["fake-build"]

[routing.build]
tier = "mid"
```

The fake provider is for deterministic integration tests. It never reaches the
network; the configured script is the complete source of behavior.
