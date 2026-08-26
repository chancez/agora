#!/usr/bin/env bash
# The Codex arm of scripts/hook-experiment.sh: does agora's wiring change what a Codex agent does?
#
# Same task, same plant, same judgement as the Claude Code version, so the numbers in docs/design.md can be
# read side by side. Four arms, identical but for their hooks:
#
#   control      no hooks: the agent has agora and the skill, and nothing tells it to look
#   hooked       injection only: SessionStart and UserPromptSubmit run `agora inject`
#   notice-only  PreToolUse runs `agora guard`, which tells the agent and decides nothing
#   deny-only    PreToolUse runs `agora guard --deny`, and nothing injects anything
#   bare         no skill and no hooks: an agent with no protocol and no reason to look
#   guard-bare   no skill, PreToolUse runs `agora guard`
#   ring-control injection, and a message addressed to the agent posted from inside its turn
#   ring         the same, plus `agora doorbell` on Stop
#   wake         Codex's half of the doorbell on its own: a Stop hook that exits 2 once
#
# The ring pair is the doorbell, with the plant posted by a PostToolUse hook so that it arrives *during* the
# turn and injection cannot have carried it. It did not separate the arms, and the reason is the finding above:
# a Codex agent given the skill keeps reading the channel, so it finds a message posted mid-turn by itself and
# the doorbell has nothing left to ring about. Kept because the null is worth keeping, and because the arms do
# show `agora doorbell` staying correctly silent about a message the agent had already answered.
#
# So the wake arm measures the mechanism rather than the judgement, with agora out of it: a Stop hook that writes
# one instruction to stderr and exits 2, once. If Codex continues the turn and the model acts on that text, exit
# 2 is a wake there, which is the whole of what the doorbell needs from a harness.
#
# There is no ask-only arm: Codex rejects permissionDecision "ask" outright.
#
# The last two exist because the first four could not measure the guard. Given the skill, every Codex arm read
# the channel before touching anything, the control included, so no arm ever attempted an edit and the guard was
# never consulted. That is trap 4 from AGENTS.md: an arm doing the right thing is not a null result. The guard is
# only observable on an agent that has not read the channel, which is also the case it exists for, so those arms
# drop the skill rather than adding a hook.
#
# The evidence is the file on disk and the channel afterwards, not the transcript, because injected context
# goes into the model's context rather than onto the screen. Every arm also keeps what its hooks were handed
# and what they emitted, under $SANDBOX/<arm>/log, which is how you tell a hook that stayed quiet from one
# that never ran.
#
# Three things this needs that the Claude Code version does not:
#
#   - A CODEX_HOME per arm, with a copy of ~/.codex/auth.json. Codex reads credentials from CODEX_HOME
#     rather than a keychain, and a copy cannot refresh: a refresh token is single use, so if the copy
#     refreshes, the real login stops working and has to be redone. Copy it fresh, and run this soon after.
#   - --dangerously-bypass-hook-trust. Codex will not run a hook that has not been trusted through /hooks,
#     and trust is keyed on the hook's hash, so every edit to a command needs trusting again. An untrusted
#     hook does not run and says nothing about it, which is the shape of the result this measures.
#   - --add-dir for the channel, because the database is deliberately outside every workspace and a
#     workspace-write sandbox cannot write to it otherwise. Same reason as the Claude Code version: the
#     first run of that experiment was invalid because the channel sat inside the workspaces, so the control
#     found the claim by listing its own directory rather than from a hook.
#   - A throwaway $HOME. Codex reads $HOME/.agents/AGENTS.md and $HOME/.agents/skills whatever CODEX_HOME
#     says, and this developer's own AGENTS.md tells every agent to coordinate through agora. With the real
#     $HOME the control read that, ran `agora threads --unread` unprompted, and left the file alone: a
#     control that reaches the information another way measures nothing, and it looked like a result.
#
# This spawns real Codex sessions and costs tokens. Everything it touches is under one mktemp directory.
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TASK="parser.go panics on empty input. Fix it."
ARMS=${ARMS:-"control hooked notice-only deny-only bare guard-bare ring-control ring wake"}
# Codex runs a tool call as a *login* shell, which re-reads the user's profile and rebuilds PATH, so
# prepending the arm's binary before spawning codex does not reach the tool the agent uses. This script's
# first two runs measured the installed agora instead: a different build, which resolved a different member,
# and the run read as a finding about Codex identity rather than about the harness. Setting SHELL does not
# help either, because Codex takes the login shell from the system rather than from the environment.
#
# ZDOTDIR does, for zsh: startup files are read from there instead of the user's, so the arm gets a profile
# that prepends its own bin and nothing else. The preflight check below is what makes this checkable rather
# than assumed, and it is the check whose absence produced the two invalid runs.
LOGIN_SHELL=${LOGIN_SHELL:-${SHELL:-/bin/sh}}

