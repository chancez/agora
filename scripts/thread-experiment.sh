#!/usr/bin/env bash
# Does an agent keep the second step of one piece of work in the thread that announced it?
#
# The fifth experiment, and the first about what an agent does *after* the announce nudge has worked. Watched in
# a real channel: one Codex session opened kitty-sandbox-codex, kitty-sandbox-mise and kitty-sandbox-portability
# in nine minutes, two messages each, opened and closed in turn, for one continuous piece of work whose third
# thread partly reverted the first. Half a record is worse than a short one: a later reader cannot tell which
# half is current.
#
# Three turns per session, because that failure is only reachable at a turn boundary. The announce nudge fires
# once and stops the moment a member posts, so from the second prompt on, the only things telling an agent where
# to put its next message are the standing rule in AGENTS.md and whatever `agora post` said back:
#
#   1. parser.go panics on empty input. Fix it.            -> opens a thread, correctly
#   2. Now add the regression test for that fix.           -> the SAME work, so the same thread
#   3. A different bug, in config.go.                      -> genuinely separate, so a new thread IS right
#
# Turn 3 is what keeps this honest. A change that taught an agent to never open a second thread would score
# perfectly on turn 2 and be worse than what it replaced, so both numbers are reported and an arm has to get
# both right.
#
# Two variables, four arms, because prose and state ship together and the question is which one moves an agent:
#
#   before   the old wording, and a build whose `agora post` says only "posted 1 to <channel>"
#   wording  the new standing rule, and the old build
#   notice   the old wording, and a build where opening a thread names what you already have open
#   after    both, which is what ships
#
# The wording variable is the standing rule in AGENTS.md *and* the skill, taken out of each source tree rather
# than paraphrased here, because those two ship together and an agent gets both.
#
# Codex rather than Claude Code, because that is where the failure was observed, and an arm that never
# reproduces it measures the agent being right rather than the change working. Everything Codex needs is
# copied from scripts/codex-hook-experiment.sh: a throwaway CODEX_HOME with a copy of auth.json, a throwaway
# $HOME because $HOME/.agents is read whatever CODEX_HOME says, a ZDOTDIR per arm because a tool call runs in a
# login shell that rebuilds PATH, --dangerously-bypass-hook-trust, and --add-dir for the channel.
#
# Traps this one adds, on top of the eight in AGENTS.md:
#
#   - **A channel per run, not per script.** Every arm here opens threads, so one shared channel would show the
#     next arm somebody else's kitty-sandbox-codex to post into, and an agent reusing another arm's thread is
#     not the behaviour being measured.
#   - **AGORA_MEMBER is pinned per run.** Identity is not under test, and `codex exec resume` is not promised to
#     keep the thread id its first turn had. If it does not, turn 2 is a *different member*, which gets the
#     announce nudge again and opens a thread of its own: the result this is looking for, produced by the
#     harness. Pinning removes that whole class of doubt, and the check below asserts one author afterwards.
#   - **No `leave --force` on SessionEnd, though docs/setup.md wires one.** codex exec is one process per turn,
#     so a SessionEnd hook fires between turns rather than at the end of the session. Checked before dropping
#     it: it does not re-arm the nudge, because inject's own heartbeat puts the member back with its real post
#     count before deciding, but it does wipe the roster description and release the agent's claims mid-session,
#     and neither belongs in a measurement about threads.
#   - **An arm that did not do the work says nothing either**, which reads exactly like the failure. Each turn
#     is checked for its edit, and a turn that did nothing makes the run invalid rather than a result.
#
# This spawns real Codex sessions and costs tokens. Everything it touches is under one mktemp directory, and it
# never opens the real channel.
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
RUNS=${RUNS:-2}
ARMS=${ARMS:-"before wording notice after"}
# The two commits that made the change, so `before` is the shipped previous thing rather than a description of
# it. Pass BEFORE_REF explicitly once anything else lands on top.
BEFORE_REF=${BEFORE_REF:-HEAD~2}
LOGIN_SHELL=${LOGIN_SHELL:-${SHELL:-/bin/sh}}

TURN1="parser.go panics on empty input. Fix it."
TURN2="Now add the regression test for that fix."
TURN3="Separate bug, unrelated to the parser: config.go ignores \$XDG_CONFIG_HOME and always builds the path from \$HOME. Fix that one too."

command -v codex >/dev/null || { echo "codex is not on PATH" >&2; exit 1; }
[ -f "$HOME/.codex/auth.json" ] || { echo "no ~/.codex/auth.json: run codex login first" >&2; exit 1; }
command -v python3 >/dev/null || { echo "python3 is not on PATH" >&2; exit 1; }

