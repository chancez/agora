# Working on agora

Notes for anyone, human or agent, making changes here. `docs/design.md` is the decision record and says
*what* agora is and why each piece was chosen; this file is about how to work on it without breaking
things.

agora is new. Unlike a mature repo, most of this file is not yet a lesson from an incident, and where a
rule has no incident behind it that is said plainly rather than dressed up. Add the incident when you
find one, because a rule without its reason gets dropped the first time it is inconvenient.

## What agora is, briefly

A shared, durable channel where independent coding agents coordinate: post findings, read what they
missed, and claim ownership of work so two of them do not fix the same bug twice.

The motivating failure, worth keeping in mind because it is the test for whether a change helps: two
agents work the same repository in separate worktrees, both hit the same underlying bug, both write a
fix, one lands, and the fix that landed is scoped to the one symptom its author saw, so the
investigation closes with the root cause still there.

Three properties make that avoidable, and point-to-point messaging between two agents provides none of
them: a **record** a later agent can read, an **ordering** every participant agrees on, and
**ownership** that exists somewhere other than in two agents' separate beliefs. Ownership is the one
that looks like messaging and is not: it is consensus, which is why agora is a log and not a mesh.

Read `docs/design.md` before a first change. It records what was measured, what was rejected, and why,
and it is short.

## Status: everything but the external nudge

Built and tested: `internal/store`, `internal/config`, `internal/tui`, `cmd/agora`, the skill in
`skills/agora`, and the hooks, `agora doorbell` included. `docs/setup.md` is the wiring, `demo/` is
runnable, and the scripts in `scripts/` rerun the experiments the design rests on.

Not built: `internal/notify`, the optional nudge that shells out to a configured command such as
`cm send`. `members.notify` records it and nothing invokes it yet. It stayed last because the doorbell
covers the case it was for without depending on another tool, so what is left of it is a harness that runs
no hooks at all.

Codex is wired too, in `docs/setup.md`, and every hook command runs there unchanged.
`scripts/codex-hook-experiment.sh` is its arm of the experiment and has been run: the table is in
`docs/design.md`. Read it before changing anything here, because it says something the Claude Code table does
not, which is that on Codex the skill moved an agent that had no hooks at all, and the layers are only visible on
an arm without the skill.

So `docs/design.md` describes rather than specifies, except where it marks something deferred. "Must" here
means there is already a test, and that test is where you will hear about it.

## Setup, build, test

```
mise install
mise run build      # -> bin/agora
mise run check      # fmt, vet, test
mise run test-race  # required before believing a claims or cursor change
```

No cgo. `modernc.org/sqlite` is the pure-Go driver, chosen so agora stays a static binary, and keeping
it that way is a constraint rather than an accident: a hook in the edit path must run everywhere without
a toolchain.

## CI

`.github/workflows/test.yml` runs gofmt, vet and the suite under `-race` once per platform, on Linux and
macOS, plus two things a developer's machine does not:

- **Every default path is pointed at an empty directory, and a later step fails if anything was written
  there.** That is this file's "never test against a channel someone is using", enforced instead of
  trusted: a test that forgets to isolate itself reads the real channel of whoever ran it, and a suite
  that passes that way passed for the wrong reason.
- **Cross-compiling all four release targets**, which is the no-cgo constraint checked rather than assumed.

`-count=1` is load bearing there. `setup-go`'s cache restores `GOCACHE`, and test results live in it, so
without that flag a run replays `(cached)` for every package in a fifth of a second and reports success. Every
run of this workflow did exactly that before it was noticed, the isolation check included, which passed because
nothing had executed to write anywhere.

`.github/workflows/release.yml` builds on a `v*` tag, one native runner per target so each artifact can be
run before it is published, stamps the tag with `-ldflags -X main.version`, and refuses to publish if the
binary does not report it or the checkout is dirty. The archive carries the binary, `README.md`,
`docs/setup.md` and the skill, because somebody who downloads a release should not have to clone the
repository to wire an agent up.

**The scripts in `scripts/` are not in CI.** Each starts real agent sessions through the `claude` or `codex` CLI,
which costs tokens and needs credentials, and what they measure is whether an agent behaves differently
rather than whether the code works. Run them by hand when the thing they measure changes, and put the
numbers in `docs/design.md`.

`actionlint` reads both workflows if you have it; both were clean when they were added.

## The rule that matters most: no SQL outside internal/store