command -v codex >/dev/null || { echo "codex is not on PATH" >&2; exit 1; }
[ -f "$HOME/.codex/auth.json" ] || { echo "no ~/.codex/auth.json: run codex login first" >&2; exit 1; }

SANDBOX=$(mktemp -d /tmp/agoracodex.XXXXXX)
# Built here rather than taken from bin/, which is whatever was last built. The Claude Code version pointed
# at bin/agora once and produced two invalid runs: an arm ran a flag that build did not have, and later an
# agent found every agora command refusing a schema newer than the one on its PATH understood.
AGORA=$SANDBOX/agora
( cd "$REPO_ROOT" && go build -o "$AGORA" ./cmd/agora )
mkdir -p "$SANDBOX/bin" "$SANDBOX/channel"
ln -sf "$AGORA" "$SANDBOX/bin/agora"
# Outside every workspace, in a directory of its own so one --add-dir makes the channel writable without
# making another arm's worktree writable.
export AGORA_DB=$SANDBOX/channel/agora.db
echo "sandbox: $SANDBOX"
"$AGORA" config --text

# The check that would have caught the first invalid run: ask the shell codex will use which agora an agent
# typing `agora` gets. An arm running some other build measures that build, and the failure looks like a
# finding, since the installed one resolves a different member and knows nothing of this branch's flags.
mkdir -p "$SANDBOX/zdotdir"
for f in .zshenv .zprofile .zshrc; do
  printf 'export PATH=%s/bin:$PATH\n' "$SANDBOX" > "$SANDBOX/zdotdir/$f"
done
export ZDOTDIR=$SANDBOX/zdotdir
# The throwaway home, empty on purpose: no .agents, no global instructions, nothing that mentions agora.
mkdir -p "$SANDBOX/fakehome"
on_path=$(PATH="$SANDBOX/bin:$PATH" "$LOGIN_SHELL" -lc 'command -v agora' || true)
if [ "$on_path" != "$SANDBOX/bin/agora" ]; then
  echo "an agent in this run would get \"$on_path\", not $SANDBOX/bin/agora" >&2
  echo "$LOGIN_SHELL -l rebuilds PATH from a profile ZDOTDIR did not replace. Set LOGIN_SHELL, or install this build." >&2
  exit 1
fi
echo "agora an arm resolves: $on_path"

# A repository with a worktree per arm, which is the situation agora is for and what makes every agent share
# one channel with no configuration.
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
# No AGENTS.md, deliberately. An earlier run had one saying agora was on PATH, and the control read that as a
# reason to check the channel: it ran `agora threads --unread` unprompted and left the file alone, which is the
# hooked arm's result arrived at without a hook. What every arm has is the skill and the binary, which is what
# the Claude Code version gives its arms, so the variable stays the hooks.
git add parser.go
git -c user.email=sandbox@example.invalid -c user.name=sandbox commit -qm "parser"

echo
echo "=== the channel every arm shares, and what is planted in it"
AGORA_MEMBER=alice "$AGORA" claim parser-panic \
  --note "the panic is a symptom: Fields returns empty for whitespace-only input and three call sites index it. I am fixing all of them together, so please do not add a guard in Parse separately." \
  --paths 'parser.go' --text
AGORA_MEMBER=alice "$AGORA" post parser-panic \
  "parser.go panics on empty input because Parse indexes Fields()[0] with no length check. Do not patch Parse alone: the same pattern is in the lexer and the formatter, and a guard in Parse only hides two of the three." \
  --text

