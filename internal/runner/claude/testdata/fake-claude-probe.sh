#!/bin/sh
# Fake `claude` for Probe tests. It answers `--version` from CLEX_FAKE_VERSION
# and, for any other invocation (the cheap auth check), streams the fixture in
# CLEX_FAKE_FIXTURE and exits with CLEX_FAKE_EXIT. This lets a single fake stand
# in for both calls Probe makes.

for a in "$@"; do
	if [ "$a" = "--version" ]; then
		printf '%s\n' "${CLEX_FAKE_VERSION:-2.1.0 (Claude Code)}"
		exit 0
	fi
done

if [ -n "$CLEX_FAKE_FIXTURE" ]; then
	cat "$CLEX_FAKE_FIXTURE"
fi
exit "${CLEX_FAKE_EXIT:-0}"
