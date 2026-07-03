#!/bin/sh
# Fake `claude` binary for tests. It is driven entirely by files inside its
# working directory (which the adapter sets to the run's dir) so it needs NO
# inherited environment — this is important because the adapter deliberately
# strips its child's env down to an allowlist, which would filter out any
# CLEX_* control variables.
#
# Config files, all under ./.fake/ relative to the working directory:
#   ./.fake/fixture   - path to a stream-json fixture; its contents go to stdout
#   ./.fake/argv-out  - if present (even empty), each argv element is written
#                       here, one per line
#   ./.fake/env-out   - if present (even empty), the full environment is dumped
#   ./.fake/exit      - exit code to use (default 0)

cfg=".fake"

if [ -f "$cfg/argv-out" ]; then
	: >"$cfg/argv-out"
	for a in "$@"; do
		printf '%s\n' "$a" >>"$cfg/argv-out"
	done
fi

if [ -f "$cfg/env-out" ]; then
	env >"$cfg/env-out"
fi

if [ -f "$cfg/fixture" ]; then
	cat "$(cat "$cfg/fixture")"
fi

if [ -f "$cfg/exit" ]; then
	exit "$(cat "$cfg/exit")"
fi
exit 0
