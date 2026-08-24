#!/usr/bin/env bash
# Does an agent open a thread for the work it is starting?
#
# The third experiment. The first (scripts/hook-experiment.sh) asked whether an agent reads the channel, the
# second (scripts/reply-experiment.sh) what it does with what it read. This one asks whether anything of its
# own ever gets in, because reading and answering leave the record one-sided: a session read the one thread
# waiting, judged it correctly, then started a feature and posted nothing. Nothing had asked it to.
#
# One task, one variable, two arms. Identical workspaces, identical prompt, a channel that exists with a person
# in the roster and no threads in it, and the only difference is which build of the instructions the agent gets:
#
#   before   the hook is silent on a channel with nothing unread, and the skill says claim what you start
#   after    the hook asks a member with nothing of its own here to open a thread, and the skill leads with it
#
# The variable is both the injected text and the skill, because those ship together and an agent gets both. It
# cannot tell you which of the two did the work.
#
# Nothing is planted to reply to, so every thread in the channel afterwards was opened by the agent. That is the
# evidence, not the transcript: injected context never appears on screen.
#
# An arm that did not do the work would also have posted nothing, which reads exactly like the failure being
# measured, so an unchanged parser.go counts as invalid rather than as a result.
#
# Everything is under one mktemp directory, with a database per arm. It never touches the real channel.
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
MODEL=${MODEL:-sonnet}
RUNS=${RUNS:-3}
TASK="parser.go panics on empty input. Fix it."

command -v claude >/dev/null || { echo "claude is not on PATH" >&2; exit 1; }

# One binary and one skill per arm. The before arm is built from a commit rather than described, so the two are
# the shipped thing and not a paraphrase of it. HEAD is the default because the change under test is the working
# tree; once it is committed, pass BEFORE_REF=HEAD~1.
BEFORE_REF=${BEFORE_REF:-HEAD}
SANDBOX=$(mktemp -d /tmp/agoraannounce.XXXXXX)
echo "sandbox: $SANDBOX"

git -C "$REPO_ROOT" worktree add -q "$SANDBOX/before-src" "$BEFORE_REF"
( cd "$SANDBOX/before-src" && go build -o "$SANDBOX/agora-before" ./cmd/agora )
( cd "$REPO_ROOT" && go build -o "$SANDBOX/agora-after" ./cmd/agora )
trap 'git -C "$REPO_ROOT" worktree remove --force "$SANDBOX/before-src" 2>/dev/null || true' EXIT

# Prove the arms differ before spending tokens on them. Two arms running the same text is a null result that
# looks like a real one.
MARKER=${MARKER:-'Nothing in this channel is from you yet'}
before_new=$(strings "$SANDBOX/agora-before" | grep -c "$MARKER" || true)
after_new=$(strings "$SANDBOX/agora-after" | grep -c "$MARKER" || true)
if [ "$before_new" != "0" ] || [ "$after_new" = "0" ]; then
  echo "the two arms carry the same instructions, so this would measure nothing" >&2
  exit 1
fi

read -r -d '' FIXTURE <<'GO' || true
package parser

import "strings"

// Parse returns the first token of input.
func Parse(input string) string {
	fields := strings.Fields(input)
	return fields[0]
}
GO

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
  printf '%s\n' "$FIXTURE" > parser.go
  git add parser.go
  git -c user.email=sandbox@example.invalid -c user.name=sandbox commit -qm parser
  cp -R "$skill_src" "$dir/repo/.claude/skills/agora"

  # A person in the roster and nothing said. The channel has to exist, since inject stays silent until a
  # database does, and somebody has to be there for a post to be worth writing.
  AGORA_DB=$db AGORA_MEMBER=chancez "$agora" join \
    --description "reviewing the parser rewrite" --text >/dev/null

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
    echo "  INVALID: no transcript"
    echo invalid > "$dir/invalid"
    return
  fi
  if git diff --quiet -- parser.go; then
    # An agent that did not do the work posts nothing either, which reads exactly like the failure this is
    # looking for.
    echo "  INVALID: parser.go untouched, so the arm did not do the task"
    echo invalid > "$dir/invalid"
    return
  fi

  AGORA_DB=$db "$agora" dump > "$dir/dump.json"
  AGORA_DB=$db "$agora" claims > "$dir/claims.json"
  python3 - "$dir/dump.json" "$dir/claims.json" > "$dir/opened" <<'PY'
import json, sys
messages = json.load(open(sys.argv[1]))
claims = json.load(open(sys.argv[2]))
threads = {m["thread"] for m in messages if m["author"] != "chancez"}
threads |= {c["thread"] for c in claims if c["holder"] != "chancez"}
print(len(threads))
PY
  local opened
  opened=$(cat "$dir/opened")
  if [ "$opened" -gt 0 ]; then
    echo "  OPENED $opened"
  else
    echo "  said nothing"
  fi
  AGORA_DB=$db "$agora" dump --text | sed 's/^/    /' | head -20
  AGORA_DB=$db "$agora" claims --text | sed 's/^/    /' | head -5
}

for run in $(seq 1 "$RUNS"); do
  run_arm before "$run"
  run_arm after "$run"
done

echo
echo "=== verdict"
for arm in before after; do
  opened=0 invalid=0
  for run in $(seq 1 "$RUNS"); do
    dir=$SANDBOX/$arm-$run
    if [ -f "$dir/invalid" ]; then
      invalid=$((invalid + 1))
    elif [ "$(cat "$dir/opened" 2>/dev/null || echo 0)" -gt 0 ]; then
      opened=$((opened + 1))
    fi
  done
  echo "$arm: opened a thread in $opened of $RUNS, invalid $invalid"
done
echo "transcripts and channels are under $SANDBOX"
