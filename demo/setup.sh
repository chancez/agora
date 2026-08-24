#!/usr/bin/env bash
# Build a throwaway workspace for the agora demo: a repository with a bug that spans three files, one
# worktree per agent, an isolated database, and a settings file carrying the hooks.
#
# Nothing here touches your real channel or your global configuration. The database lives inside the demo
# directory, and the hooks are in a settings file you pass to claude with --settings rather than installed
# into ~/.claude/settings.json, so your other sessions are unaffected and there is nothing to undo but a
# single rm -rf.
#
# Usage: demo/setup.sh [directory]     (default /tmp/agora-demo)
set -euo pipefail

DEMO=${1:-/tmp/agora-demo}

if command -v agora >/dev/null 2>&1; then
  AGORA=$(command -v agora)
else
  AGORA=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/bin/agora
  [ -x "$AGORA" ] || { echo "agora is not on PATH and $AGORA does not exist. Run: mise run install" >&2; exit 1; }
fi

# The demo wires whatever agora is installed, so an older build makes an act quietly do nothing rather than
# fail. Checked here instead, since "alice never woke up" is indistinguishable from a doorbell that is not in
# that binary.
"$AGORA" doorbell --help >/dev/null 2>&1 || {
  echo "$AGORA has no doorbell command, so act six would do nothing. Run: mise run install" >&2; exit 1; }

if [ -e "$DEMO" ]; then
  echo "$DEMO already exists. Remove it first, or pass another directory:" >&2
  echo "    rm -rf $DEMO && $0 $DEMO" >&2
  exit 1
fi

mkdir -p "$DEMO"
DEMO=$(cd "$DEMO" && pwd)
REPO=$DEMO/repo
DB=$DEMO/agora.db

# A repository, because a channel is keyed on the repository root: two worktrees of it share a channel with
# no configuration, which is the situation agora is for.
mkdir -p "$REPO"
cd "$REPO"
git init -q -b main

cat > parser.go <<'GO'
package tokens

import "strings"

// Parse returns the first token of input.
func Parse(input string) string {
	fields := strings.Fields(input)
	return fields[0]
}
GO

cat > lexer.go <<'GO'
package tokens

import "strings"

// Lex returns the leading token, lowercased.
func Lex(input string) string {
	fields := strings.Fields(input)
	return strings.ToLower(fields[0])
}
GO

cat > format.go <<'GO'
package tokens

import "strings"

// Format returns input with its first token capitalised.
func Format(input string) string {
	fields := strings.Fields(input)
	return strings.ToUpper(fields[0]) + " " + strings.Join(fields[1:], " ")
}
GO

cat > README.md <<'MD'
# tokens

Three helpers that each take the first whitespace-separated field of a string.
MD

git add .
git -c user.email=demo@example.invalid -c user.name=demo commit -qm "tokens: parse, lex, and format"
git worktree add -q "$REPO/.worktrees/alice" -b alice
git worktree add -q "$REPO/.worktrees/bob" -b bob

# The hooks, in files passed to claude rather than installed globally. Absolute paths, because a hook runs
# in whatever environment the harness has and not necessarily one where agora is on PATH.
#
# One file per agent, differing only in AGORA_MEMBER. Identity would otherwise be each agent's own session,
# which is correct but unreadable: the demo is easier to follow with alice and bob than with two eight
# character session ids, and naming them here makes it behave the same however you launch it.
#
# The guard is here with --ask, which puts an overlap to you rather than refusing outright: an overlap in
# files is not proof of an overlap in work, and you can tell the difference in a second.
#
# The doorbell is here because the demo is where it is worth watching: both agents sit at a prompt between
# acts, which is exactly the state nothing else in agora reaches. --wait blocks until something addressed to
# that agent arrives, and asyncRewake is what lets its exit 2 wake a session nobody is typing into.
for agent in alice bob; do
  cat > "$DEMO/settings-$agent.json" <<JSON
{
  "env": {
    "AGORA_DB": "$DB",
    "AGORA_MEMBER": "$agent"
  },
  "hooks": {
    "SessionStart": [
      { "hooks": [ { "type": "command", "command": "$AGORA inject" } ] }
    ],
    "UserPromptSubmit": [
      { "hooks": [ { "type": "command", "command": "$AGORA inject" } ] }
    ],
    "PreToolUse": [
      { "matcher": "Edit|Write|MultiEdit",
        "hooks": [ { "type": "command", "command": "$AGORA guard --ask", "timeout": 5 } ] }
    ],
    "Stop": [
      { "hooks": [ { "type": "command", "command": "$AGORA doorbell --wait 30m",
                     "asyncRewake": true, "timeout": 1800 } ] }
    ]
  }
}
JSON
done

# A helper so every shell in the demo points at the demo database rather than yours.
cat > "$DEMO/env.sh" <<SH
# source this in any shell you want pointed at the demo channel
export AGORA_DB="$DB"
export PATH="$(dirname "$AGORA"):\$PATH"
SH

cat <<OUT
Demo workspace ready.

  workspace   $DEMO
  repository  $REPO
  database    $DB   (empty until somebody posts)
  settings    $DEMO/settings-alice.json, $DEMO/settings-bob.json
  binary      $AGORA

Check the isolation before anything else. It must name the demo database rather than the one under
\$XDG_DATA_HOME, and say the channel came from the repository:

  (cd $REPO/.worktrees/alice && AGORA_DB=$DB $AGORA config --text)

Then follow demo/README.md. The short version, three terminals:

  1  cd $REPO/.worktrees/alice && claude --settings $DEMO/settings-alice.json
  2  cd $REPO/.worktrees/bob   && claude --settings $DEMO/settings-bob.json
  3  source $DEMO/env.sh && cd $REPO && agora tui

When you are done, the whole thing goes away with:

  rm -rf $DEMO
OUT