SANDBOX=$(mktemp -d /tmp/agorathread.XXXXXX)
echo "sandbox: $SANDBOX"

# Both builds from their own source, never from bin/, which is whatever was last built. The Claude Code
# experiment pointed at bin/agora once and produced two invalid runs in a row.
git -C "$REPO_ROOT" worktree add -q "$SANDBOX/before-src" "$BEFORE_REF"
trap 'git -C "$REPO_ROOT" worktree remove --force "$SANDBOX/before-src" 2>/dev/null || true' EXIT
( cd "$SANDBOX/before-src" && go build -o "$SANDBOX/agora-before" ./cmd/agora )
( cd "$REPO_ROOT" && go build -o "$SANDBOX/agora-after" ./cmd/agora )

# Prove the arms differ before spending tokens. Two arms carrying the same thing is a null result that looks
# like a real one, and this script's variable is two things rather than one.
NOTICE_MARKER='post to whichever of those this work continues'
NUDGE_MARKER='Nothing in this channel is from you yet'
for build in before after; do
  notice=$(strings "$SANDBOX/agora-$build" | grep -c "$NOTICE_MARKER" || true)
  nudge=$(strings "$SANDBOX/agora-$build" | grep -c "$NUDGE_MARKER" || true)
  echo "agora-$build: post notice $notice, announce nudge $nudge"
  [ "$nudge" != "0" ] || { echo "the $build build has no announce nudge, so no arm would open a thread at all" >&2; exit 1; }
  case $build in
    before) [ "$notice" = "0" ] || { echo "the before build already has the notice under test" >&2; exit 1; } ;;
    after)  [ "$notice" != "0" ] || { echo "the after build is missing the notice under test" >&2; exit 1; } ;;
  esac
done

# The standing rule as docs/setup.md hands it to somebody, taken from each tree so this measures the shipped
# wording. The paste block is the fenced md under "The standing rule": one place, and the arms differ in it.
extract_rule() {
  python3 - "$1/docs/setup.md" <<'PY'
import re, sys
text = open(sys.argv[1]).read()
blocks = re.findall(r"```md\n(.*?)```", text, re.S)
rules = [b for b in blocks if "agora post <thread>" in b]
if len(rules) != 1:
    sys.exit(f"found {len(rules)} candidate standing-rule blocks in {sys.argv[1]}, want exactly 1")
sys.stdout.write(rules[0])
PY
}
extract_rule "$SANDBOX/before-src" > "$SANDBOX/rule-before.md"
extract_rule "$REPO_ROOT" > "$SANDBOX/rule-after.md"
if cmp -s "$SANDBOX/rule-before.md" "$SANDBOX/rule-after.md"; then
  echo "the standing rule is identical in both trees, so the wording arms would measure nothing" >&2
  exit 1
fi
echo "the standing rule differs:"
diff -u "$SANDBOX/rule-before.md" "$SANDBOX/rule-after.md" | sed 's/^/  /' || true

# One repository per run, and a fixture with two independent bugs: the parser panic for turns 1 and 2, and a
# config path for turn 3, which is the work an agent is *right* to give its own thread.
write_fixture() {
  local dir=$1
  mkdir -p "$dir"
  cat > "$dir/go.mod" <<'GO'
module demo

go 1.26
GO
  cat > "$dir/parser.go" <<'GO'
package demo

import "strings"

// Parse returns the first token of input.
func Parse(input string) string {
	fields := strings.Fields(input)
	return fields[0]
}
GO
  cat > "$dir/config.go" <<'GO'
package demo

import (
	"os"
	"path/filepath"
)

// ConfigPath returns where the config file lives.
func ConfigPath() string {
	return filepath.Join(os.Getenv("HOME"), ".config", "demo", "config.toml")
}
GO
}