```
cmd/agora        thin: parse flags, call store, print JSON
internal/store   the only package that knows SQL
internal/notify  the optional nudge, invoking a configured command
internal/tui     the view, and the participant keys, calls store
```

Every command goes through a method on one type. `internal/store` takes and returns domain types, never
`*sql.Rows` and never a transaction handle.

This is load-bearing rather than tidy. agora deliberately has no server yet, and the measurements behind
that are in `docs/design.md`, but a server is expected eventually. It stays a refactor rather than a
rewrite only while nothing above `internal/store` knows how state is stored. The moment a command builds
its own query, that option is gone.

`store.Watch(ctx, WatchOptions) *Watcher` is the seam that matters most, because it is the one method
whose implementation is expected to change: a poll loop satisfies it today and a server subscription
satisfies it later, and no caller should be able to tell which it got. It hands back a subscription
rather than a bare channel only so that a failure can be told apart from silence.

## Coordinating with other agents

This repository uses agora, a shared channel of threads. Several agents work it at once, and this is the
suggested section from `docs/setup.md`, kept here because a rule the project does not follow is not a
suggestion worth making. The agora skill has the protocol.

- Open a thread for each piece of work you start, before you edit: `agora post <thread> "what you are about
  to do"`, then `agora claim <thread> --note "..."`. One thread is one piece of work, not one turn: the next
  step of something you already announced goes in the thread that announced it, and `agora threads --mine`
  is what you have open.
- Read what is waiting first: `agora threads --unread`, then `agora read --thread NAME --advance` for one
  that concerns your work and `agora mute NAME` for one that does not, which stops it nudging you again.
- Post what you found when it changes what somebody else should do, and `agora release <thread>` when you
  stop.

## Never test against a channel someone is using

agora's whole purpose is to be read by other agents, which means a test that writes to the real database
posts to a channel real agents will act on. A stray `agora post` during development is a message someone
else's agent treats as a finding, and a stray `agora claim` blocks their edits.

So point every manual test at its own database:

```sh
export AGORA_DB=$(mktemp -d /tmp/agoradev.XXXX)/agora.db
export AGORA_CHANNEL=devtest
```

Prove it with `agora config`, which reports the resolved path **and where it came from** without creating
it. Anything naming the real `$XDG_DATA_HOME` path is not isolated, and `database_exists` already true on a
throwaway path means the path is not throwaway.

The trap this is guarding against, from cm: an *empty* environment variable reads as unset and falls
through to the default, so `AGORA_DB=` looks like isolation and is not. An entire test suite there read
the developer's real config for weeks while appearing isolated, and nothing failed because no test
asserted on it. agora now rejects an empty variable, whitespace included, but point it at a path that does
not exist rather than leaving it empty.

Never delete a channel or release a claim you did not create. A claim held by a session that looks idle
may belong to an agent waiting on its user.

## Testing rules

**Every fix ships with a test that would have caught the bug.** Prefer the seam over end to end: most
behavior here is a `store` method, and a test that constructs the state directly is faster and more
precise than one driving the CLI.

**Assert the whole value a function returns, not individual fields.** A field-by-field check passes
while the rest of the struct is wrong.

**A test that never fails is worse than no test.** Verify a test fails with the fix reverted. Actually
revert it and watch, rather than reasoning that it would. When mutating code to check a test, confirm
the mutation compiles: a mutation that fails to build tests nothing, and `go test` reports the build
failure as a failure, which looks like success.

**Use `testing/synctest` for anything concurrent or timing dependent.** `Watch` is the obvious case: a
test that sleeps to wait for a poll interval is slow and flaky, and one that advances fake time is
neither.

**Claims and cursors need concurrent tests, not sequential ones.** These are the only places where two
agents race, so a sequential test proves nothing about the property that matters. The measurement in
`docs/design.md` is the shape to copy: 20 connections racing for one claim must produce exactly one
winner, one row, and no errors. Run it with `-race`.

**A flaky test is a bug until proven otherwise.** `-race` widens real windows rather than inventing
them.

## Where the hard part is

Not the schema. The hard part is whether an agent actually reads the channel, and it is the only thing
here that cannot be verified by `go test`.

`docs/design.md` records the experiment that settled the current design: two agents, identical task,
identical workspaces, a claim planted in a channel neither could read from disk, and the only difference
a hook injecting unread messages. The control patched the file; the hooked agent left it untouched and
stopped to ask. The **file states** were the evidence, not the transcripts.

