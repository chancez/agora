#!/usr/bin/env bash
# Does a recorded link reach an agent that would otherwise not have followed the pointer?
#
# The sixth experiment. scripts/thread-experiment.sh measured that an agent opens a thread per site whatever it
# is told, and that the standing rule gets it to *name* the new thread in the one it came out of. That pointer was
# prose, so only a person could follow it. Naming a thread now records a link, and this is whether that changes
# what an agent does.
#
# The plant is built around the one thing that makes the question answerable. `agora inject` already names every
# *unread* thread with its oldest message, so a planted thread the member has not read arrives in the briefing
# whether links exist or not, and an arm that found it would have found it anyway. So the thread carrying the
# finding is **already read**: its cursor is advanced before the session starts, which is the situation the
# pointer exists for. An agent triaged a thread as not its own, then started work that turns out to be exactly
# that thread's subject.
#
#   lexer-panic    the root cause, and a claim on it. Read already, so the briefing will not mention it.
#   parser-panic   unread, and it names lexer-panic in prose, so the briefing does deliver this one.
#
# The task is the narrow symptom. parser.go, lex.go and format.go all index strings.Fields()[0] with no length
# check, and the planted note says a guard in Parse alone hides the other two, which is this project's motivating
# failure: a fix scoped to the one symptom its author saw.
#
#   before   the last build without links, where the pointer is prose and nothing surfaces it
#   after    the working tree, where `agora read --thread parser-panic` names lexer-panic and its unread
#
# The binary and the skill move together, because they ship together and an agent gets both. If this separates
# the arms, which half did it is the next experiment rather than this one.
#
# The evidence is the files. A guard in Parse alone is the failure; all three sites, or stopping to ask, is the
# finding having arrived. The transcript is read for whether lexer-panic was opened at all, which is the
# mechanism, but the files are what count: injected context never appears on screen.
#
# **This is a hard experiment and the expectation is written down before running it.** The control can reach the
# information two ways: `agora threads` lists every thread, read or not, and the briefing quotes parser-panic's
# unread message, which *is* the pointer, so both arms are told the name lexer-panic in prose. What the link adds
# is therefore not reach but structure: a field, with the unread count, on the thread the agent is already
# reading. So a null here means the link is redundant *for a pointer that is already in context*, which is worth
# knowing and is not the same as pointers going nowhere. The case where it would add reach, and which this does
# not test, is a pointer nothing quotes: an old message in a long thread, or a channel with more unread threads
# than the briefing's limit.
#
# The one leak that would make it measure nothing is the briefing carrying the *finding* rather than the pointer,
# which is checked per run and printed, not reasoned about. It caught a claim: agora reports standing claims on
# SessionStart with their notes and paths, so a claim covering all three files handed every arm the answer.
#
# Everything Codex needs is copied from scripts/thread-experiment.sh, including its four traps. This spawns real
# Codex sessions and costs tokens, and never opens the real channel.
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
RUNS=${RUNS:-3}
ARMS=${ARMS:-"before after"}
# The commit before links, so `before` is the shipped previous thing rather than a description of it.
BEFORE_REF=${BEFORE_REF:-03b5717}
LOGIN_SHELL=${LOGIN_SHELL:-${SHELL:-/bin/sh}}
TASK="parser.go panics on empty input. Fix it."

command -v codex >/dev/null || { echo "codex is not on PATH" >&2; exit 1; }
[ -f "$HOME/.codex/auth.json" ] || { echo "no ~/.codex/auth.json: run codex login first" >&2; exit 1; }
command -v python3 >/dev/null || { echo "python3 is not on PATH" >&2; exit 1; }

SANDBOX=$(mktemp -d /tmp/agoralink.XXXXXX)
echo "sandbox: $SANDBOX"

git -C "$REPO_ROOT" worktree add -q "$SANDBOX/before-src" "$BEFORE_REF"
trap 'git -C "$REPO_ROOT" worktree remove --force "$SANDBOX/before-src" 2>/dev/null || true' EXIT
( cd "$SANDBOX/before-src" && go build -o "$SANDBOX/agora-before" ./cmd/agora )
( cd "$REPO_ROOT" && go build -o "$SANDBOX/agora-after" ./cmd/agora )

# Prove the arms differ before spending tokens, on the layer under test rather than on any string in the binary.
MARKER=${MARKER:-'related: '}
for build in before after; do
  found=$(strings "$SANDBOX/agora-$build" | grep -c "$MARKER" || true)
  echo "agora-$build: layer under test $found"
  case $build in
    before) [ "$found" = "0" ] || { echo "the before build already links threads, so BEFORE_REF is wrong" >&2; exit 1; } ;;
    after)  [ "$found" != "0" ] || { echo "the after build does not link threads" >&2; exit 1; } ;;
  esac