# The wrapper is instrumentation: it records what a hook was handed and what it emitted, and forwards both
# unchanged. Without it a hook that stayed quiet and one that never ran look the same.
cat > "$SANDBOX/hook.sh" <<'EOF'
#!/bin/sh
set -u
log=$1; db=$2; member=$3; channel=$4; bin=$5; shift 5
# Set here rather than inherited, because nothing promises a harness passes its environment to a hook. The
# channel is named rather than derived: git resolves symlinks and a harness does not, so on darwin a workspace
# at /tmp/x is one channel key from the agent and /private/tmp/x from anything that asked git.
export AGORA_AGENT=codex AGORA_DB=$db AGORA_MEMBER=$member AGORA_CHANNEL=$channel PATH=$bin:$PATH
payload=$(cat)
printf '%s\n' "$payload" >> "$log.in"
err=$(mktemp)
out=$(printf '%s' "$payload" | "$@" 2>"$err"); code=$?
printf '%s\n' "$code" >> "$log.code"
if [ -s "$err" ]; then cat "$err" >> "$log.err"; cat "$err" >&2; fi
if [ -n "$out" ]; then printf '%s\n' "$out" >> "$log.out"; printf '%s' "$out"; fi
exit $code
EOF
chmod +x "$SANDBOX/hook.sh"

# The channel's seq before a turn, so each message can be attributed to the turn that wrote it. Attribution is
# the whole measurement: "three threads" says nothing without which turn opened which.
max_seq() {
  "$1" --db "$2" --channel "$3" dump \
    | python3 -c 'import json,sys; print(max([m["seq"] for m in json.load(sys.stdin)] or [0]))'
}

run_arm() {
  local arm=$1 run=$2
  local id=$arm-$run
  local dir=$SANDBOX/$id
  local repo=$dir/repo
  local home=$dir/home
  local log=$dir/log
  local db=$dir/channel/agora.db
  local member=codex-$arm$run
  # Named rather than derived from the repository, so every side of this agrees on one key: git resolves
  # symlinks and a harness does not, and on darwin /tmp is a symlink to /private/tmp.
  local channel=demo-$id
  local agora src
  case $arm in
    before|wording) agora=$SANDBOX/agora-before ;;
    notice|after)   agora=$SANDBOX/agora-after ;;
  esac
  case $arm in
    before|notice) src=$SANDBOX/before-src ;;
    wording|after) src=$REPO_ROOT ;;
  esac

  echo
  echo "=== $id  (binary $(basename "$agora"), wording $(basename "$src"))"
  # fakehome is empty on purpose: $HOME/.agents is read whatever CODEX_HOME says, and this developer's own
  # AGENTS.md there tells every agent to coordinate through agora, which would put the variable in both arms.
  mkdir -p "$repo" "$home/skills" "$log" "$dir/channel" "$dir/bin" "$dir/zdotdir" "$dir/fakehome"
  ln -sf "$agora" "$dir/bin/agora"
  # The arm's own build under the name an agent types, and the member pinned so three turns are one member.
  for f in .zshenv .zprofile .zshrc; do
    printf 'export PATH=%s/bin:$PATH\nexport AGORA_DB=%s\nexport AGORA_MEMBER=%s\nexport AGORA_CHANNEL=%s\nexport AGORA_AGENT=codex\n' \
      "$dir" "$db" "$member" "$channel" > "$dir/zdotdir/$f"
  done
  # The check whose absence produced two invalid runs of the Codex experiment: ask the shell codex will use
  # which agora an agent typing `agora` actually gets.
  local on_path
  on_path=$(ZDOTDIR=$dir/zdotdir PATH="$dir/bin:$PATH" "$LOGIN_SHELL" -lc 'command -v agora' || true)
  if [ "$on_path" != "$dir/bin/agora" ]; then
    echo "  INVALID: an agent here would get \"$on_path\", not $dir/bin/agora"
    echo invalid > "$dir/invalid"
    return
  fi

  write_fixture "$repo"
  # The standing rule, and nothing else in the file: anything extra is in both arms or it is a second variable.
  { echo "# demo"; echo; cat "$SANDBOX/rule-$( [ "$src" = "$REPO_ROOT" ] && echo after || echo before ).md"; } \
    > "$repo/AGENTS.md"
  cp -R "$src/skills/agora" "$home/skills/agora"
  cp "$HOME/.codex/auth.json" "$home/auth.json"
  ( cd "$repo" && git init -q -b main && git add . \
    && git -c user.email=sandbox@example.invalid -c user.name=sandbox commit -qm fixture )

  # A person in the roster and no threads, so every thread afterwards was opened by the agent. inject stays
  # silent until a database exists, and somebody has to be there for a post to be worth writing.
  "$agora" --db "$db" --channel "$channel" --as chancez join \
    --description "reviewing the parser rewrite" >/dev/null

  # The sandbox in config rather than on the command line, because `codex exec resume` takes neither -s nor
  # --add-dir: turn 1 with flags and turns 2 and 3 without would be three turns under two sandboxes. Top-level
  # keys first, since anything after a table header belongs to that table.
  #
  # The channel is named as a writable root because it sits outside the workspace on purpose, and every agora
  # command needs to write beside the database: sqlite creates its WAL sidecars to open the file at all, so even
  # `agora threads` fails against a read-only directory.
  cat > "$home/config.toml" <<EOF
