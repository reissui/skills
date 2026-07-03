#!/usr/bin/env bash
# fake-ollama.sh — a stand-in for the official `ollama` binary used by the local
# adapter tests. It emits a recorded `ollama list` fixture and can simulate the
# daemon being down. Behavior is driven entirely by environment variables the
# test sets, so a single script covers every scenario without a live Ollama.
#
#   FAKE_OLLAMA_ARGS_FILE  if set, the full argv is written (one arg per line)
#                          here before anything else, so tests can assert argv.
#   FAKE_OLLAMA_LIST       path to a fixture printed verbatim for `list`.
#   FAKE_OLLAMA_LIST_FAIL  if set to 1, `list` exits 1 (daemon down / no ollama).
set -u

if [ -n "${FAKE_OLLAMA_ARGS_FILE:-}" ]; then
  : >"$FAKE_OLLAMA_ARGS_FILE"
  for a in "$@"; do
    printf '%s\n' "$a" >>"$FAKE_OLLAMA_ARGS_FILE"
  done
fi

case "${1:-}" in
  list)
    if [ "${FAKE_OLLAMA_LIST_FAIL:-0}" = "1" ]; then
      printf 'Error: could not connect to ollama app, is it running?\n' >&2
      exit 1
    fi
    if [ -n "${FAKE_OLLAMA_LIST:-}" ] && [ -f "${FAKE_OLLAMA_LIST}" ]; then
      cat "${FAKE_OLLAMA_LIST}"
    fi
    exit 0
    ;;
  *)
    printf 'fake-ollama: unexpected command: %s\n' "${1:-<none>}" >&2
    exit 64
    ;;
esac
