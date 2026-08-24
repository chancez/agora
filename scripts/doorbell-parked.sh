#!/usr/bin/env bash
# Does a message wake a session that is already sitting at its prompt?
#
# The half of the doorbell that `scripts/doorbell-experiment.sh` cannot reach. That one measures the plain
# `Stop` wiring, where exit 2 continues a turn that is still running, because `claude -p` exits when its turn
# ends and leaves nothing to wake. This asks the question the doorbell exists for: nobody has typed anything
# for minutes, the agent is parked, and a message addressed to it arrives.
#
# So it needs an interactive session on a pty that outlives this script, which is what cm provides. That is a
# requirement of the measurement and not of agora: `agora doorbell --wait` needs a harness that runs hooks and
# nothing else.
#
# One arm, because there is nothing to compare against: no other layer here delivers to an idle session at all.
# The evidence is a reply in the channel with no input sent to the session between the post and the reply, and
# `cm read` at the end shows what the session did.
#
# Everything is under one mktemp directory, with its own database. It never touches the real channel.
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
MODEL=${MODEL:-sonnet}
SESSION=${SESSION:-agora-parked}
WAIT_FOR_REPLY=${WAIT_FOR_REPLY:-120}
# How long the session sits parked before anything is posted to it. The point of a bounded --wait is that it
# outlives the turn, and a post two seconds later does not test that: a background hook killed at its timeout
# would still have delivered. Raise it past the harness's hook timeout to measure the difference.
SETTLE=${SETTLE:-5}
THREAD=lexer-panic

command -v claude >/dev/null || { echo "claude is not on PATH" >&2; exit 1; }
command -v cm >/dev/null || { echo "cm is not on PATH: this needs a pty that outlives the script" >&2; exit 1; }

DIR=$(mktemp -d /tmp/agoraparked.XXXXXX)
echo "sandbox: $DIR"
AGORA=$DIR/agora
( cd "$REPO_ROOT" && go build -o "$AGORA" ./cmd/agora )

mkdir -p "$DIR/repo/.claude/skills" "$DIR/claude" "$DIR/bin"
cd "$DIR/repo"
git init -q -b main
printf 'a parked agent\n' > README.md
git add README.md
git -c user.email=sandbox@example.invalid -c user.name=sandbox commit -qm readme
cp -R "$REPO_ROOT/skills/agora" "$DIR/repo/.claude/skills/agora"
ln -sf "$AGORA" "$DIR/bin/agora"
# Without this an agent invents a path for the binary, and the one it invented in the headless experiment was
# inside the skill directory, which no permission rule covers.
printf '%s\n' "agora is installed and on your PATH. Run it as \`agora\`, never by a path." > "$DIR/repo/CLAUDE.md"
git add CLAUDE.md
git -c user.email=sandbox@example.invalid -c user.name=sandbox commit -qm claude-md

# An interactive session in a fresh config dir opens the onboarding wizard and the trust dialog, and a wizard
# waiting for a keystroke looks exactly like an agent that was never woken. Three flags from the developer's
# own config, and this repository marked trusted: not a copy of the file, which carries every project they
# have ever opened.
python3 - "$DIR" > "$DIR/claude/.claude.json" <<'PY'
import json, os, sys
dir = sys.argv[1]
try:
    theirs = json.load(open(os.path.expanduser("~/.claude.json")))
except (OSError, ValueError):
    theirs = {}
json.dump({
    "hasCompletedOnboarding": True,
    "lastOnboardingVersion": theirs.get("lastOnboardingVersion", "1.0.0"),
    "installMethod": theirs.get("installMethod", "unknown"),
    "projects": {f"{dir}/repo": {
        "hasTrustDialogAccepted": True,
        "hasCompletedProjectOnboarding": True,
        "projectOnboardingSeenCount": 1,
        "allowedTools": [],
    }},
}, sys.stdout, indent=2)
PY

# Somebody to be woken by, and a channel that exists.
AGORA_DB=$DIR/agora.db AGORA_MEMBER=bob "$AGORA" join --description "rewriting the lexer" --text >/dev/null

python3 - "$DIR" "$AGORA" "$DIR/agora.db" <<'PY'
import json, sys
dir, agora, db = sys.argv[1:4]
inject = [{"hooks": [{"type": "command", "command": f"{agora} inject"}]}]
json.dump({
    "env": {"AGORA_DB": db},
    "permissions": {"allow": [
        f"Bash({agora}:*)", "Bash(agora:*)", "Bash(cat:*)", "Bash(ls:*)",
        "Read", "Edit", "Write", "Glob", "Grep",
    ]},
    "hooks": {
        "SessionStart": inject,
        "UserPromptSubmit": inject,
        # The event is captured to know when the turn ended, which is when the session is parked, and to read
        # the session id: the member name a plant has to address is derived from it.
        #
        # asyncRewake is the whole mechanism under test: it "runs in the background and wakes Claude on exit
        # code 2", and the hook's stderr is what the model is shown.
        "Stop": [{"hooks": [
            {"type": "command", "command": f"cat > {dir}/stop-event.json"},
            {"type": "command", "command": f"{agora} doorbell --wait 10m", "asyncRewake": True},
        ]}],
    },
}, open(f"{dir}/settings.json", "w"), indent=2)
PY

