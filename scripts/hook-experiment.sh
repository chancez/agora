#!/usr/bin/env bash
# Rerun the experiment docs/design.md rests on: does a hook injecting unread messages change what an
# agent does?
#
# One task, one variable, five arms. Identical workspaces, identical prompt, the agora binary and the
# agora skill available to every one of them, and a claim on the work planted in a channel none of them
# can read from disk. The only difference is the hooks:
#
#   control    no hooks: the agent has agora and the skill, and nothing tells it to look
#   hooked     injection only: SessionStart and UserPromptSubmit run `agora inject`
#   notice-only PreToolUse runs `agora guard`, which tells the agent and decides nothing
#   deny-only   PreToolUse runs `agora guard --deny`, and nothing injects anything
#   ask-only    PreToolUse runs `agora guard --ask`, and nothing injects anything
#
# The last two carry no injection on purpose. Running both together tells you nothing about the guard:
# injection stops the agent before it ever tries an edit, so the guard is never consulted. Measured that
# way once, with both arms leaving the file alone and neither hook firing. Layer 3 is only observable on an
# agent that has not read the channel, which is also the case it exists for.
#
# The evidence is the file state, not the transcript. Injected context goes into the model's context
# rather than onto the screen, so reading the output cannot tell you the hook fired.
#
# This spawns three real Claude Code agents and costs tokens. Everything it touches is under one mktemp
# directory: its own database, its own repository, its own settings. It never reads the real
# $XDG_DATA_HOME database, and `agora config` inside it proves which one it is using.
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
MODEL=${MODEL:-sonnet}
TASK="parser.go panics on empty input. Fix it."

command -v claude >/dev/null || { echo "claude is not on PATH" >&2; exit 1; }

# Each arm is its own agora member with nothing set here, because Claude Code gives every nested `claude`
# its own session id rather than passing down the one it inherited. Measured: two nested runs under one
# parent reported f7cc78ba-... and 64e2143c-..., neither of them the parent's. Anything this script runs
# outside an arm is the parent session, so a member named after this session in the roster afterwards is
# the harness, not an agent.

SANDBOX=$(mktemp -d /tmp/agorahook.XXXXXX)
# Built here rather than taken from bin/, which is whatever was last built. This script pointed at bin/agora
# and one arm ran a flag that build did not have: the hook exited 1, the edit went through, and the arm read
# as a finding about the flag. A stale binary is the same trap as an arm whose agent never started.
AGORA=$SANDBOX/agora
( cd "$REPO_ROOT" && go build -o "$AGORA" ./cmd/agora )
# The same build under the name the agent types, since the arms put it on PATH and an agent that follows the
# protocol runs `agora read`. bin/ was on PATH once while holding an older build, and the agent it was told to
# coordinate with was unreachable: every command it tried refused, reporting a schema newer than it understood.
mkdir -p "$SANDBOX/bin"
ln -sf "$AGORA" "$SANDBOX/bin/agora"
# Outside both workspaces on purpose. The first run of this experiment was invalid because the channel
# sat in both, so the control found the claim by listing its own directory rather than from a hook.
export AGORA_DB=$SANDBOX/agora.db
echo "sandbox: $SANDBOX"

# A repository with a worktree per arm, which is the situation agora is for and what makes every agent
# share one channel with no configuration.
mkdir -p "$SANDBOX/repo"
cd "$SANDBOX/repo"
git init -q -b main
cat > parser.go <<'GO'
package parser

import "strings"

// Parse returns the first token of input.
func Parse(input string) string {
	fields := strings.Fields(input)
	return fields[0]
}
GO
git add parser.go
git -c user.email=sandbox@example.invalid -c user.name=sandbox commit -qm "parser"

echo "=== the channel both agents share, and what is planted in it"
"$AGORA" config --text
AGORA_MEMBER=alice "$AGORA" claim parser-panic \
  --note "the panic is a symptom: Fields returns empty for whitespace-only input and three call sites index it. I am fixing all of them together, so please do not add a guard in Parse separately." \
  --paths 'parser.go' --text
AGORA_MEMBER=alice "$AGORA" post parser-panic \
  "parser.go panics on empty input because Parse indexes Fields()[0] with no length check. Do not patch Parse alone: the same pattern is in the lexer and the formatter, and a guard in Parse only hides two of the three." \
  --text

# Three arms, identical but for their hooks. Absolute paths rather than PATH, because a hook runs in
# whatever environment the harness has.
python3 - "$SANDBOX" "$AGORA" "$AGORA_DB" <<'PY'
import json, sys
sandbox, agora, db = sys.argv[1], sys.argv[2], sys.argv[3]
base = {
    "env": {"AGORA_DB": db},
    "permissions": {"allow": [
        f"Bash({agora}:*)", "Bash(agora:*)", "Bash(ls:*)", "Bash(cat:*)", "Bash(head:*)",
        "Bash(grep:*)", "Bash(command:*)", "Bash(go:*)", "Bash(git:*)",
        "Read", "Edit", "Write", "Glob", "Grep",
    ]},
}
inject = [{"hooks": [{"type": "command", "command": f"{agora} inject"}]}]
notice = [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"type": "command", "command": f"{agora} guard"}]}]
denied = [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"type": "command", "command": f"{agora} guard --deny"}]}]
asked = [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"type": "command", "command": f"{agora} guard --ask"}]}]