done

# And prove the plant works, without a model: the before build must not surface the link and the after build must,
# from the same channel. This is the check that would have caught a plant whose pointer nothing could follow.
verify_plant() {
  local agora=$1 db=$2 channel=$3 member=$4 expect=$5
  local out
  out=$("$agora" --db "$db" --channel "$channel" --as "$member" --text read --thread parser-panic)
  case $expect in
    linked) case "$out" in *"related: lexer-panic"*) ;; *) echo "the after build did not surface the link:"; echo "$out"; return 1 ;; esac ;;
    prose)  case "$out" in *"related: lexer-panic"*) echo "the before build surfaced a link it cannot have"; return 1 ;; esac ;;
  esac
  # Either way the pointer is in the message, or this measures a plant with nothing to follow.
  case "$out" in *lexer-panic*) ;; *) echo "the plant does not name lexer-panic at all:"; echo "$out"; return 1 ;; esac
}

read -r -d '' PARSER <<'GO' || true
package demo

import "strings"

// Parse returns the first token of input.
func Parse(input string) string {
	fields := strings.Fields(input)
	return fields[0]
}
GO
read -r -d '' LEX <<'GO' || true
package demo

import "strings"

// Lex returns the last token of input.
func Lex(input string) string {
	fields := strings.Fields(input)
	return fields[len(fields)-1]
}
GO
read -r -d '' FORMAT <<'GO' || true
package demo

import "strings"

// Format renders input with its first token capitalised.
func Format(input string) string {
	fields := strings.Fields(input)
	return strings.ToUpper(fields[0]) + " " + strings.Join(fields[1:], " ")
}
GO

cat > "$SANDBOX/hook.sh" <<'EOF'
#!/bin/sh
set -u
log=$1; db=$2; member=$3; channel=$4; bin=$5; shift 5
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

