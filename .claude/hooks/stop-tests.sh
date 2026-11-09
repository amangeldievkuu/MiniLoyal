#!/usr/bin/env bash
# Stop hook. Runs the short Go test suite before the agent stops.
# Exit 2 keeps the agent working with the failures in stderr.
set -u

input=$(cat)
if command -v jq >/dev/null 2>&1; then
  active=$(printf '%s' "$input" | jq -r '.stop_hook_active // false')
  [ "$active" = "true" ] && exit 0
  cwd=$(printf '%s' "$input" | jq -r '.cwd // empty')
  if [ -n "$cwd" ]; then cd "$cwd" 2>/dev/null || exit 0; fi
fi

[ -f go.mod ] || exit 0
command -v go >/dev/null 2>&1 || exit 0

out=$(make -s test-short 2>&1)
status=$?
if [ "$status" -ne 0 ]; then
  printf '%s\n' "$out" | tail -40 >&2
  printf 'make test-short failed. Fix the failures before stopping.\n' >&2
  exit 2
fi
exit 0