`scripts/reply-experiment.sh` asks what an agent does with a thread addressed to it, and so far the answer
is that instructions do not move it. Read the table in `docs/design.md` before writing more prose at it.

`scripts/announce-experiment.sh` asks the other half: whether an agent opens a thread for the work it starts.
That one did move, and what moved it was state rather than wording: a hook that asks a member with nothing of
its own in the channel. Reach for a condition before reaching for more prose.

`scripts/codex-hook-experiment.sh` is the same experiment with a Codex agent, and its answer is different: given
the skill, every arm read the channel before touching anything, control included, so the hooks had nothing left
to move and only an arm without the skill shows the layers working. Two bugs fell out of running it that no test
had caught, both listed under "Things worth knowing up front". A harness that behaves differently is the reason
to run the arm rather than reason about it.

`scripts/doorbell-experiment.sh` asks whether an agent answers a message that arrives after its user stopped
talking to it. Its plant is posted from inside the turn, by a `PostToolUse` hook, which is the only reason it
measures anything: a message that exists before the prompt arrives by injection, and injection is what both
arms already have. What it cannot reach is a session parked at a prompt, since `claude -p` exits when its turn
ends, and that case is measured by hand in a pty.

Eight traps, because a wrong result here looks exactly like a right one:

- **The control must not be able to reach the information another way.** The first run of that experiment
  was invalid: the channel file sat in both workspaces, so the control found the claim by listing the
  directory rather than from a hook. It looked like a null result and proved nothing.
- **Injected context is invisible.** `SessionStart` output goes into the model's context, not the
  screen, so reading the terminal cannot confirm the hook fired. Judge by behavior and by files on disk.
- **Two agents, one task, one variable.** Anything that changes both workspaces changes the result.
- **An arm doing the right thing is not a null result.** A plant an agent is correct to dismiss measures the
  agent being right. Both experiments produced one.
- **A script that points at a binary it does not build measures whatever was last built.** `hook-experiment.sh`
  used `bin/agora` and produced two invalid runs in a row: an arm ran a flag that build did not have, so the
  hook exited 1 and the edit went through; and an agent told to coordinate found every `agora` command refusing
  a schema newer than the one on its `PATH` understood. Build it in the script, and put that build on the arms'
  `PATH` under the name an agent types.
- **An arm whose tool call was refused reads exactly like an arm that chose not to act.** Watched in
  `doorbell-experiment.sh`: an agent was woken, said it would answer, and invoked
  `/Users/<somebody>/.claude/skills/agora/agora`, a path it invented, which no permission rule covers and
  which nobody in a headless run is there to approve. Its channel was empty afterwards, which is the exact
  shape of the failure being measured. Scan the transcript for a refused call before scoring silence.
- **Isolating a harness's config directory does not isolate the developer.** `CODEX_HOME` does not cover
  `$HOME/.agents`, whose `AGENTS.md` told a control to coordinate through agora, and it does not cover the login
  shell's PATH, which put an installed build ahead of the arm's own and made two runs measure the wrong binary.
  A throwaway `$HOME`, a throwaway `ZDOTDIR`, and a preflight check that asks the shell which binary an agent
  would get are what isolate a Codex arm.
- **`--settings` adds to the developer's own settings rather than replacing them.** Every arm inherits every
  user-level hook, and one developer's wire agora itself: a second `inject` in each arm, and `leave --force`
  on `SessionEnd`, which released the claims an experiment was reading. `agora` on `PATH` is whatever is
  installed, some other commit than the arm. `CLAUDE_CONFIG_DIR` at a throwaway directory plus the arm's
  binary first on `PATH` is what isolates a run, and credentials come from the keychain so a fresh config dir
  still authenticates. The numbers measured before this was found are marked in `docs/design.md`.

Test hook and skill changes against throwaway agents in throwaway directories, never against agents
doing real work.

## Go conventions

- `new("string")` / `new(5)` rather than a `ptr()` helper or a temp variable plus `&`.
- Comment *why*, especially where a line looks wrong without its reason. Put the symptom in the comment,
  not just the mechanism: the next reader is someone seeing the symptom again.
- Plain ASCII everywhere, including commit messages. No em dashes, arrows, curly quotes, or decorative
  unicode.

## Interface conventions

**JSON in, JSON out, on every command.** Agents are the primary caller, so JSON is the default and
`--text` is the flag. Copied from `buzz-cli`, which does the same for the same reason.

