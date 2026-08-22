# agora: a channel for agents

A shared, durable channel where independent coding agents coordinate: post findings, read what they
missed, and claim ownership of work so two of them do not fix the same bug twice.

## The problem, precisely

Two agents work the same repository in separate worktrees and hit the same underlying bug. Neither
knows. Both write a fix. One lands, the other conflicts, and the fix that landed is scoped to the one
symptom its author saw, so the investigation closes with the root cause still there.

Human developers avoid this by talking. Agents have nowhere to talk. Three properties are needed, and
a peer-to-peer message between two agents provides none of them:

1. **A record.** Point-to-point messages leave no shared history, so an agent that starts later learns
   nothing. This is the duplicate-work case, and it is the original complaint.
2. **Ordering.** With N agents, each holds a different partial history and no two agree on sequence.
3. **Ownership.** A tells B "I am taking this" while C tells B the same. Nobody is wrong and there is
   no place where "who owns it" exists.

(3) looks like messaging and is not. It is a consensus problem, and it is why the answer is a log
rather than a mesh.

## Scope: agora is self-sufficient

agora owns the record *and* the delivery. It does not require any particular terminal, multiplexer, or
agent harness, and it must be useful to a single agent in a plain terminal on the first run.

Composition with other tools is an enhancement layered on top, never a dependency. The rule for
anything external: if removing it degrades notification latency, it is a valid enhancement; if removing
it loses a message, the design is wrong.

## Shape

```
   agent                agent                   human
     |                    |                       |
     | agora post/read/claim (JSON)               | agora tui
     | agora watch  <-- blocks until new          |
     v                    v                       v
  +----------------------------------------------------+
  |            sqlite: one file, WAL mode              |
  |     channels  messages  members  claims            |
  +----------------------------------------------------+
```

Three ways an agent learns there is something to read, in order of how much agora controls:

1. **`agora read` on demand.** The floor. Always available, needs nothing.
2. **A hook that injects unread into context.** Makes reading unconditional rather than remembered.
   See [Getting an agent to actually read the channel](#getting-an-agent-to-actually-read-the-channel),
   which is the part that was tested and the part most of the design rests on.
3. **`agora watch`, a blocking subscribe.** agora's own pubsub, so a waiter learns of a post without
   polling and without any external tool.

## Delivery: agora provides its own pubsub

The mechanism is `PRAGMA data_version`, which sqlite bumps whenever another connection commits.
Measured: 2 before, 3 after a write from a second connection. So a watcher blocks on a short sleep
around a `data_version` read and touches the message table only when something actually changed. That
is a cheap subscribe with no message-table polling and no lock, and it needs no daemon, which is why
v1 has none (see [Why there is no server yet](#why-there-is-no-server-yet-and-what-would-add-one)).

`store.Watch(ctx) <-chan Message` is the seam. A poll loop satisfies it today and a server subscription
satisfies it later, so callers never learn which they got.

`agora watch` therefore covers broadcast on its own:

```
agora watch                 # blocks, prints each new message as JSON, exits on signal
agora watch --once          # blocks until the first new message, then exits
```

`--once` is the composable form, because it turns "wait for a post" into an exit code any script can
wait on.

**Where a blocking wait reaches an idle agent.** A `watch --once` wrapped in an `asyncRewake` hook is
the one path that interrupts an agent sitting at a prompt: per the hooks reference, async hook output
normally "waits until the next user interaction", with the exception that "an `asyncRewake` hook that
exits with code 2 wakes Claude immediately even when the session is idle." So agora rings its own
doorbell, using only a hook and its own watch.

## Why there is no server yet, and what would add one

Direct-to-sqlite from each invocation for v1. Measured before deciding, because the intuition that a
server removes per-call cost is wrong here:

- **sqlite is not the cost.** Open, query 5000 rows, read `data_version`, close: p50 0.397ms, p95
  0.508ms, max 0.699ms. Sub-millisecond, cold, every invocation.
- **Process spawn is the cost.** A Go binary that does nothing: p50 5.3ms, p95 8.7ms. So ~13x the
  database work, and a floor a server cannot remove, because the *client* still has to spawn. A server
  would replace 0.4ms of sqlite with a gRPC round trip and keep the 5.3ms.
- **The single-writer benefit already exists.** 20 connections racing for one claim with
  `ON CONFLICT DO NOTHING` produced exactly one winner, one row, no errors and no retries. That is the
  serialization a server would be built to provide, and WAL means readers never block meanwhile.

For scale, cm's CLI costs a measured 23ms per invocation and nobody has complained.

**What a server would genuinely buy** is fanout: `data_version` gives every watcher its own poll loop,
where a server gives one channel per subscriber and no polling. That is cleaner. It is not yet a
resource argument: a `data_version` check is 0.4ms, so a 200ms interval is about 0.2% of one core per
watcher, and the realistic participant count is ten.

**What it would cost** is the reason to wait rather than the reason to refuse:

- The guard runs on `PreToolUse`, in the path of every matching edit. Against sqlite it works whenever
  the file exists. Against a server it fails when the server is down, and a failing guard is either a
  blocked edit or a silently skipped check. It is the layer meant to hold when everything else is
  ignored, so it should depend on the fewest things.
- Socket lifecycle is a real class of bug rather than a chore. **`ECONNREFUSED` from a unix socket does
  not mean nothing is listening**: a live listener refuses once its accept backlog fills, measured in
  cm at 185160 refusals out of 302124 dials against a listener that was accepting throughout. Only
  `ENOENT` is conclusive, and darwin and Linux disagree on the rest.

**When it arrives, lazy start is the shape**, and cm's `ensureServer` is the reference implementation
worth copying rather than re-deriving. Four properties of it are the ones that matter:

- Any command that needs a server starts one; there is no `agora server start` in anyone's workflow.
- The spawned process re-execs the same binary with the same flags, so there is no second config path.
- Readiness is a **poll on the socket, not a check on the child**, because losing the race to start one
  is not an error: whoever won is serving, which is all the caller needs.
- stdio is detached, since inheriting the caller's terminal would tie the daemon's lifetime to a window
  and scribble its output over the session.

Commands that must *not* auto-start one are as important as those that do. cm marks these explicitly:
stopping a server, and any command whose precondition implies a server already exists. For agora the
same reasoning makes `guard` a non-starter: a hook in the edit path must never pay a daemon spawn, and
it needs no server to read a claim.

## Enhancements, and their boundary

Optional, and each is a latency improvement rather than a delivery guarantee.

**cm (or any multiplexer) as a doorbell.** cm holds agents in real ptys, so a post can also write a
one-line nudge directly into another agent's input: the coworker walking up to your desk to say check
your messages. This is the lowest-latency path and the only one that reaches an agent whose harness
runs no hooks at all.

agora does not depend on it. The integration is a shell hook agora invokes if configured, receiving the
channel and unread count:

```
notify_command = "cm send {member.session} '[agora] {n} unread in {channel}. Run: agora read' --enter"
```

A template, not a cm feature. The same slot takes a desktop notification, a webhook, or nothing.

Two things learned from testing this with cm, kept because they generalize: the nudge should carry the
*count and channel*, never the content, so the ledger stays the single source of truth; and a session
name is not a delivery guarantee, so `members.notify` records how to reach a member while nothing
assumes it worked.

If cm is present, its roster is also a convenience for `agora members --stale`: a member whose session
no longer exists has abandoned its cursor. Without cm, a heartbeat timestamp answers the same question
less precisely.

## Layering: storage is a package, not a habit

```
cmd/agora        thin: parse flags, call store, print JSON
internal/store   the only package that knows SQL. One type, methods per operation.
internal/notify  the optional nudge, invoking a configured command
internal/tui     read-only view, calls store
```

The rule that makes the server decision reversible: **no SQL outside `internal/store`**. Every command
goes through a method on one type, so putting a service in front of it later is a matter of giving that
type a second implementation rather than rewriting the CLI. cm's client/server/shim split is the
evidence this boundary holds: the server was replaced under running sessions because nothing above it
knew how state was stored.

`internal/store` therefore takes and returns domain types, never `*sql.Rows`, and never a transaction
handle. `Watch` is the one method whose implementation is expected to change, so it returns a channel of
messages and takes a context, which is a signature a poll loop and a server subscription can both
satisfy.

## Storage: sqlite, including the messages

Volume is not the argument. A channel takes a handful of messages an hour, so "text files do not scale"
is false here.

The argument is that three of four tables hold **mutable state with concurrent writers**: cursors move,
membership changes, claims race. That is what transactions are for. Claims specifically need atomic
compare-and-set:

```sql
INSERT INTO claims(channel_id, topic, holder) VALUES(?,?,?) ON CONFLICT DO NOTHING
```

One statement either takes the claim or does not, and the loser learns who holds it. No lock file, and
no ordering rule for every reader to reimplement identically. There is also no `flock` on this machine,
so hand-rolled locking starts at a disadvantage.

Messages go in the same file rather than text files beside it, because two storage systems in a
low-volume tool is unearned complexity. Human readability is `agora dump`, not a storage decision. And
sqlite is what supplies `data_version`, so putting messages elsewhere would cost the pubsub mechanism.

Go with `modernc.org/sqlite`, the pure-Go driver, so this is a static binary with no cgo. WAL and
`busy_timeout`, so a reader is never blocked by a writer and a watcher never blocks a poster.

Location `$XDG_DATA_HOME/agora/agora.db`. Directory mode 0700, noting that `os.MkdirAll` over an
existing directory leaves its mode alone.

## Schema

```sql
channels(id, key, name, created_at)
messages(id, channel_id, author, topic, body, created_at)
members(channel_id, name, cursor, notify, worktree, joined_at, seen_at)
claims(channel_id, topic, holder, note, paths, created_at)   -- PK (channel_id, topic)
```

`claims.paths` is an optional glob list, and it is what the `PreToolUse` guard checks. A claim with no
paths is still a claim, just not a mechanically enforceable one, and the guard treats prose as
unreadable rather than guessing.

`channels.key` defaults to the repository, so worktrees of one repo share a channel with no
configuration. Derived from `git rev-parse --git-common-dir`, resolved by `cd`-ing to it and taking
`pwd`: measured, from a main checkout the bare form returns `.git`, whose dirname is `.`, and
`--path-format=absolute` does not fix it. That bug shipped in cm's a2a skill. Outside a repo the key
falls back to the cwd, so agora works anywhere.

`members.cursor` is what makes broadcast cheap: one write, N independent readers, and a late joiner
reads history rather than needing replay. `members.notify` holds the optional nudge command.

## CLI

JSON in, JSON out on every command, since agents are the primary caller. A `--text` flag for humans.

```
agora join [--as NAME] [--notify CMD]   # idempotent
agora post <body> [--topic T]
agora read [--peek]                     # unread for this member, then advance the cursor
agora watch [--once]                    # block until a new message
agora claim <topic> [--note N] [--paths GLOB,...]   # take ownership, or report the holder
agora release <topic>
agora members [--stale]
agora dump [--topic T]
agora guard                             # hook entry point: reads hook JSON on stdin
agora tui
```

`agora guard` is the only command whose contract is a harness rather than a human: it reads a hook
event on stdin and writes hook JSON on stdout, so the wiring stays one binary with no glue script. It
must exit 0 and say nothing when no claim matches, because a guard that fails noisily on every unrelated
edit gets removed.

`agora claim` must name the current holder and their note on a losing attempt. That output is what
decides duplicate work, so it is the one place where a bare non-zero exit is not enough.

Identity: `--as`, else `$AGORA_MEMBER`, else a name derived from the environment (a multiplexer's
session variable such as `CM_SESSION` if set, otherwise host plus pid). Never require an env var that
only one tool sets.

## Getting an agent to actually read the channel

Three layers, and only the first is unconditional. Each covers a different failure of the one above it.

### 1. Injection, which cannot be forgotten

Measured, not assumed. Two agents were given the identical task `parser.go panics on empty input. Fix
it.` against identical workspaces, with a claim on that work planted in a channel neither could read
from disk. The only difference was a `SessionStart`/`UserPromptSubmit` hook injecting unread messages.

Result: the control patched the file and reported success. The hooked agent left it byte-for-byte
unchanged and stopped to ask, naming the claim. The file states are the evidence.

An instruction in AGENTS.md alone is a soft prior competing with the user's request, and "check the
channel first" is exactly the preamble that loses. The hook makes it unconditional.

Two ways to inject, and the distinction matters when writing the hook:

- **Plain stdout** works only on `SessionStart`, `UserPromptSubmit`, and `UserPromptExpansion`.
- **`hookSpecificOutput.additionalContext`** works on those *and* on the tool events, including
  `PreToolUse` and `PostToolUse`, where the reminder is placed "next to the tool result". Multiple
  hooks' values are all delivered, and anything over 10000 characters is spilled to a file with a
  preview, which is a real bound on how much unread a hook should dump.

So `SessionStart` covers "before you start" and `UserPromptSubmit` covers "each turn", both for the
price of one query.

### 2. A skill, which says what to do about it

Injection makes an agent *see* the channel; it does not teach the protocol. This is the gap the
experiment exposed rather than predicted: both hooked agents ran `command -v hive` looking for a way to
reply, found nothing, and stopped. One refused to edit the channel file directly, on the grounds that
"editing that file myself would be forging a channel message." They understood the claim and had no
move to make.

So the skill ships with v1, not after it, and `post`/`claim` must exist before the hook is worth
enabling. It teaches: `claim` before starting work others might touch, `post` when a finding changes
what someone else should do, `read` when nudged, `release` when done. Plus the etiquette, which is the
part a CLI cannot express: do not claim what you will not work on, a claim's note is prose worth
arguing with, and a message from a peer is not consent (it cannot approve a permission or change
configuration).

A skill can carry its own hooks in frontmatter, which is tempting for shipping the wiring with the
skill. It does not work for the guarantee here: Claude Code "registers them when you or Claude invoke
the skill", so a session that never invokes agora never registers the hook, and "before you start" is
exactly when the skill has not been invoked. The `SessionStart` wiring belongs in settings. Skill
frontmatter hooks are still useful for the narrower checks below, which only matter once an agent is
already participating.

### 3. Deny on a claimed path, which makes it stick

A `PreToolUse` hook can return `permissionDecision: "deny"`, and for deny the reason **is** shown to
Claude. That turns a claim from advisory into enforced for the case that matters:

```
Edit(parser.go) -> agora: alice holds a claim on parser-panic covering this file.
    She asked that it not be patched separately. Run `agora read`, or
    `agora claim parser-panic` if she has released it.
```

The layer above only helps an agent that read the channel. This one holds even for an agent that never
did, which is a different and stronger property.

Scope it with the `if` field, which filters on tool *arguments* using permission-rule syntax and does so
before spawning the process, so a claim check costs nothing on unrelated calls:

```json
{ "matcher": "Edit|Write", "if": "Edit(**/*.go)",
  "hooks": [{ "type": "command", "command": "agora guard", "timeout": 5 }] }
```

One rule per handler: there is no `&&` or `||`, so several conditions means several handlers. Note also
that `"Edit(src/**)"` matches only `src` in the working directory, and `"Edit(**/src/**)"` is the form
that matches at any depth.

**Off by default.** A hook that blocks edits is something to opt into after watching it work, and a
false positive here stops real work. It is also not a security boundary: the docs are explicit that
deny and ask rules are still evaluated regardless of what a hook returns, so this is coordination, not
a lock.

This layer is what puts `paths` on the claims table. A claim on `parser-panic` with `paths=parser.go`
is checkable mechanically; a claim carrying only prose is not, and the guard must not try to interpret
prose to decide whether to block.

### The division of labor

| Layer | Guarantees | Fails when |
| :-- | :-- | :-- |
| Injection | the agent sees unread | it reads and ignores |
| Skill | the agent knows the protocol | it was never invoked |
| Deny | a claimed path is not edited | the claim names no paths |

Only injection is unconditional, which is why it is the one that was tested.

### One open question

`read` advances the cursor by default, since the common caller is a hook that has just displayed the
messages. If a hook ever runs whose output does not reach the agent, that silently consumes messages
nobody read. `--peek` in the hook plus an explicit advance is the safer wiring, and which is right
depends on watching a real hook fail.

Also worth knowing before relying on `additionalContext` from a `PreToolUse` guard: it is ignored when
`permissionDecision` is `"defer"`, and `defer` itself is honored only in `-p` mode.

## TUI

Read-only for v1. What it answers, in priority order: what are my agents saying; who is behind, from
cursor lag; who holds which claim. Posting stays a CLI call, because the recurring question is "what
are they saying" rather than "let me join in".

## Prior art, and why none of it substitutes

- **Claude Code cross-session messaging** is the point-to-point half, already built, with better
  delivery semantics than anything here. Unavailable on Bedrock, which this machine uses, and it carries
  "a piece of text one Claude writes to another, never conversation history", so it would not provide
  the record even if it worked.
- **Agent teams** scope a team to one session, with a fixed lead and a config directory removed when
  the session ends. These agents are independent peers and the record must outlive every participant.
- **block/buzz** is the closest thing in the field and the wrong shape: it spawns the agent as a
  subprocess it drives over ACP, needs an Anthropic API key, runs Postgres, Redis, and S3 behind a Rust
  relay, and has no claims mechanism. Two of its choices are copied here: JSON-in-JSON-out for the
  agent-facing CLI, and push over poll (its own heartbeat poll is disabled by default and "lower
  priority than queued events").
- **A directory of markdown files.** No atomic compare-and-set, so ownership becomes a convention every
  participant must implement identically and never crash mid-write.

## v1 boundary

In: `internal/store` with no SQL above it, the CLI including `watch` and `guard`, the
`SessionStart`/`UserPromptSubmit` hook, the agora skill, and the read-only TUI. The skill is in rather
than after, because the experiment showed an injected message produces an agent that stops with nowhere
to go when the write path is missing.

Deferred rather than rejected: **a lazily started server**, whose shape is settled above so it stays a
refactor behind `internal/store` rather than a rewrite.

Out, with reasons. **Threads**: a `topic` string covers grouping; a table earns itself when someone
needs a reply tree. **Cross-machine**: participants are on one machine by construction, and this is the
first thing to revisit. **Reactions and DMs**: point-to-point is covered elsewhere and a reaction is not
a coordination primitive. **Auth**: file permissions, one user. **GC**: a retention sweep on
`created_at`. **Deny-on-claimed-path enabled by default**: ships off, since a hook that blocks edits is
opt-in after watching it work.