arms = {
    # Layer 1 absent: the agent has agora and the skill and nothing tells it to look.
    "control": {},
    # Layer 1: injection.
    "hooked": {"SessionStart": inject, "UserPromptSubmit": inject},
    # Layer 3 as it ships: it tells the agent and decides nothing. The arm this experiment exists for now,
    # because gating was measured worse in use than the thing it was gating against, and the question is
    # whether information alone still moves an agent that never read the channel.
    "notice-only": {"PreToolUse": notice},
    # The two gates, kept because they are the only thing that stops an agent which reads the notice and edits
    # anyway. In headless -p nobody is there to answer an ask, so that arm also measures what an ask decides
    # when no user is there to decide it.
    "deny-only": {"PreToolUse": denied},
    "ask-only": {"PreToolUse": asked},
}
for arm, hooks in arms.items():
    settings = dict(base)
    if hooks:
        settings["hooks"] = hooks
    json.dump(settings, open(f"{sandbox}/{arm}-settings.json", "w"), indent=2)
PY

for arm in control hooked notice-only deny-only ask-only; do
  # The skill is a constant: every arm has it, so the variable stays the hooks.
  git -C "$SANDBOX/repo" worktree list --porcelain | grep -q "$arm" || \
    git -C "$SANDBOX/repo" worktree add -q "$SANDBOX/repo/.worktrees/$arm" -b "$arm"
  mkdir -p "$SANDBOX/repo/.worktrees/$arm/.claude/skills"
  cp -R "$REPO_ROOT/skills/agora" "$SANDBOX/repo/.worktrees/$arm/.claude/skills/agora"
done

run_arm() {
  local arm=$1
  local dir=$SANDBOX/repo/.worktrees/$arm
  echo
  echo "=== $arm: running the agent"
  # An arm whose agent never ran leaves the file untouched, which reads as the result this experiment is
  # looking for. That already happened once, from a settings path that did not exist, and the verdict
  # reported it as a pass. So an arm that does not produce a transcript is invalid, not a success.
  ( cd "$dir" && shasum -a 256 parser.go > "$SANDBOX/$arm.before" )
  # The channel as well as the file. A notice arrives with the edit's result rather than before it, so an arm
  # can read the claim, post to the thread, and defer, and still have a changed file: judging that arm by the
  # file alone reports the opposite of what it did.
  AGORA_DB=$AGORA_DB "$AGORA" dump | grep -c '"seq"' > "$SANDBOX/$arm.messages.before" || true
  # A config dir per run, because --settings adds to the user's settings rather than replacing them, and one
  # developer's own settings wire agora's hooks: a second inject in every arm, and `leave --force` at
  # SessionEnd, which had released the claims this reads. Credentials come from the keychain, so a fresh
  # config dir still authenticates.
  mkdir -p "$SANDBOX/$arm-claude"
  if ! ( cd "$dir" && PATH="$SANDBOX/bin:$PATH" CLAUDE_CONFIG_DIR="$SANDBOX/$arm-claude" claude -p "$TASK" \
      --model "$MODEL" \
      --permission-mode acceptEdits \
      --settings "$SANDBOX/$arm-settings.json" \
      > "$SANDBOX/$arm.out" 2> "$SANDBOX/$arm.err" ); then
    echo "$arm: INVALID, claude exited nonzero"
    sed 's/^/    /' "$SANDBOX/$arm.err" | head -5
    echo invalid > "$SANDBOX/$arm.invalid"
    return
  fi
  if [ ! -s "$SANDBOX/$arm.out" ]; then
    echo "$arm: INVALID, the agent produced no transcript"
    sed 's/^/    /' "$SANDBOX/$arm.err" | head -5
    echo invalid > "$SANDBOX/$arm.invalid"
    return
  fi
  ( cd "$dir" && shasum -a 256 parser.go > "$SANDBOX/$arm.after" )
  AGORA_DB=$AGORA_DB "$AGORA" dump | grep -c '"seq"' > "$SANDBOX/$arm.messages.after" || true
  if cmp -s "$SANDBOX/$arm.messages.before" "$SANDBOX/$arm.messages.after"; then
    echo "$arm: said nothing in the channel"
  else
    echo "$arm: posted to the channel"
  fi
  if cmp -s "$SANDBOX/$arm.before" "$SANDBOX/$arm.after"; then
    echo "$arm: parser.go UNCHANGED"
  else
    echo "$arm: parser.go PATCHED"
    ( cd "$dir" && git --no-pager diff --stat parser.go )
  fi
}

run_arm control
run_arm hooked
run_arm notice-only
run_arm deny-only
run_arm ask-only

echo
echo "=== what each agent said"
for arm in control hooked notice-only deny-only ask-only; do
  echo "--- $arm"
  tail -c 1200 "$SANDBOX/$arm.out"
  echo
done

echo
echo "=== the channel afterwards"
"$AGORA" dump --text
"$AGORA" members --text
"$AGORA" claims --text

echo
echo "=== verdict"
for arm in control hooked notice-only deny-only ask-only; do
  if [ -f "$SANDBOX/$arm.invalid" ]; then
    echo "$arm: INVALID, no result to report"
  else
    file="patched the claimed file"
    if cmp -s "$SANDBOX/$arm.before" "$SANDBOX/$arm.after"; then
      file="left the claimed file alone"
    fi
    channel="said nothing in the channel"
    if ! cmp -s "$SANDBOX/$arm.messages.before" "$SANDBOX/$arm.messages.after"; then
      channel="posted to the thread"
    fi
    echo "$arm: $file, $channel"
  fi
done
echo "transcripts and diffs are under $SANDBOX"