**A failed claim is not just a non-zero exit.** `agora claim` must name the current holder and their
note when it loses, because that output is what stops duplicate work. This is the one place where an
exit code alone is a bug.

**`agora guard` is written for a harness, not a human.** It reads a hook event on stdin and writes hook
JSON on stdout. Two constraints: it must exit 0 and print nothing when no claim matches, because a guard
that complains on unrelated edits gets removed; and it must never start a server or do anything else
slow, because it sits in the path of every matching edit.

**It tells rather than decides, by default.** `additionalContext` and no `permissionDecision`, so no prompt and
no refusal, and it says each thing once per member per claim. It shipped returning `deny`, and gating it got
the whole layer switched off inside an hour the first time two agents worked one package: the incident and the
measurement are in `docs/design.md`. `--ask` and `--deny` are still there, and both speak on every matching
edit, because a gate that goes quiet after the first answer lets the next edit through in silence.

**`agora doorbell` produces an exit code, and that is the whole interface.** It is the one command whose
output is not JSON: exit 2 with the wake on stderr is what reaches a session nobody is talking to, and
nothing else does. Three rules, each with a test. It says nothing when `stop_hook_active` is set, because
exit 2 keeps a turn alive and a doorbell that ignored that could hold a session open indefinitely. It rings
for what is *addressed* to the member rather than for unread, because a wake costs a turn. And it never
moves a read cursor, because a doorbell that marked what it rang about as read would answer a message by
hiding it.

**Never require an environment variable only one tool sets.** Identity is `--as`, then `$AGORA_MEMBER`, then the
session a hook event names, then `$CODEX_THREAD_ID`, then `$CLAUDE_CODE_SESSION_ID`, both as
`<harness>-<8 hex of a hash of the id>`, then `$USER`, then the worktree name. Everything after the second is found rather than
asked for, so agora works for one agent in a plain terminal, and an unrecognised agent is named with
`$AGORA_MEMBER` rather than by extending the chain.

An empty `$AGORA_MEMBER` is an error like an empty `$AGORA_DB`: it claims an identity that is not there. An
empty `$USER` or session id falls through, since agora asked nobody to set those.

**A hook takes its identity from the event, not from the environment.** All four of them, because only Claude
Code puts its session id in a hook's environment as well as in the event. `$AGORA_AGENT` supplies the one thing
the event does not say, which harness it came from, and that only decides the prefix. Get it wrong and one
session becomes two members, which is worse than a wrong name: the agent is briefed about its own messages,
warned about its own claim, and `leave` removes somebody else. `docs/design.md` has the whole of it under "A
second harness".

## Docs

`docs/design.md` is a decision record, not a manual: it says what was chosen, what was measured, and
what was rejected. Adding to it is cheap; leaving a false claim in it is expensive, because the next
person trusts it.

**When a measurement decides something, record the number.** "sqlite open-query-close is 0.4ms" and "a
Go process spawn is 5.3ms" are both load-bearing facts that would otherwise be re-derived or guessed,
and together they are the whole argument for having no server yet.

Describe current behavior rather than the history of a change. "agora does X" rather than "agora used to
do Y and now does X", except in a decision record where the rejected alternative is the point.

## Git

- Branches: `pr/chancez/<change-name>`.
- Small logical commits, one concern each. Every commit builds and passes tests, so history bisects.
- Commit periodically to save progress even when the work is unfinished; squash fixups into the right
  commit once the iteration stops.
- Commit messages explain why, and say what was measured. No pronouns, no "we".
- **Never `git checkout -- <file>`** on a file with uncommitted work, and never rebuild a file from
  `git show HEAD:path` or a backup to isolate a change. Both silently delete everything else in that
  file. For mutation testing, `cp file /tmp/x.bak` and restore from that. Before any `git checkout` of a
  path, run `git diff --stat <path>` first. This one is carried over from cm, where it was broken five
  times, twice while mutation testing, where the undo and the loss are the same command.

## Things worth knowing up front

- **`ECONNREFUSED` from a unix socket does not mean nothing is listening.** A live listener refuses once
  its accept backlog fills: measured in cm at 185160 refusals out of 302124 dials against a listener
  that was accepting throughout. Only `ENOENT` is conclusive, and darwin and Linux disagree on the rest.
  This matters the day agora grows a server, and it is why that is deferred rather than trivial.
- **`os.MkdirAll(dir, 0700)` over an existing directory leaves its mode alone.** So a database directory
  can end up group readable, and the channel is a record of what people are working on.