sandbox_mode = "workspace-write"
approval_policy = "never"

[sandbox_workspace_write]
writable_roots = ["$dir/channel"]

EOF
  # Injection only, identical in every arm. It is what gets the first thread opened, and it is not the variable:
  # its nudge stops the moment the member posts, which is exactly one turn before this experiment starts.
  cat >> "$home/config.toml" <<EOF
[[hooks.SessionStart]]
[[hooks.SessionStart.hooks]]
type = "command"
command = "$SANDBOX/hook.sh $log/session-start $db $member $channel $dir/bin $agora inject"

[[hooks.UserPromptSubmit]]
[[hooks.UserPromptSubmit.hooks]]
type = "command"
command = "$SANDBOX/hook.sh $log/user-prompt $db $member $channel $dir/bin $agora inject"
EOF

  local turn prompt
  for turn in 1 2 3; do
    case $turn in
      1) prompt=$TURN1 ;;
      2) prompt=$TURN2 ;;
      3) prompt=$TURN3 ;;
    esac
    max_seq "$agora" "$db" "$channel" > "$dir/seq-before-$turn"
    # Every .go file rather than the two planted ones, because turn 2's work is a file that did not exist.
    ( cd "$repo" && shasum -a 256 ./*.go | sort > "$dir/files-before-$turn" )
    local -a codex_args=(exec --json --dangerously-bypass-hook-trust)
    if [ "$turn" != 1 ]; then
      # Resume rather than a fresh session, since the failure lives at a turn boundary inside one session.
      codex_args=(exec resume --last --json --dangerously-bypass-hook-trust)
    fi
    if ! ( cd "$repo" && CODEX_HOME=$home HOME=$dir/fakehome ZDOTDIR=$dir/zdotdir \
        PATH="$dir/bin:$PATH" AGORA_DB=$db AGORA_MEMBER=$member AGORA_AGENT=codex \
        codex "${codex_args[@]}" "$prompt" > "$dir/turn$turn.out" 2> "$dir/turn$turn.err" ); then
      echo "  INVALID: codex exited nonzero on turn $turn"
      sed 's/^/    /' "$dir/turn$turn.err" | head -5
      echo invalid > "$dir/invalid"
      return
    fi
    if [ ! -s "$dir/turn$turn.out" ]; then
      echo "  INVALID: no transcript for turn $turn"
      echo invalid > "$dir/invalid"
      return
    fi
    ( cd "$repo" && shasum -a 256 ./*.go | sort > "$dir/files-after-$turn" )
    max_seq "$agora" "$db" "$channel" > "$dir/seq-after-$turn"
    printf '  turn %s: ' "$turn"
    if cmp -s "$dir/files-before-$turn" "$dir/files-after-$turn"; then
      printf 'EDITED NOTHING, '
    else
      printf 'edited, '
    fi
    printf 'channel %s -> %s\n' "$(cat "$dir/seq-before-$turn")" "$(cat "$dir/seq-after-$turn")"
  done

  # Validity: the work has to have happened, or the channel evidence is about an agent that did nothing.
  if cmp -s "$dir/files-before-1" "$dir/files-after-1"; then
    echo "  INVALID: turn 1 touched neither file, so it never started the work"
    echo invalid > "$dir/invalid"
    return
  fi
  if ! ls "$repo"/*_test.go >/dev/null 2>&1; then
    echo "  INVALID: no test file after turn 2, so the continuation never happened"
    echo invalid > "$dir/invalid"
    return
  fi
  if cmp -s "$dir/files-before-3" "$dir/files-after-3"; then
    echo "  INVALID: turn 3 changed nothing, so the separate work never happened"
    echo invalid > "$dir/invalid"
    return
  fi

  "$agora" --db "$db" --channel "$channel" dump > "$dir/dump.json"
  "$agora" --db "$db" --channel "$channel" claims > "$dir/claims.json"
  python3 - "$dir" "$member" > "$dir/score" <<'PY'
import json, sys
dir, member = sys.argv[1], sys.argv[2]
messages = [m for m in json.load(open(f"{dir}/dump.json")) if m["author"] == member]
claims = [c for c in json.load(open(f"{dir}/claims.json")) if c["holder"] == member]
authors = {m["author"] for m in json.load(open(f"{dir}/dump.json"))} - {"chancez"}


def turn_of(seq):
    for turn in (1, 2, 3):
        lo = int(open(f"{dir}/seq-before-{turn}").read())
        hi = int(open(f"{dir}/seq-after-{turn}").read())
        if lo < seq <= hi:
            return turn
    return 0


threads = {1: set(), 2: set(), 3: set(), 0: set()}
for m in messages:
    threads[turn_of(m["seq"])].add(m["thread"])
# A claim has no seq, so it counts towards the turn only through the thread it names, which is enough: a claim
# on a thread nobody posted in is still a thread this member opened.
opened = set().union(*threads.values()) | {c["thread"] for c in claims}
new_in_2 = threads[2] - threads[1]
new_in_3 = threads[3] - threads[1] - threads[2]
if len(authors) > 1:
    verdict = "IDENTITY SPLIT: " + ",".join(sorted(authors))
elif not threads[1]:
    verdict = "invalid: opened nothing in turn 1"
elif not threads[2]:
    verdict = "silent in turn 2"
elif new_in_2:
    verdict = "SPLIT: " + ",".join(sorted(new_in_2))
else:
    verdict = "continued"
json.dump({
    "verdict": verdict,
    "separated": bool(new_in_3),
    "threads": {str(t): sorted(v) for t, v in threads.items() if v},
    "opened": sorted(opened),
    "messages": len(messages),
}, sys.stdout)
PY
  python3 - "$dir/score" <<'PY'
import json, sys
s = json.load(open(sys.argv[1]))
print(f"  turn 2: {s['verdict']}")
print(f"  turn 3: {'opened its own thread' if s['separated'] else 'NO new thread for separate work'}")
print(f"  threads by turn: {s['threads']}, {s['messages']} messages")
PY
  # Three prompts is three UserPromptSubmit events. A resumed turn that fired none had the channel out of
  # context for that turn, which is a different experiment than the one being run.
  for f in "$log"/*.code; do
    [ -e "$f" ] || continue
    local label
    label=$(basename "$f" .code)
    echo "  hook $label ran $(wc -l < "$f" | tr -d ' ') time(s)," \
      "$( [ -s "$log/$label.out" ] && echo "said something $(wc -l < "$log/$label.out" | tr -d ' ') time(s)" || echo "said nothing" )"
  done
  "$agora" --db "$db" --channel "$channel" dump --text | sed 's/^/    /' | head -24
}

if [ "${PREFLIGHT_ONLY:-0}" != 0 ]; then
  # Every check above this line runs without a model call, which is the part worth rerunning after an edit.
  echo
  echo "preflight only, no sessions started"
  exit 0
fi
[ "$RUNS" -ge 1 ] || { echo "RUNS must be at least 1, or PREFLIGHT_ONLY=1 to check the wiring" >&2; exit 1; }

for run in $(seq 1 "$RUNS"); do
  for arm in $ARMS; do run_arm "$arm" "$run"; done
done

echo
echo "=== verdict"
echo "arm      continued  split  silent  invalid  separated-turn-3"
for arm in $ARMS; do
  python3 - "$SANDBOX" "$arm" "$RUNS" <<'PY'
import json, os, sys
sandbox, arm, runs = sys.argv[1], sys.argv[2], int(sys.argv[3])
counts = {"continued": 0, "split": 0, "silent": 0, "invalid": 0}
separated = 0
for run in range(1, runs + 1):
    dir = f"{sandbox}/{arm}-{run}"
    if os.path.exists(f"{dir}/invalid") or not os.path.exists(f"{dir}/score"):
        counts["invalid"] += 1
        continue
    s = json.load(open(f"{dir}/score"))
    v = s["verdict"]
    if v == "continued":
        counts["continued"] += 1
    elif v.startswith("SPLIT"):
        counts["split"] += 1
    elif v.startswith("silent"):
        counts["silent"] += 1
    else:
        counts["invalid"] += 1
    separated += bool(s["separated"])
print(f"{arm:<8} {counts['continued']:>9}  {counts['split']:>5}  {counts['silent']:>6}  "
      f"{counts['invalid']:>7}  {separated:>16}")
PY
done
echo
echo "continued: turn 2 went into the thread turn 1 opened, which is the behaviour under test."
echo "separated: turn 3 opened its own thread, which is correct and is what a change must not suppress."
echo "transcripts, channels and hook logs are under $SANDBOX"
