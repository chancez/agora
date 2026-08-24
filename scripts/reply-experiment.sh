#!/usr/bin/env bash
# Does an agent answer a question somebody asked it in a thread?
#
# The second experiment this design rests on. The first (scripts/hook-experiment.sh) asked whether an agent
# reads the channel at all. This one asks what it does with what it read, because reading turned out not to
# be the whole problem: an agent handed two threads of direct feedback read both, replied to one, and left
# the other marked read and unanswered. The instruction it had offered two outcomes, read or dismiss, and it
# satisfied that instruction exactly.
#
# One task, one variable, two arms. Identical workspaces, identical prompt, the same question planted in each
# arm's own channel, and the only difference is which build of the instructions the agent gets:
#
#   before   triage says read or dismiss
#   after    triage says read, answer, or dismiss, and a question read but unanswered is unfinished
#
# The variable is both the injected text and the skill, because those ship together and an agent gets both.
# It cannot tell you which of the two did the work.
#
# The evidence is the channel afterwards, not the transcript: whether a message from the agent exists in the
# planted thread. Injected context never appears on screen, so reading the output cannot tell you what the
# agent was told.
#
# Everything is under one mktemp directory, with a database per arm. It never touches the real channel.
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
MODEL=${MODEL:-sonnet}
RUNS=${RUNS:-2}
TASK="parser.go panics on empty input. Fix it."
THREAD="empty-input-panic"

# Two plants, because the first one measured nothing. Both are from a person and both are about the work the
# agent is doing, so a reply is the right move either way and an arm that stays silent is the instructions
# failing rather than a judgement call about relevance.
SCENARIO=${SCENARIO:-two-threads}
case $SCENARIO in
blocking)
  # Names the reply as the thing being waited on. Measured 2 of 2 answered on both arms: the instructions
  # never needed fixing for this one, which is why it is kept rather than deleted.
  PLANT="Are you fixing only Parse, or the lexer too? I am about to touch internal/lex and I do not want to \
collide with you. Tell me which before you start, I am waiting on it."
  ;;
feedback)
  # The case that actually went wrong: a request with no question in it and no urgency claimed. An agent read
  # two threads shaped like this, replied to one, and left the other read and silent. Nothing here says "tell
  # me", so the pull to answer has to come from the instructions.
  PLANT="While you are in Parse: it should probably return an error rather than an empty string on empty \
input. Your call, but that is what I would want as a caller."
  ;;
two-threads)
  # The shape the failure actually had. Two threads arrive at once, the agent has a job to get on with, and it
  # replies in the one its own work touches and leaves the other read and silent.
  #
  # The second thread has to be about the agent's own codebase. The first version of this asked for a feature
  # in an unrelated tool, and every arm ran `agora ack` on it, which is the right judgement: a request about
  # something the repository has nothing to do with is not that agent's work. Measured 0 of 2 on both arms and
  # it proved nothing, the same trap as a control that can reach the answer another way.
  PLANT="While you are in Parse: it should probably return an error rather than an empty string on empty \
input. Your call, but that is what I would want as a caller."
  SECOND_THREAD="lexer-empty-input"
  SECOND_PLANT="lexer.go looks like it has the same bug as Parse does. We should fix that one too."
  ;;
*) echo "SCENARIO must be blocking, feedback, or two-threads" >&2; exit 1 ;;
esac
SECOND_THREAD=${SECOND_THREAD:-}

command -v claude >/dev/null || { echo "claude is not on PATH" >&2; exit 1; }

# One binary and one skill per arm. The before arm is built from a commit rather than described, so the two
# are the shipped thing and not a paraphrase of it.
BEFORE_REF=${BEFORE_REF:-HEAD~1}
SANDBOX=$(mktemp -d /tmp/agorareply.XXXXXX)
echo "sandbox: $SANDBOX, scenario: $SCENARIO"

git -C "$REPO_ROOT" worktree add -q "$SANDBOX/before-src" "$BEFORE_REF"
( cd "$SANDBOX/before-src" && go build -o "$SANDBOX/agora-before" ./cmd/agora )
( cd "$REPO_ROOT" && go build -o "$SANDBOX/agora-after" ./cmd/agora )
trap 'git -C "$REPO_ROOT" worktree remove --force "$SANDBOX/before-src" 2>/dev/null || true' EXIT

# Prove the arms differ before spending tokens on them. Two arms running the same text is a null result that
# looks like a real one.
MARKER=${MARKER:-'Answer in the thread that asked'}
before_new=$(strings "$SANDBOX/agora-before" | grep -c "$MARKER" || true)
after_new=$(strings "$SANDBOX/agora-after" | grep -c "$MARKER" || true)
if [ "$before_new" != "0" ] || [ "$after_new" = "0" ]; then
  echo "the two arms carry the same instructions, so this would measure nothing" >&2
  exit 1
fi

