#!/usr/bin/env bash
# fake-codex.sh — a stand-in for the official `codex` binary used by the codex
# adapter tests. It emits recorded JSONL fixtures and simulates auth/hang states.
# Behavior is driven entirely by environment variables the test sets, so a single
# script covers every scenario without a live CLI.
#
#   FAKE_CODEX_ARGS_FILE   if set, the full argv is written (one arg per line)
#                          here before anything else, so tests can assert argv.
#   FAKE_CODEX_STREAM      path to a JSONL fixture streamed to stdout for `exec`.
#   FAKE_CODEX_VERSION     version string printed for `--version` (default given).
#   FAKE_CODEX_LOGIN_FAIL  if set to 1, `login status` exits 1 (unauthenticated).
#   FAKE_CODEX_HANG        if set to 1, `exec` sleeps forever (cancellation test).
#   FAKE_CODEX_EXIT        exit code for `exec` after streaming (default 0).
#   FAKE_CODEX_ENV_FILE    if set, the child's environment is dumped here.
set -u

if [ -n "${FAKE_CODEX_ARGS_FILE:-}" ]; then
  : >"$FAKE_CODEX_ARGS_FILE"
  for a in "$@"; do
    printf '%s\n' "$a" >>"$FAKE_CODEX_ARGS_FILE"
  done
fi

if [ -n "${FAKE_CODEX_ENV_FILE:-}" ]; then
  env >"$FAKE_CODEX_ENV_FILE"
fi

case "${1:-}" in
  --version)
    printf 'codex-cli %s\n' "${FAKE_CODEX_VERSION:-0.136.0}"
    exit 0
    ;;
  login)
    # `login status`
    if [ "${FAKE_CODEX_LOGIN_FAIL:-0}" = "1" ]; then
      printf 'Not logged in.\n' >&2
      exit 1
    fi
    printf 'Logged in.\n'
    exit 0
    ;;
  exec)
    if [ "${FAKE_CODEX_HANG:-0}" = "1" ]; then
      # Emit the thread id so a session is captured, then hang until killed.
      printf '{"type":"thread.started","thread_id":"hang-0000"}\n'
      # Sleep in a way that a SIGKILL to the process group terminates promptly.
      while true; do sleep 1; done
    fi
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