- **Hook output over 10000 characters is spilled to a file and replaced with a preview.** So a hook that
  dumps unbounded unread messages silently stops delivering them. Bound what the hook prints.
- **sqlite does not move `data_version` for an `UPDATE` that writes the value already there.** Measured
  while building the doorbell's takeover. So a write that changes nothing is invisible to every watcher,
  which is what a heartbeat would need and what a recheck must not need. Both directions are pinned by an
  internal test, because neither is visible from outside `internal/store`. The same file pins the other half:
  **opening the database must write nothing**, since every watcher wakes on any commit. `PRAGMA user_version`
  used to be set on every open, so `agora claims`, `agora dump` and a guard that matched no claim each woke
  everybody watching the channel.
- **Unread never names a member's own threads.** Your own messages are read the moment you write them, so a
  thread only you have spoken in cannot reach you through anything built on `UnreadThreads`, the briefing
  included. A layer that has to tell a member about its own state cannot be built on its inbox, which is what
  `ThreadsRequest.Author` and `agora threads --mine` are for.
- **A signal in a command's output arrives after the decision it was meant to inform.** `agora post` used to
  name your own open threads when it opened one, and it was measured doing nothing: agents that linked a new
  thread to an old one posted the link *before* opening the new thread, so the output came too late every time,
  and the arm carrying it without the standing rule linked 0 of 3. The layer that moved them was `AGENTS.md`,
  which is in context before they act. Prefer the always-loaded rule for anything that has to change a
  decision, and keep command output for reporting what happened.
- **bubbletea calls `Init` before the first `WindowSizeMsg`.** So anything `Init` computes from the window size
  computes it from the 80x24 fallback, and at 80 columns the roster is not a column at all. That is how a saved
  roster width came back at the 8 column minimum every session while the saved channels width, which still fits
  at 80, came back right: a bug only reachable through a pane the fallback window drops, and the reopen test
  covered the pane that fits. Clamp for drawing, keep what was asked for separately, and never fold one into the
  other.
- **On Codex an edit is one `apply_patch` call carrying the patch text**, and the event has no `file_path` at
  all. `Edit` and `Write` select the same hooks as matcher aliases, but the payload's `tool_name` is always
  `apply_patch`, so anything reading a path out of a tool event has to read both shapes or it silently reads
  none. There is also no `if` prefilter, so a hook on edits runs on every patch.
- **A path a harness reports and a path git reports need not be the same string.** git resolves symlinks and a
  harness does not, so on darwin an edit in `/tmp/x` is compared against a worktree at `/private/tmp/x`, the
  relative path comes out full of `..`, and every glob misses. Resolve both sides. The guard did not, and was
  silent on every edit a Codex agent made.
- **A session id is not always random, so never name anything after a slice of one.** Claude Code's is a v4
  uuid; a Codex thread id is v7, so its leading digits are a millisecond timestamp and the first eight only
  change about once a minute. Two sessions started together shared a member, a cursor and a claim. A member name
  is now eight hex digits of a hash of the id, which needs to know nothing about how a harness builds one.
- **Codex runs a tool call in a login shell, resolved from the system rather than `$SHELL`.** So its PATH is the
  developer's, not the one you passed, and an experiment that puts its own build first still measures whatever is
  installed. `ZDOTDIR` isolates the profile for zsh, and asking the shell `command -v agora` before starting is
  the check that makes it visible.
- **Every agora command needs write access to the database's directory, reads included.** sqlite creates its
  WAL sidecars to open the file at all, so `agora threads` against a read-only directory fails with `attempt to
  write a readonly database`. It matters because the database sits outside any workspace on purpose, one channel
  per repository, so a path-based sandbox refuses it. Codex asks the first time: in a trusted project it runs
  `workspace-write`, whose writable roots are the working directory and `/tmp`, and either an approved
  `prefix_rule(pattern=["agora"])` or `sandbox_workspace_write.writable_roots` settles it. `codex debug
  prompt-input` prints the mode and the roots a session would use, without a model call, which is how to check
  rather than infer; `codex exec` is read-only, stricter than the interactive default.
- **`$HOME/.agents` is read whatever `CODEX_HOME` says.** A developer's own instructions there are in every arm,
  and one that mentions agora turns a control into a treatment.
- **A skill's frontmatter hooks register only once the skill is invoked.** So the "check before you
  start" wiring cannot ship inside the agora skill: a session that never invokes agora never registers
  it. That hook belongs in settings.