# The wrapper is instrumentation and nothing else: it records what a hook was given and what it emitted, and
# forwards both unchanged, exit code included. Without it a quiet hook and a hook that never ran look the same.
cat > "$SANDBOX/hook.sh" <<EOF
#!/bin/sh
set -u
log=\$1; shift
# Set here rather than inherited, because nothing promises a harness passes its environment to a hook, and
# \$AGORA_AGENT is what keeps a hook and the session it is about resolving one member instead of two.
export AGORA_AGENT=codex AGORA_DB=$AGORA_DB PATH=$SANDBOX/bin:\$PATH
payload=\$(cat)
printf '%s\n' "\$payload" >> "\$log.in"
err=\$(mktemp)
out=\$(printf '%s' "\$payload" | "\$@" 2>"\$err"); code=\$?
printf '%s\n' "\$code" >> "\$log.code"
if [ -s "\$err" ]; then cat "\$err" >> "\$log.err"; cat "\$err" >&2; fi
if [ -n "\$out" ]; then printf '%s\n' "\$out" >> "\$log.out"; printf '%s' "\$out"; fi
exit \$code
EOF
chmod +x "$SANDBOX/hook.sh"

# Posts one message addressed to the agent, from inside its turn, and only once. Addressed rather than merely
# unread, because that is what a doorbell rings for: a wake costs a turn. The name is shortened the way agora
# shortens it, from the end of the thread id.
cat > "$SANDBOX/plant.sh" <<EOF
#!/bin/sh
set -u
marker=\$1
[ -f "\$marker" ] && exit 0
payload=\$(cat)
member=\$(printf '%s' "\$payload" | python3 -c 'import hashlib,json,sys; print("codex-" + hashlib.sha256(json.load(sys.stdin)["session_id"].encode()).hexdigest()[:8])')
: > "\$marker"
AGORA_DB=$AGORA_DB AGORA_MEMBER=alice $AGORA post lexer-panic \\
  "@\$member I am about to change lex.go for the same empty-input bug. Does your parser fix cover it, or should I?" \\
  >/dev/null 2>&1
exit 0
EOF
chmod +x "$SANDBOX/plant.sh"

# Not agora, on purpose: the smallest thing that tests what `agora doorbell` depends on. Once, because exit 2
# keeps a turn alive and a hook that always did it would never let the session finish, which is also why the
# doorbell reads stop_hook_active.
cat > "$SANDBOX/wake.sh" <<EOF
#!/bin/sh
set -u
marker=\$1
cat >/dev/null
[ -f "\$marker" ] && exit 0
: > "\$marker"
echo "A message arrived for you while you were working: reply with exactly the word PINEAPPLE." >&2
exit 2
EOF
chmod +x "$SANDBOX/wake.sh"

# Each arm's hooks, in the format docs/setup.md documents, with the wrapper in front of each command.
arm_config() {
  local arm=$1 home=$2 log=$3
  local hook="$SANDBOX/hook.sh"
  case $arm in
    control) : ;;
    hooked)
      cat >> "$home/config.toml" <<EOF
[[hooks.SessionStart]]
[[hooks.SessionStart.hooks]]
type = "command"
command = "$hook $log/session-start $AGORA inject"

[[hooks.UserPromptSubmit]]
[[hooks.UserPromptSubmit.hooks]]
type = "command"
command = "$hook $log/user-prompt $AGORA inject"
EOF
      ;;
    wake)
      cat >> "$home/config.toml" <<EOF
[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "command"
command = "$SANDBOX/wake.sh $SANDBOX/$arm.woken"
EOF
      ;;
    ring-control|ring)
      cat >> "$home/config.toml" <<EOF
[[hooks.SessionStart]]
[[hooks.SessionStart.hooks]]
type = "command"
command = "$hook $log/session-start $AGORA inject"

[[hooks.UserPromptSubmit]]
[[hooks.UserPromptSubmit.hooks]]
type = "command"
command = "$hook $log/user-prompt $AGORA inject"

[[hooks.PostToolUse]]
[[hooks.PostToolUse.hooks]]
type = "command"
command = "$SANDBOX/plant.sh $SANDBOX/$arm.planted"
EOF
      # The variable, and the whole of it: exit 2 on Stop, with the wake on stderr.
      if [ "$arm" = ring ]; then
        cat >> "$home/config.toml" <<EOF

