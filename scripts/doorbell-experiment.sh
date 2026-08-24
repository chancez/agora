#!/usr/bin/env bash
# Does an agent answer a message that arrives after its user stopped talking to it?
#
# The fourth experiment. The first (scripts/hook-experiment.sh) asked whether an agent reads the channel, the
# second (scripts/reply-experiment.sh) what it does with what it read, the third (scripts/announce-experiment.sh)
# whether anything of its own gets in. All three deliver at a moment the agent is already acting: a session
# start, a prompt, an attempted edit. This one asks about the moment nothing is acting, which is where every
# other layer has nothing to say.
#
# One task, one variable, two arms. Identical workspaces, identical prompt, the same binary, and the only
# difference is one hook:
#
#   control    inject on SessionStart and UserPromptSubmit
#   doorbell   the same, plus `agora doorbell` on Stop
#
# The plant is posted from inside the turn, by a PostToolUse hook, and that is the whole reason this measures
# anything: a message that exists before the prompt would arrive by injection, and injection is what both arms
# have. It names the agent, so it is addressed to it whatever thread it lands in.
#
# The evidence is the channel afterwards: a reply in the planted thread, written by the agent. Not the
# transcript, since neither injected context nor a wake appears on screen.
#
# One trap this one added: an arm that was woken, tried to answer, and had its `agora` call refused looks
# exactly like an arm that chose to stay silent. Seen on the first run, where the agent invoked
# `/Users/<somebody>/.claude/skills/agora/agora`, a path it invented, which no permission rule covers and
# which nobody was there to approve. A refused agora call now counts as invalid rather than as silence.
#
# What this cannot measure is the case the doorbell was built for: a session parked at a prompt, woken by an
# asyncRewake hook exiting 2. `claude -p` exits when its turn ends, so there is no idle session for a
# background hook to wake. The plain Stop wiring measured here shares everything except that last step, and the
# parked case needs an interactive session in a pty. Doing it by hand: wire
# `{"type": "command", "command": "agora doorbell --wait 30m", "asyncRewake": true}` on Stop, run claude
# interactively in a throwaway CLAUDE_CONFIG_DIR, let it finish a task and sit at the prompt, then post to it
# from another terminal and watch whether a reply appears with nothing typed into the session.
#
# Everything is under one mktemp directory, with a database per arm. It never touches the real channel.
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
MODEL=${MODEL:-sonnet}
RUNS=${RUNS:-3}
TASK="parser.go panics on empty input. Fix it."
PLANT_THREAD=lexer-panic

command -v claude >/dev/null || { echo "claude is not on PATH" >&2; exit 1; }

SANDBOX=$(mktemp -d /tmp/agoradoorbell.XXXXXX)
echo "sandbox: $SANDBOX"

# Built here, not taken from PATH. An arm running a binary from outside itself is not the arm, and a script
# that points at a build it did not make measures whatever was last built.
AGORA=$SANDBOX/agora
( cd "$REPO_ROOT" && go build -o "$AGORA" ./cmd/agora )
"$AGORA" doorbell --help >/dev/null 2>&1 || {
  echo "this build has no doorbell command, so the arms would not differ" >&2; exit 1; }

read -r -d '' FIXTURE <<'GO' || true
package parser

import "strings"

// Parse returns the first token of input.
func Parse(input string) string {
	fields := strings.Fields(input)
	return fields[0]
}
GO

# The plant, posted from inside the turn. Once per run: PostToolUse fires on every edit, and a channel filling
# with the same message measures nothing.
read -r -d '' PLANT <<'SH' || true
#!/usr/bin/env bash
set -euo pipefail
dir=$1; agora=$2; db=$3; thread=$4
[ -f "$dir/planted" ] && exit 0
session=$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("session_id",""))')
[ -n "$session" ] || exit 0
member="claude-${session:0:8}"
echo "$member" > "$dir/member"
AGORA_DB=$db AGORA_MEMBER=bob "$agora" post "$thread" \
  "$member, your parser fix does not cover lex.go: the same missing bounds check is there, and I am about to change it. Does your fix touch that file, or are these two separate goes at one bug?" \
  --text >/dev/null
echo planted > "$dir/planted"
SH