cm kill "$SESSION" >/dev/null 2>&1 || true
# env -u because this script is usually run *by* an agent, and a session that inherits
# CLAUDE_CODE_CHILD_SESSION comes up as a nested one with its transcript saving off. Measured: the first run
# had that marker and never completed a turn.
cm run --detach --session "$SESSION" --dir "$DIR/repo" \
  --env "CLAUDE_CONFIG_DIR=$DIR/claude" --env "PATH=$DIR/bin:$PATH" \
  -- env -u CLAUDE_CODE_CHILD_SESSION -u CLAUDE_CODE_SESSION_ID -u CLAUDECODE -u CLAUDE_CODE_ENTRYPOINT \
     claude --settings "$DIR/settings.json" --model "$MODEL" --permission-mode acceptEdits >/dev/null
trap 'cm kill "$SESSION" >/dev/null 2>&1 || true' EXIT

echo "waiting for the prompt"
for _ in $(seq 1 40); do
  cm read "$SESSION" | grep -qE "accept edits on|for shortcuts" && break
  sleep 1
done
# The prompt box appearing is not the same as it being ready for input, and a keystroke sent into a
# half-drawn TUI is silently dropped.
sleep 5
PROMPT="read README.md and tell me its first line, then stop"
cm send "$SESSION" "$PROMPT" --enter
sleep 5
if ! cm read "$SESSION" | grep -q "first line"; then
  echo "the prompt did not land, sending it again"
  cm send "$SESSION" "$PROMPT" --enter
fi

echo "waiting for the turn to end"
for _ in $(seq 1 90); do
  [ -s "$DIR/stop-event.json" ] && break
  sleep 1
done
if [ ! -s "$DIR/stop-event.json" ]; then
  echo "INVALID: the session never finished a turn, so it was never parked"
  cm read "$SESSION" | tail -20
  exit 1
fi
MEMBER=claude-$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["session_id"][:8])' "$DIR/stop-event.json")
echo "parked as $MEMBER"

# The doorbell should be waiting in the background. A wake cannot arrive if nothing is holding the wait, and
# that failure would otherwise be indistinguishable from an agent that ignored the wake.
sleep 2
if ! pgrep -f "doorbell --wait" >/dev/null; then
  echo "INVALID: no doorbell is waiting, so asyncRewake did not start one"
  exit 1
fi
echo "a doorbell is waiting"

if [ "$SETTLE" -gt 0 ]; then
  echo "leaving it parked for ${SETTLE}s"
  sleep "$SETTLE"
  if ! pgrep -f "doorbell --wait" >/dev/null; then
    echo "INVALID: the doorbell did not survive ${SETTLE}s parked, so --wait is bounded by something else"
    exit 1
  fi
fi

# Nothing is sent to the session from here. Whatever it does next, it decided to do without being prompted.
AGORA_DB=$DIR/agora.db AGORA_MEMBER=bob "$AGORA" post "$THREAD" \
  "$MEMBER, I am about to change lex.go for the same missing bounds check. Does anything you are holding touch that file?" \
  --text >/dev/null
POSTED=$(date +%s)
echo "posted to $THREAD, waiting up to ${WAIT_FOR_REPLY}s for a reply with nothing typed"

replied=0
for _ in $(seq 1 "$WAIT_FOR_REPLY"); do
  if AGORA_DB=$DIR/agora.db "$AGORA" dump --thread "$THREAD" | python3 -c '
import json, sys
messages = json.load(sys.stdin)
sys.exit(0 if any(m["author"] == sys.argv[1] for m in messages) else 1)' "$MEMBER"; then
    replied=1
    break
  fi
  sleep 1
done

echo
if [ "$replied" = "1" ]; then
  echo "=== WOKEN: it answered $(( $(date +%s) - POSTED ))s after the post, with nothing typed"
else
  echo "=== not woken: no reply in ${WAIT_FOR_REPLY}s"
fi
AGORA_DB=$DIR/agora.db "$AGORA" dump --text | sed 's/^/    /'
echo
echo "=== the session's last lines"
cm read "$SESSION" | tail -25
echo
echo "sandbox: $DIR"