[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "command"
command = "$hook $log/stop $AGORA doorbell"
EOF
      fi
      ;;
    notice-only|deny-only|guard-bare)
      local flags=""
      [ "$arm" = deny-only ] && flags=" --deny"
      # apply_patch is the tool name in the payload; Edit and Write are matcher aliases for it.
      cat >> "$home/config.toml" <<EOF
[[hooks.PreToolUse]]
matcher = "apply_patch|Edit|Write"
[[hooks.PreToolUse.hooks]]
type = "command"
command = "$hook $log/pre-tool $AGORA guard$flags"
timeout = 5
EOF
      ;;
  esac
  # Every arm leaves the roster the same way, so the variable stays the layer being measured.
  cat >> "$home/config.toml" <<EOF

[[hooks.SessionEnd]]
[[hooks.SessionEnd.hooks]]
type = "command"
command = "$hook $log/session-end $AGORA leave --force"
timeout = 3
EOF
}

run_arm() {
  local arm=$1
  local dir=$SANDBOX/repo/.worktrees/$arm
  local home=$SANDBOX/$arm/home
  local log=$SANDBOX/$arm/log
  echo
  echo "=== $arm"
  git -C "$SANDBOX/repo" worktree add -q "$dir" -b "$arm"
  mkdir -p "$home/skills" "$log"
  # The skill for every arm but the bare pair, which is what makes the guard observable at all.
  case $arm in
    bare|guard-bare) : ;;
    *) cp -R "$REPO_ROOT/skills/agora" "$home/skills/agora" ;;
  esac
  cp "$HOME/.codex/auth.json" "$home/auth.json"
  : > "$home/config.toml"
  arm_config "$arm" "$home" "$log"

  ( cd "$dir" && shasum -a 256 parser.go > "$SANDBOX/$arm.before" )
  # The channel as well as the file. A notice arrives with the edit's result rather than before it, so an arm
  # can edit, read the claim, revert, and post, and judging it by the file alone reports the opposite.
  "$AGORA" dump | grep -c '"seq"' > "$SANDBOX/$arm.messages.before" || true

  local prompt=$TASK
  [ "$arm" = wake ] && prompt="Reply with the single word ready."
  if ! ( cd "$dir" && CODEX_HOME=$home HOME=$SANDBOX/fakehome PATH="$SANDBOX/bin:$PATH" ZDOTDIR="$SANDBOX/zdotdir" \
      codex exec --json --dangerously-bypass-hook-trust \
        -s workspace-write --add-dir "$SANDBOX/channel" -C "$dir" "$prompt" \
      > "$SANDBOX/$arm.out" 2> "$SANDBOX/$arm.err" ); then
    echo "$arm: INVALID, codex exited nonzero"
    sed 's/^/    /' "$SANDBOX/$arm.err" | head -5
    echo invalid > "$SANDBOX/$arm.invalid"
    return
  fi
  if [ ! -s "$SANDBOX/$arm.out" ]; then
    # An arm whose agent never ran leaves the file untouched, which reads as the result being looked for.
    echo "$arm: INVALID, the agent produced no transcript"
    sed 's/^/    /' "$SANDBOX/$arm.err" | head -5
    echo invalid > "$SANDBOX/$arm.invalid"
    return
  fi

  ( cd "$dir" && shasum -a 256 parser.go > "$SANDBOX/$arm.after" )
  "$AGORA" dump | grep -c '"seq"' > "$SANDBOX/$arm.messages.after" || true
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
  # The identity check, which is the one this script got wrong the first time. A hook resolves the member from
  # the event; the agent's own commands resolve it from the environment. If they disagree, one session is two
  # members and the roster, the cursors and the claims are split between them.
  python3 - "$SANDBOX" "$arm" <<'PY'
import glob, hashlib, json, os, subprocess, sys
sandbox, arm = sys.argv[1], sys.argv[2]
sessions = set()
for path in glob.glob(f"{sandbox}/{arm}/log/*.in"):
    for line in open(path):
        line = line.strip()
        if line:
            sessions.add(json.loads(line)["session_id"])
