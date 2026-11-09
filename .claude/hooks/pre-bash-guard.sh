#!/usr/bin/env bash
# PreToolUse hook for Bash. Blocks a short list of destructive commands.
# Exit 2 blocks the command and shows stderr to the agent.
set -u

input=$(cat)
command -v jq >/dev/null 2>&1 || exit 0
cmd=$(printf '%s' "$input" | jq -r '.tool_input.command // empty')
[ -n "$cmd" ] || exit 0
cwd=$(printf '%s' "$input" | jq -r '.cwd // empty')

deny() {
  printf 'Blocked by .claude/hooks/pre-bash-guard.sh: %s\n' "$1" >&2
  exit 2
}
has() { printf '%s' "$cmd" | grep -Eq "$1"; }
hasi() { printf '%s' "$cmd" | grep -Eiq "$1"; }

# Recursive rm of /, ~, ., $HOME or a bare *.
if has '(^|[;&|[:space:]])rm([[:space:]]+-[A-Za-z-]+)*[[:space:]]+(-[A-Za-z]*[rR][A-Za-z]*|--recursive)([[:space:]]+-[A-Za-z-]+)*[[:space:]]+("?\$HOME"?|~|/|\.|\*)/?([[:space:]]|$)'; then
  deny "recursive rm of /, ~, . or * is not allowed"
fi

# Force push to main or master, or a force push with no explicit refspec.
if has 'git[[:space:]]+push[^;&|]*([[:space:]]-f([[:space:]]|$)|--force)'; then
  seg=$(printf '%s' "$cmd" | grep -Eo 'git[[:space:]]+push[^;&|]*' | head -1)
  nonflags=0
  for w in $seg; do
    case "$w" in git|push|-*) ;; *) nonflags=$((nonflags + 1)) ;; esac
  done
  if [ "$nonflags" -lt 2 ] || printf '%s' "$seg" | grep -Eq '(^|[[:space:]:+])(main|master)([[:space:]]|$)'; then
    deny "force push to main is not allowed"
  fi
fi

# git reset --hard while on main or master.
if has 'git[[:space:]]+reset[^;&|]*--hard'; then
  branch=""
  if [ -n "$cwd" ]; then
    branch=$(git -C "$cwd" rev-parse --abbrev-ref HEAD 2>/dev/null || true)
  fi
  case "$branch" in
    main|master) deny "git reset --hard on $branch is not allowed; use a branch or git stash" ;;
  esac
fi

# Destructive SQL.
if hasi 'drop[[:space:]]+database'; then
  deny "DROP DATABASE is not allowed from the agent"
fi
if hasi 'drop[[:space:]]+table' && ! has '_test'; then
  deny "DROP TABLE is only allowed against a *_test database"
fi

exit 0
