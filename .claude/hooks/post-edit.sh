#!/usr/bin/env bash
# PostToolUse hook for Edit|Write. Formats and checks the edited file.
# Exit 2 sends stderr back to the agent as feedback (PostToolUse cannot block).
# Missing tools never fail the hook.
set -u

input=$(cat)
command -v jq >/dev/null 2>&1 || exit 0

file=$(printf '%s' "$input" | jq -r '.tool_input.file_path // empty')
cwd=$(printf '%s' "$input" | jq -r '.cwd // empty')
[ -n "$file" ] || exit 0
if [ -n "$cwd" ]; then cd "$cwd" 2>/dev/null || exit 0; fi
[ -f "$file" ] || exit 0

case "$file" in
  */node_modules/*|*/dist/*|*/.claude/worktrees/*) exit 0 ;;
esac

fail() {
  printf '%s\n' "$1" >&2
  exit 2
}

case "$file" in
  *.go)
    command -v gofmt >/dev/null 2>&1 || exit 0
    if ! out=$(gofmt -l -w "$file" 2>&1); then
      fail "gofmt failed on $file:
$out"
    fi
    command -v go >/dev/null 2>&1 || exit 0
    dir=$(dirname "$file")
    gomod=$(cd "$dir" 2>/dev/null && go env GOMOD 2>/dev/null)
    if [ -z "$gomod" ] || [ "$gomod" = "/dev/null" ]; then exit 0; fi
    if ! out=$(go vet "$dir" 2>&1); then
      fail "go vet reported problems in $dir:
$(printf '%s\n' "$out" | tail -40)"
    fi
    ;;
  *.sql)
    case "$file" in
      */db/migrations/*|*/db/queries/*|db/migrations/*|db/queries/*)
        [ -f Makefile ] || exit 0
        grep -q '^gen-sql:' Makefile || exit 0
        if ! out=$(make gen-sql 2>&1); then
          fail "make gen-sql failed after editing $file:
$(printf '%s\n' "$out" | tail -40)"
        fi
        ;;
    esac
    ;;
  *.ts|*.tsx)
    [ -x node_modules/.bin/prettier ] || exit 0
    npx prettier --write "$file" >/dev/null 2>&1 || true
    ;;
esac
exit 0