run_arm() {
  local arm=$1 run=$2
  local dir=$SANDBOX/$arm-$run
  local db=$dir/agora.db

  mkdir -p "$dir/repo/.claude/skills" "$dir/claude" "$dir/bin"
  cd "$dir/repo"
  git init -q -b main
  printf '%s\n' "$FIXTURE" > parser.go
  git add parser.go
  git -c user.email=sandbox@example.invalid -c user.name=sandbox commit -qm parser
  cp -R "$REPO_ROOT/skills/agora" "$dir/repo/.claude/skills/agora"
  # Both arms, so it is not the variable. Without it an agent invents a path for the binary, and the one it
  # invented was inside the skill directory: `/Users/<somebody>/.claude/skills/agora/agora`, which no
  # permission rule covers and which nobody in a headless run is there to approve.
  printf '%s\n' "agora is installed and on your PATH. Run it as \`agora\`, never by a path." \
    > "$dir/repo/CLAUDE.md"
  git add CLAUDE.md
  git -c user.email=sandbox@example.invalid -c user.name=sandbox commit -qm claude-md
  printf '%s\n' "$PLANT" > "$dir/plant.sh"
  chmod +x "$dir/plant.sh"
  # The arm's own build under the name an agent types.
  ln -sf "$AGORA" "$dir/bin/agora"

  # A person in the roster and nothing said, so the channel exists and inject has somebody to name. The plant
  # is the only message in it, and it arrives later.
  AGORA_DB=$db AGORA_MEMBER=chancez "$AGORA" join \
    --description "reviewing the parser rewrite" --text >/dev/null

  python3 - "$dir" "$AGORA" "$db" "$arm" "$PLANT_THREAD" <<'PY'
import json, sys
dir, agora, db, arm, thread = sys.argv[1:6]
inject = [{"hooks": [{"type": "command", "command": f"{agora} inject"}]}]
# Both arms capture the Stop event, which is how the payload this reads is checked against a real one rather
# than against the documentation. A hook that writes to a file and exits 0 changes nothing for the agent.
stop = [{"hooks": [{"type": "command", "command": f"cat > {dir}/stop-event.json"}]}]
if arm == "doorbell":
    stop[0]["hooks"].append({"type": "command", "command": f"{agora} doorbell"})
json.dump({
    "env": {"AGORA_DB": db},
    "permissions": {"allow": [
        f"Bash({agora}:*)", "Bash(agora:*)", "Bash(ls:*)", "Bash(cat:*)", "Bash(go:*)",
        "Read", "Edit", "Write", "Glob", "Grep",
    ]},
    "hooks": {
        "SessionStart": inject,
        "UserPromptSubmit": inject,
        "PostToolUse": [{"matcher": "Edit|Write",
                         "hooks": [{"type": "command",
                                    "command": f"{dir}/plant.sh {dir} {agora} {db} {thread}"}]}],
        "Stop": stop,
    },
}, open(f"{dir}/settings.json", "w"), indent=2)
PY

  echo
  echo "=== $arm run $run"
  # A config dir per run, because --settings adds to the user's own settings rather than replacing them, and
  # one developer's settings wire agora themselves. Credentials come from the keychain, so a fresh config dir
  # still authenticates.
  if ! CLAUDE_CONFIG_DIR=$dir/claude PATH="$dir/bin:$PATH" claude -p "$TASK" --model "$MODEL" \
      --permission-mode acceptEdits --settings "$dir/settings.json" \
      > "$dir/out.txt" 2> "$dir/err.txt"; then
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
    echo "  INVALID: parser.go untouched, so the arm never edited and the plant never landed"
    echo invalid > "$dir/invalid"
    return
  fi
  if [ ! -f "$dir/planted" ]; then
    echo "  INVALID: nothing was planted, so there was nothing to answer"
    echo invalid > "$dir/invalid"
    return
  fi

  # An agent that was woken, tried to answer, and could not run agora reads exactly like one that stayed
  # silent. Seen once: it invoked a path it invented, `/Users/<somebody>/.claude/skills/agora/agora`, which
  # no permission rule covers, and in headless mode nobody is there to approve it.
  python3 - "$dir/stop-event.json" > "$dir/blocked" <<'PY'
import json, sys
event = json.load(open(sys.argv[1]))
blocked = []
try:
    lines = open(event["transcript_path"])
except (KeyError, OSError):
    lines = []
pending = {}
for line in lines:
    try:
        entry = json.loads(line)
    except ValueError:
        continue
    content = (entry.get("message") or {}).get("content")
    if not isinstance(content, list):
        continue
    for part in content:
        if not isinstance(part, dict):
            continue
        if part.get("type") == "tool_use" and part.get("name") == "Bash":
            pending[part.get("id")] = (part.get("input") or {}).get("command", "")
        if part.get("type") == "tool_result":
            command = pending.get(part.get("tool_use_id"), "")
            text = part.get("content")
            text = text if isinstance(text, str) else json.dumps(text)
            # "This command requires approval" is what a refusal actually says, with is_error set. The other
            # two are the same failure by a different route: a path the agent invented no rule covers, or a
            # binary that is not where it guessed.
            refused = ("requires approval", "permission denied", "no such file", "command not found")
            if "agora" in command and part.get("is_error") and any(r in text.lower() for r in refused):
                blocked.append(command + "   -> " + text.strip().splitlines()[0][:120])
# Written rather than printed, because print of an empty list is still a newline, and a one-byte file passes
# the -s test that decides whether anything was refused. That marked three clean control runs invalid.
sys.stdout.write("".join(line + "\n" for line in blocked))
PY

  local member
  member=$(cat "$dir/member")
  # What the doorbell knows about this member now: rang is the watermark, so a nonzero one is a ring that
  # happened, and messages is what is still addressed to it. In the control arm nothing rang, so the plant
  # must show up here: if it does not, it named the wrong member and the run proves nothing.
  AGORA_DB=$db AGORA_MEMBER=$member "$AGORA" doorbell --dry-run > "$dir/doorbell.json" 2>/dev/null || true
  AGORA_DB=$db "$AGORA" dump > "$dir/dump.json"
  python3 - "$dir/dump.json" "$dir/doorbell.json" "$member" "$PLANT_THREAD" > "$dir/verdict" <<'PY'
import json, sys
messages = json.load(open(sys.argv[1]))
bell = json.load(open(sys.argv[2]))
member, thread = sys.argv[3], sys.argv[4]
replies = [m for m in messages if m["thread"] == thread and m["author"] == member]
print(json.dumps({
    "replied": len(replies),
    "rang": bell.get("rang", 0),
    "addressed_left": len(bell.get("messages") or []),
}))
PY
  local replied rang addressed
  replied=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["replied"])' "$dir/verdict")
  rang=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["rang"])' "$dir/verdict")
  addressed=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["addressed_left"])' "$dir/verdict")
  if [ "$rang" = "0" ] && [ "$addressed" = "0" ]; then
    echo "  INVALID: the plant was not addressed to $member"
    echo invalid > "$dir/invalid"
    return
  fi
  if [ "$replied" -gt 0 ]; then
    echo "  ANSWERED ($replied in $PLANT_THREAD, rang at $rang)"
  elif [ -s "$dir/blocked" ]; then
    echo "  INVALID: it tried to answer and its agora call was refused:"
    sed 's/^/    /' "$dir/blocked" | head -3
    echo invalid > "$dir/invalid"
    return
  else
    echo "  said nothing (rang at $rang)"
  fi
  AGORA_DB=$db "$AGORA" dump --text | sed 's/^/    /' | head -20
}

for run in $(seq 1 "$RUNS"); do
  run_arm control "$run"
  run_arm doorbell "$run"
done

echo
echo "=== the Stop event, as the harness writes it"
# Printed once, because whether stop_hook_active is really there is a fact this reads rather than assumes.
for candidate in "$SANDBOX"/doorbell-*/stop-event.json "$SANDBOX"/control-*/stop-event.json; do
  [ -s "$candidate" ] || continue
  python3 -c 'import json,sys; print(json.dumps(sorted(json.load(open(sys.argv[1])).keys())))' "$candidate"
  break
done

echo
echo "=== verdict"
for arm in control doorbell; do
  answered=0 invalid=0
  for run in $(seq 1 "$RUNS"); do
    dir=$SANDBOX/$arm-$run
    if [ -f "$dir/invalid" ]; then
      invalid=$((invalid + 1))
    elif [ "$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["replied"])' "$dir/verdict" 2>/dev/null || echo 0)" -gt 0 ]; then
      answered=$((answered + 1))
    fi
  done
  echo "$arm: answered the plant in $answered of $RUNS, invalid $invalid"
done
echo "transcripts and channels are under $SANDBOX"
