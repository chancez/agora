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

## Status: nothing is built yet

This repo currently holds `AGENTS.md`, `docs/design.md`, `mise.toml`, and `go.mod`. There is no code.

So anything below describing how a command behaves is a **specification** rather than a description of
something you can run, and `docs/design.md` is the contract. Where this file says "must", read it as a
requirement on the code you are about to write.

The first commit worth making is `internal/store`: the schema, `join`, `post`, `read`, and `claim`, with
tests, before any CLI polish and before the TUI. Everything else depends on that boundary being right,
and it is the one thing that is expensive to change later.

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

## The rule that matters most: no SQL outside internal/store

```
cmd/agora        thin: parse flags, call store, print JSON
internal/store   the only package that knows SQL
internal/notify  the optional nudge, invoking a configured command
internal/tui     read-only view, calls store
```

Every command goes through a method on one type. `internal/store` takes and returns domain types, never
`*sql.Rows` and never a transaction handle.

This is load-bearing rather than tidy. agora deliberately has no server yet, and the measurements behind
that are in `docs/design.md`, but a server is expected eventually. It stays a refactor rather than a
rewrite only while nothing above `internal/store` knows how state is stored. The moment a command builds
its own query, that option is gone.

`store.Watch(ctx) <-chan Message` is the seam that matters most, because it is the one method whose
implementation is expected to change: a poll loop satisfies it today and a server subscription satisfies
it later, and no caller should be able to tell which it got.

## Never test against a channel someone is using

agora's whole purpose is to be read by other agents, which means a test that writes to the real database
posts to a channel real agents will act on. A stray `agora post` during development is a message someone
else's agent treats as a finding, and a stray `agora claim` blocks their edits.

So point every manual test at its own database:

```sh
export AGORA_DB=$(mktemp -d /tmp/agoradev.XXXX)/agora.db
export AGORA_CHANNEL=devtest
```

Prove it rather than assuming it, which means `agora config --json` has to report the resolved database
path **and where it came from**. Build that early, because it is what makes isolation checkable instead
of hoped for. Anything reporting the real `$XDG_DATA_HOME` path is not isolated.

The trap this is guarding against, from cm: an *empty* environment variable reads as unset and falls
through to the default, so `AGORA_DB=` looks like isolation and is not. An entire test suite there read
the developer's real config for weeks while appearing isolated, and nothing failed because no test
asserted on it. Point the variable at a path that does not exist rather than leaving it empty.

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

Three traps from running it, because a wrong result here looks exactly like a right one:

- **The control must not be able to reach the information another way.** The first run of that experiment
  was invalid: the channel file sat in both workspaces, so the control found the claim by listing the
  directory rather than from a hook. It looked like a null result and proved nothing.
- **Injected context is invisible.** `SessionStart` output goes into the model's context, not the
  screen, so reading the terminal cannot confirm the hook fired. Judge by behavior and by files on disk.
- **Two agents, one task, one variable.** Anything that changes both workspaces changes the result.

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

**Never require an environment variable only one tool sets.** Identity resolves from `--as`, then
`$AGORA_MEMBER`, then the environment, and a multiplexer's session variable is used only if one happens
to be there. agora must work for a single agent in a plain terminal on the first run.

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
- **A skill's frontmatter hooks register only once the skill is invoked.** So the "check before you
  start" wiring cannot ship inside the agora skill: a session that never invokes agora never registers
  it. That hook belongs in settings.