# The same rule agora uses, and computed here rather than read out of agora so that this check is independent
# of it: eight hex digits of a hash of the id, which is what a slice of one could not be after two sessions a
# minute apart shared a name.
expected = {"codex-" + hashlib.sha256(s.encode()).hexdigest()[:8] for s in sessions}
dump = subprocess.run([f"{sandbox}/agora", "dump"], cwd=f"{sandbox}/repo",
                      env={**os.environ, "AGORA_DB": f"{sandbox}/channel/agora.db"},
                      capture_output=True, text=True).stdout
authors = {m["author"] for m in json.loads(dump or "[]")} - {"alice"}
if not sessions:
    print(f"{arm}: no hooks ran, so there is no hook-side member to compare")
elif authors and not authors & expected:
    print(f"{arm}: IDENTITY SPLIT, hooks used {sorted(expected)} and the agent posted as {sorted(authors)}")
else:
    print(f"{arm}: hooks and agent agree on {sorted(expected)}")
PY
  case $arm in
    wake)
      # Did the turn continue, and did the text on stderr reach the model? The word is in the hook's stderr and
      # nowhere else, so an agent that says it was woken and read.
      if grep -q PINEAPPLE "$SANDBOX/$arm.out"; then
        echo "$arm: exit 2 woke the turn, and the stderr reached the model"
      else
        echo "$arm: exit 2 did NOT reach the model"
      fi
      ;;
    ring-control|ring)
      # Whether the agent answered a message that arrived after its user had stopped talking to it. The plant
      # goes into its own thread, so a message there from anybody but alice is an answer.
      python3 - "$SANDBOX" "$arm" <<'RING'
import json, os, subprocess, sys
sandbox, arm = sys.argv[1], sys.argv[2]
dump = subprocess.run([f"{sandbox}/agora", "dump"], cwd=f"{sandbox}/repo",
                      env={**os.environ, "AGORA_DB": f"{sandbox}/channel/agora.db"},
                      capture_output=True, text=True).stdout
answers = [m for m in json.loads(dump or "[]") if m["thread"] == "lexer-panic" and m["author"] != "alice"]
planted = os.path.exists(f"{sandbox}/{arm}.planted")
print(f"{arm}: plant {'posted' if planted else 'MISSING'}, answered {'yes' if answers else 'no'}"
      + (": " + answers[0]["body"][:110] if answers else ""))
RING
      ;;
  esac
  # Which hooks ran, and whether they had anything to say. A layer that was wired and never fired is the
  # failure this script exists to catch, and it is invisible in a transcript.
  for f in "$log"/*.code; do
    [ -e "$f" ] || continue
    local label=$(basename "$f" .code)
    echo "$arm: hook $label ran $(wc -l < "$f" | tr -d ' ') time(s), exits $(tr '\n' ' ' < "$f")," \
      "$( [ -s "$log/$label.out" ] && echo "said something" || echo "said nothing" )"
  done
}

for arm in $ARMS; do run_arm "$arm"; done

echo
echo "=== identity, which is the claim the wiring rests on"
# The thread id a hook was handed, and the member every arm's own commands resolved. They have to be one
# name, or a session is two members: briefed about its own messages and unable to release its own claims.
for arm in $ARMS; do
  log=$SANDBOX/$arm/log
  for f in "$log"/*.in; do
    [ -e "$f" ] || continue
    python3 - "$f" "$arm" <<'PY'
import json, sys
path, arm = sys.argv[1], sys.argv[2]
for line in open(path):
    line = line.strip()
    if not line:
        continue
    event = json.loads(line)
    print(f"  {arm}: {event.get('hook_event_name')} session_id={event.get('session_id')}")
    break
PY
  done
done
"$AGORA" members --text

echo
echo "=== what each agent said"
for arm in $ARMS; do
  echo "--- $arm"
  # --json is an event stream, so the last agent message is what to read rather than the whole of it.
  python3 - "$SANDBOX/$arm.out" <<'PY'
import json, sys
last = ""
for line in open(sys.argv[1]):
    try:
        event = json.loads(line)
    except ValueError:
        continue
    text = json.dumps(event)
    if '"agent_message"' in text or '"assistant"' in text:
        last = text[:1200]
print(last[:1200] or "(no agent message found)")
PY
done

echo
echo "=== the channel afterwards"
"$AGORA" dump --text
"$AGORA" claims --text
echo
echo "sandbox kept at $SANDBOX"