run_arm() {
  local arm=$1 run=$2
  local dir=$SANDBOX/$arm-$run
  local agora=$SANDBOX/agora-$arm
  local db=$dir/agora.db
  local skill_src=$REPO_ROOT/skills/agora
  [ "$arm" = "before" ] && skill_src=$SANDBOX/before-src/skills/agora

  mkdir -p "$dir/repo/.claude/skills"
  cd "$dir/repo"
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
  # A second file, so a thread about it is about this agent's own codebase rather than about somebody else's
  # tool, which it would be right to dismiss.
  cat > lexer.go <<'GO'
package parser

import "strings"

// Tokens splits input into tokens.
func Tokens(input string) []string {
	fields := strings.Fields(input)
	return fields[:len(fields)-1]
}
GO
  git add parser.go lexer.go
  git -c user.email=sandbox@example.invalid -c user.name=sandbox commit -qm parser
  cp -R "$skill_src" "$dir/repo/.claude/skills/agora"

  # Planted as a person. Its own database per run, so no arm can see another's channel.
  AGORA_DB=$db AGORA_MEMBER=chancez "$agora" post "$THREAD" "$PLANT" --text >/dev/null
  if [ -n "$SECOND_THREAD" ]; then
    AGORA_DB=$db AGORA_MEMBER=chancez "$agora" post "$SECOND_THREAD" "$SECOND_PLANT" --text >/dev/null
  fi

  python3 - "$dir" "$agora" "$db" <<'PY'
import json, sys
dir, agora, db = sys.argv[1], sys.argv[2], sys.argv[3]
inject = [{"hooks": [{"type": "command", "command": f"{agora} inject"}]}]
json.dump({
    "env": {"AGORA_DB": db},
    "permissions": {"allow": [
        f"Bash({agora}:*)", "Bash(agora:*)", "Bash(ls:*)", "Bash(cat:*)", "Bash(go:*)",
        "Read", "Edit", "Write", "Glob", "Grep",
    ]},
    "hooks": {"SessionStart": inject, "UserPromptSubmit": inject},
}, open(f"{dir}/settings.json", "w"), indent=2)
PY

  echo
  echo "=== $arm run $run"
  # A config dir per run, because --settings adds to the user's settings rather than replacing them, and one
  # developer's own settings wire agora's hooks: a second inject in every arm, and `leave --force` at
  # SessionEnd, which had released the claims this reads. Credentials come from the keychain, so a fresh
  # config dir still authenticates.
  mkdir -p "$dir/claude"
  # The arm's own build under the name the agent types. `agora` on the developer's PATH is some other commit,
  # and an arm running a binary from outside itself is not the arm.
  mkdir -p "$dir/bin"
  ln -sf "$agora" "$dir/bin/agora"
  if ! CLAUDE_CONFIG_DIR=$dir/claude PATH="$dir/bin:$PATH" claude -p "$TASK" --model "$MODEL" --permission-mode acceptEdits \
      --settings "$dir/settings.json" > "$dir/out.txt" 2> "$dir/err.txt"; then
    echo "  INVALID: claude exited nonzero"
    sed 's/^/    /' "$dir/err.txt" | head -3
    echo invalid > "$dir/invalid"
    return
  fi
  if [ ! -s "$dir/out.txt" ]; then
    # An arm whose agent never ran leaves the channel untouched, which reads exactly like the failure this
    # is looking for. That happened once in the other experiment, from a settings path that did not exist.
    echo "  INVALID: no transcript"
    echo invalid > "$dir/invalid"
    return
  fi
  # The evidence is the channel: a reply in the thread being measured, and whether anything was left unread.
  # A thread dismissed with ack is a judgement the agent is allowed to make, so it is reported separately
  # rather than counted as the failure, which is a person left with no answer at all.
  local measured=${SECOND_THREAD:-$THREAD}
  AGORA_DB=$db "$agora" dump --thread "$measured" > "$dir/thread.json"
  AGORA_DB=$db "$agora" members > "$dir/members.json"
  local replies
  replies=$(python3 -c '
import json, sys
msgs = json.load(open(sys.argv[1]))
print(sum(1 for m in msgs if m["author"] != "chancez"))' "$dir/thread.json")
  local unread
  unread=$(python3 -c '
import json, sys
members = json.load(open(sys.argv[1]))
print(sum(m["unread"] for m in members if m["name"] != "chancez"))' "$dir/members.json")
  echo "$replies" > "$dir/replies"
  echo "  thread under test: $measured"
  if [ "$replies" -gt 0 ]; then
    echo "  ANSWERED ($replies), $unread left unread"
  else
    echo "  left unanswered, $unread left unread"
  fi
  AGORA_DB=$db "$agora" dump --text | sed 's/^/    /' | head -20
}

for run in $(seq 1 "$RUNS"); do
  run_arm before "$run"
  run_arm after "$run"
done

echo
echo "=== verdict"
for arm in before after; do
  answered=0 invalid=0
  for run in $(seq 1 "$RUNS"); do
    dir=$SANDBOX/$arm-$run
    if [ -f "$dir/invalid" ]; then
      invalid=$((invalid + 1))
    elif [ "$(cat "$dir/replies" 2>/dev/null || echo 0)" -gt 0 ]; then
      answered=$((answered + 1))
    fi
  done
  echo "$arm: answered $answered of $RUNS, invalid $invalid"
done
echo "transcripts and channels are under $SANDBOX"