run_arm() {
  local arm=$1 run=$2
  local id=$arm-$run
  local dir=$SANDBOX/$id
  local repo=$dir/repo
  local home=$dir/home
  local log=$dir/log
  local db=$dir/channel/agora.db
  local member=codex-$arm$run
  local channel=demo-$id
  local agora=$SANDBOX/agora-$arm
  local src=$SANDBOX/before-src
  [ "$arm" = after ] && src=$REPO_ROOT

  echo
  echo "=== $id  (binary agora-$arm, skill $(basename "$src"))"
  mkdir -p "$repo" "$home/skills" "$log" "$dir/channel" "$dir/bin" "$dir/zdotdir" "$dir/fakehome"
  ln -sf "$agora" "$dir/bin/agora"
  for f in .zshenv .zprofile .zshrc; do
    printf 'export PATH=%s/bin:$PATH\nexport AGORA_DB=%s\nexport AGORA_MEMBER=%s\nexport AGORA_CHANNEL=%s\nexport AGORA_AGENT=codex\n' \
      "$dir" "$db" "$member" "$channel" > "$dir/zdotdir/$f"
  done
  local on_path
  on_path=$(ZDOTDIR=$dir/zdotdir PATH="$dir/bin:$PATH" "$LOGIN_SHELL" -lc 'command -v agora' || true)
  if [ "$on_path" != "$dir/bin/agora" ]; then
    echo "  INVALID: an agent here would get \"$on_path\", not $dir/bin/agora"
    echo invalid > "$dir/invalid"
    return
  fi

  printf '%s\n' "$PARSER" > "$repo/parser.go"
  printf '%s\n' "$LEX" > "$repo/lex.go"
  printf '%s\n' "$FORMAT" > "$repo/format.go"
  printf 'module demo\n\ngo 1.26\n' > "$repo/go.mod"
  # The standing rule, the same text in both arms, taken from each tree the way the skill is.
  python3 - "$src/docs/setup.md" > "$repo/AGENTS.md" <<'PY'
import re, sys
text = open(sys.argv[1]).read()
blocks = [b for b in re.findall(r"```md\n(.*?)```", text, re.S) if "agora post <thread>" in b]
if len(blocks) != 1:
    sys.exit(f"found {len(blocks)} standing-rule blocks in {sys.argv[1]}, want 1")
sys.stdout.write("# demo\n\n" + blocks[0])
PY
  cp -R "$src/skills/agora" "$home/skills/agora"
  cp "$HOME/.codex/auth.json" "$home/auth.json"
  ( cd "$repo" && git init -q -b main && git add . \
    && git -c user.email=sandbox@example.invalid -c user.name=sandbox commit -qm fixture )

  # The plant. lexer-panic carries the finding and a claim; parser-panic names it and is left unread.
  local plant=("$agora" --db "$db" --channel "$channel")
  "${plant[@]}" --as alice join --description "fixing the empty-input panic across the three call sites" >/dev/null
  "${plant[@]}" --as alice post lexer-panic \
    "The panic is not local to Parse. strings.Fields returns an empty slice for whitespace-only input and three call sites index it without a length check: Parse, Lex and Format. A guard in Parse alone hides two of the three, and the next reader will think this is fixed." >/dev/null
  # No claim on it, though the scenario would carry one. inject reports standing claims on SessionStart with
  # their notes and paths, so a claim covering parser.go, lex.go and format.go would hand the finding to *both*
  # arms and the control would reach it without any link. Caught by printing the briefing below rather than by
  # reasoning about it, and it is the first trap in AGENTS.md: a control that can get there another way measures
  # nothing. Zero claims exist across every real channel anyway.
  "${plant[@]}" --as alice post parser-panic \
    "Somebody asked about the Parse panic specifically. The root cause and what it takes to fix it are in lexer-panic." >/dev/null
  # Already triaged, which is the state that makes this a measurement: a thread with nothing unread is not in the
  # briefing, so the only thing in context that can reach it is the pointer from parser-panic.
  "${plant[@]}" --as "$member" ack lexer-panic >/dev/null
  if [ "$(("$("${plant[@]}" --as "$member" threads --unread | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')"))" != 1 ]; then
    echo "  INVALID: the plant does not leave exactly one unread thread"
    "${plant[@]}" --as "$member" --text threads | sed 's/^/    /'
    echo invalid > "$dir/invalid"
    return
  fi
  local expect=prose
  [ "$arm" = after ] && expect=linked
  if ! verify_plant "$agora" "$db" "$channel" "$member" "$expect" | sed 's/^/  /'; then
    echo "  INVALID: the plant is not what this arm is supposed to measure"
    echo invalid > "$dir/invalid"
    return
  fi

  # What the briefing will actually say, which is the check that cannot be reasoned about: anything it carries,
  # both arms get. The *name* lexer-panic is in it, because the briefing quotes parser-panic's unread message and
  # that message is the pointer. That is the control condition rather than a leak, and it is why this experiment
  # is a hard one: see the header. What must not be in it is the finding itself, since an arm that is told the
  # other two call sites needs no thread and no link.
  printf '{"hook_event_name":"SessionStart","cwd":"%s","session_id":"plant"}' "$repo" \
    | AGORA_DB=$db AGORA_CHANNEL=$channel AGORA_MEMBER=$member "$agora" inject \
    | python3 -c 'import json,sys; raw=sys.stdin.read(); print(json.loads(raw)["hookSpecificOutput"]["additionalContext"] if raw.strip() else "(the briefing says nothing)")' \
    > "$dir/briefing.txt"
  for leak in lex.go format.go "three call sites"; do
    if grep -qF "$leak" "$dir/briefing.txt"; then
      echo "  INVALID: the briefing carries the finding itself ($leak), so no arm needs the pointer"
      sed 's/^/    /' "$dir/briefing.txt"
      echo invalid > "$dir/invalid"
      return
    fi
  done
  if ! grep -q parser-panic "$dir/briefing.txt"; then
    echo "  INVALID: the briefing does not deliver parser-panic, so there is no pointer to follow"
    echo invalid > "$dir/invalid"
    return
  fi

  if [ "${PLANT_ONLY:-0}" != 0 ]; then
    # Everything above this line runs without a model call, and it is the half that was wrong twice in earlier
    # experiments: a plant that does not hold, or that both arms can reach the same way.
    echo "  plant verified, no session started"
    "$agora" --db "$db" --channel "$channel" --as "$member" --text threads | sed 's/^/    /'
    echo "  the briefing this session will get:"
    sed 's/^/      /' "$dir/briefing.txt"
    return
  fi

  cat > "$home/config.toml" <<EOF
sandbox_mode = "workspace-write"
approval_policy = "never"

[sandbox_workspace_write]
writable_roots = ["$dir/channel"]

[[hooks.SessionStart]]
[[hooks.SessionStart.hooks]]
type = "command"
command = "$SANDBOX/hook.sh $log/session-start $db $member $channel $dir/bin $agora inject"

[[hooks.UserPromptSubmit]]
[[hooks.UserPromptSubmit.hooks]]
type = "command"
command = "$SANDBOX/hook.sh $log/user-prompt $db $member $channel $dir/bin $agora inject"
EOF

  if ! ( cd "$repo" && CODEX_HOME=$home HOME=$dir/fakehome ZDOTDIR=$dir/zdotdir \
      PATH="$dir/bin:$PATH" AGORA_DB=$db AGORA_MEMBER=$member AGORA_CHANNEL=$channel AGORA_AGENT=codex \
      codex exec --json --dangerously-bypass-hook-trust "$TASK" \
      > "$dir/out.json" 2> "$dir/err.txt" ); then
    echo "  INVALID: codex exited nonzero"
    sed 's/^/    /' "$dir/err.txt" | head -5
    echo invalid > "$dir/invalid"
    return
  fi
  if [ ! -s "$dir/out.json" ]; then
    echo "  INVALID: no transcript"
    echo invalid > "$dir/invalid"
    return
  fi

  "$agora" --db "$db" --channel "$channel" dump > "$dir/dump.json"
  python3 - "$dir" "$member" > "$dir/score" <<'PY'
import json, os, re, sys
dir, member = sys.argv[1], sys.argv[2]
guarded = {}
for name in ("parser.go", "lex.go", "format.go"):
    text = open(f"{dir}/repo/{name}").read()
    # A guard is any length check on the slice the panic comes from. Deliberately loose: what matters is whether
    # the site was touched at all, and the compiler and the agent's own tests cover whether it works.
    guarded[name] = bool(re.search(r"len\(fields\)|== 0|!= 0|\bIsEmpty\b|TrimSpace", text))
opened = False
for line in open(f"{dir}/out.json"):
    try:
        event = json.loads(line)
    except ValueError:
        continue
    item = event.get("item", {})
    if item.get("type") == "command_execution" and "lexer-panic" in item.get("command", ""):
        opened = True
posted = [m["thread"] for m in json.load(open(f"{dir}/dump.json")) if m["author"] == member]
sites = sum(1 for ok in guarded.values() if ok)
if not guarded["parser.go"]:
    verdict = "invalid: never fixed the symptom it was asked about"
elif sites == 3:
    verdict = "all three sites"
elif sites > 1:
    verdict = f"{sites} of 3 sites"
else:
    verdict = "PARSE ALONE"
json.dump({
    "verdict": verdict, "sites": sites, "guarded": guarded,
    "opened_lexer_panic": opened, "posted_to": sorted(set(posted)),
}, sys.stdout)
PY
  python3 - "$dir/score" <<'PY'
import json, sys
s = json.load(open(sys.argv[1]))
print(f"  {s['verdict']}, read lexer-panic: {'yes' if s['opened_lexer_panic'] else 'NO'}"
      f", posted to {s['posted_to'] or 'nothing'}")
PY
  for f in "$log"/*.code; do
    [ -e "$f" ] || continue
    local label
    label=$(basename "$f" .code)
    echo "  hook $label ran $(wc -l < "$f" | tr -d ' ') time(s)," \
      "$( [ -s "$log/$label.out" ] && echo "said something" || echo "said nothing" )"
  done
}

if [ "${PREFLIGHT_ONLY:-0}" != 0 ]; then
  echo
  echo "preflight only, no sessions started"
  exit 0
fi

for run in $(seq 1 "$RUNS"); do
  for arm in $ARMS; do run_arm "$arm" "$run"; done
done

echo
echo "=== verdict"
echo "arm      all three  partial  parse alone  invalid  read lexer-panic"
for arm in $ARMS; do
  python3 - "$SANDBOX" "$arm" "$RUNS" <<'PY'
import json, os, sys
sandbox, arm, runs = sys.argv[1], sys.argv[2], int(sys.argv[3])
c = {"all": 0, "partial": 0, "alone": 0, "invalid": 0}
opened = 0
for run in range(1, runs + 1):
    d = f"{sandbox}/{arm}-{run}"
    if os.path.exists(f"{d}/invalid") or not os.path.exists(f"{d}/score"):
        c["invalid"] += 1
        continue
    s = json.load(open(f"{d}/score"))
    if s["verdict"].startswith("invalid"):
        c["invalid"] += 1
    elif s["sites"] == 3:
        c["all"] += 1
    elif s["sites"] > 1:
        c["partial"] += 1
    else:
        c["alone"] += 1
    opened += bool(s["opened_lexer_panic"])
print(f"{arm:<8} {c['all']:>9}  {c['partial']:>7}  {c['alone']:>11}  {c['invalid']:>7}  {opened:>16}")
PY
done
echo
echo "all three sites, or stopping to ask, is the planted finding having arrived. PARSE ALONE is the failure"
echo "agora exists for: a fix scoped to the one symptom its author saw."
echo "transcripts, channels and hook logs are under $SANDBOX"
