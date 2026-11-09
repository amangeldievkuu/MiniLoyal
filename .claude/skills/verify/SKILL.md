---
name: verify
description: Run the full local CI (format check, vet, staticcheck, vulnerability check, Go and TypeScript tests, builds) and report the result.
disable-model-invocation: true
allowed-tools: Bash(make *), Read
---

# Verify

Run the complete local check and report. Do not fix anything unless the user asks.

1. Run `make ci` from the repository root.
2. If it fails, run the failing target alone (`make lint`, `make vuln`, `make test`, `make build`) to isolate it.
3. Report:
   - Pass or fail per target.
   - For failures, the first error with file and line, and the command to reproduce.
   - Whether `make gen` left a diff (`git status --short` after `make gen` means the spec or queries changed without regeneration).
