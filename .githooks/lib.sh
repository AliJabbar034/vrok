#!/bin/sh
# Shared definitions for the git hooks.
#
# The patterns live here, in one place, so the hook that blocks a push and the
# CI job that blocks a merge cannot drift apart and disagree about what a
# valid commit looks like.

# Commit types, and what each one releases. Keep in step with the table in
# CONTRIBUTING.md and the bump logic in .github/workflows/tag.yml.
COMMIT_TYPES="feat fix perf sec security revert docs test chore ci refactor build"
CONVENTIONAL_COMMIT_RE='^(feat|fix|perf|sec|security|revert|docs|test|chore|ci|refactor|build)(\([a-z0-9/-]+\))?!?: .+'

# Branch types are the same words, minus the ones that never describe a branch.
BRANCH_TYPES="feat fix perf sec docs test chore ci refactor build"
BRANCH_RE='^(feat|fix|perf|sec|security|revert|docs|test|chore|ci|refactor|build)/[a-z0-9][a-z0-9-]*$'

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
	C_RED=$(printf '\033[31m')
	C_YELLOW=$(printf '\033[33m')
	C_GREEN=$(printf '\033[32m')
	C_DIM=$(printf '\033[2m')
	C_OFF=$(printf '\033[0m')
else
	C_RED="" C_YELLOW="" C_GREEN="" C_DIM="" C_OFF=""
fi

step() { printf '%s→ %s%s\n' "$C_DIM" "$1" "$C_OFF" >&2; }
ok() { printf '%s✓ %s%s\n' "$C_GREEN" "$1" "$C_OFF" >&2; }
warn() { printf '%s! %s%s\n' "$C_YELLOW" "$1" "$C_OFF" >&2; }

# fail prints a headline and any number of detail lines, then stops the hook.
fail() {
	printf '\n%s✗ %s%s\n' "$C_RED" "$1" "$C_OFF" >&2
	shift
	for line in "$@"; do
		printf '%s\n' "$line" >&2
	done
	printf '\n' >&2
	exit 1
}

# PRETTIER_BIN is a real executable path rather than a shell function,
# because the hooks pipe file lists through xargs, which spawns a process and
# cannot see a function.
#
# A locally installed copy is required: falling back to npx would reach the
# network on every commit. `npm install` or `make fmt` puts it there.
if [ -x node_modules/.bin/prettier ]; then
	PRETTIER_BIN="$(pwd)/node_modules/.bin/prettier"
elif command -v prettier >/dev/null 2>&1; then
	PRETTIER_BIN="$(command -v prettier)"
else
	PRETTIER_BIN=""
fi

# has_prettier reports whether non-Go files can be formatted here. When they
# cannot, committing still works: CI reports anything missed.
has_prettier() { [ -n "$PRETTIER_BIN" ]; }
