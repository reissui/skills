#!/bin/sh
# Fake `claude` that spawns a long-lived child then blocks, so the cancellation
# test can prove the adapter kills the whole process GROUP (parent plus
# descendants) with no leaked orphan. Driven by files in the working directory
# (see fake-claude.sh) to avoid depending on the stripped child environment.
#
#   ./.fake/childpid-out - the background child's PID is written here so the
#                          test can assert it was reaped.

cfg=".fake"

sleep 300 &
child=$!

if [ -d "$cfg" ]; then
	printf '%s\n' "$child" >"$cfg/childpid-out"
fi

# Emit one line so the reader has data before it blocks, then wait so that only
# cancellation can end this process.
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-sleep","model":"m"}'
wait "$child"
