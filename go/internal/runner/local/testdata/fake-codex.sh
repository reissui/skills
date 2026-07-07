#!/usr/bin/env bash
# fake-codex.sh — a stand-in for the official `codex` binary used by the local
# adapter's Run test. The local adapter wraps the codex adapter, so this fake
# mirrors the one in internal/runner/codex/testdata: it records argv (letting the
# test assert that --oss reached the child) and streams a recorded JSONL fixture.
#
#   FAKE_CODEX_ARGS_FILE   if set, the full argv is written (one arg per line)
#                          here before anything else, so tests can assert argv.
#   FAKE_CODEX_STREAM      path to a JSONL fixture streamed to stdout for `exec`.
#   FAKE_CODEX_EXIT        exit code for `exec` after streaming (default 0).
set -u

if [ -n "${FAKE_CODEX_ARGS_FILE:-}" ]; then
  : >"$FAKE_CODEX_ARGS_FILE"
  for a in "$@"; do
    printf '%s\n' "$a" >>"$FAKE_CODEX_ARGS_FILE"
  done
fi

case "${1:-}" in
  exec)
    if [ -n "${FAKE_CODEX_STREAM:-}" ] && [ -f "${FAKE_CODEX_STREAM}" ]; then
      cat "${FAKE_CODEX_STREAM}"
    fi
    exit "${FAKE_CODEX_EXIT:-0}"
    ;;
  *)
    printf 'fake-codex: unexpected command: %s\n' "${1:-<none>}" >&2
    exit 64
    ;;
esac
