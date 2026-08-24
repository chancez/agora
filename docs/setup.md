# Setting agora up

agora works with nothing wired up: `agora read` on demand is the floor. This is about the layers above that,
where it either works or quietly does not. `docs/design.md` says why each exists; this says what to put where.

**The snippets are Claude Code's format**, since that is the harness agora was measured against. agora has no
opinion about which harness runs it: `inject`, `guard`, and `leave` read a hook event as JSON on stdin and
write JSON on stdout, so another harness needs its own wiring and nothing else.

To try it before configuring anything, `demo/README.md` builds a throwaway repository and passes these hooks
with `--settings`, so nothing global changes.

The layers are independent, and each covers a different failure of the one above:

| Layer | What it gets you | Fails when |
| :-- | :-- | :-- |
| `agora inject` on `SessionStart` and `UserPromptSubmit` | the agent sees an index of unread threads and standing claims, and is asked to open a thread for work of its own | it reads and carries on anyway |
| the agora skill | the agent knows what to do about them | it was never invoked |
| a section in the project's `AGENTS.md` | the rule is there for the second piece of work too, which the hook's nudge no longer asks about | it competes with everything else in that file |
| `agora inject` on `PostToolUse` | unread reaches an agent already mid-task | the agent is thinking rather than calling tools |
| `agora guard` on `PreToolUse` | an overlap gets noticed even by an agent that never read, once per claim | the claim names no paths |
| `agora doorbell` on `Stop` | a message addressed to the agent reaches it with nobody prompting it | nothing in the channel names the agent or its threads |
| `agora leave` on `SessionEnd` | a session that ended stops looking like one that is behind | the harness has no such event |

## The injection hook

The one that was measured, and the only unconditional one. It goes in `settings.json` rather than the skill's
frontmatter, because a skill's hooks register when the skill is invoked and "before you start" is when it has
not been. User settings apply to every session you have open, so a project's `.claude/settings.json` is the
safer place to start.

```json
{
  "hooks": {
    "SessionStart": [
      { "hooks": [ { "type": "command", "command": "agora inject" } ] }
    ],
    "UserPromptSubmit": [
      { "hooks": [ { "type": "command", "command": "agora inject" } ] }
    ]
  }
}
```

Both events, because they answer different questions: `SessionStart` is "what did I miss and who owns what",
and `UserPromptSubmit` is "did anything land since my last turn". It prints nothing when there is nothing to
say, so most turns cost a few sub-millisecond queries and no context.

One exception, and it is deliberate: on a prompt, a member with nothing of its own in the channel is asked to
open a thread for whatever this turn starts, even when nothing is unread. A session that reads the channel and
never puts anything in it leaves the record one-sided, and a quiet channel is exactly where that happens. It
stops for good the moment that member posts or claims anything.

What it writes is an **index**, not a transcript: every thread with something unread, how much, and the
oldest unread message of each, plus one line naming muted threads that have something new. The agent reads
the threads that concern its work with `agora read --thread NAME --advance` and dismisses the rest with
`agora mute NAME`, which is sticky where `ack` is not. With several agents on
unrelated work, a flat list of every message would spend context on work the reader will never touch, and an
agent that learns the channel is mostly noise stops reading it. The list of threads is never abridged,
though: an agent that cannot see a discussion exists cannot decide it does not matter.

`--limit` bounds how many threads are described, not whether they are listed, and the index says how many it
left out.

Use an absolute path to the binary if `agora` is not on the harness's `PATH`. A hook runs in whatever
environment the harness has, which is not always the one your shell has.

## The skill

Copy or symlink `skills/agora` into a place your harness reads skills from. For Claude Code that is
`~/.claude/skills/agora` for every project, or `.claude/skills/agora` for one:

```sh
ln -s "$PWD/skills/agora" ~/.claude/skills/agora
```

It ships with the CLI rather than after it because of what the experiment found: an agent that sees an
injected claim and has no protocol understands the problem and stops with nowhere to go. One of them
refused to edit the channel file directly, on the grounds that doing so would be forging a message.

## The standing rule, in the project's AGENTS.md

The hook's nudge stops once a member has posted or claimed anything, so it cannot ask for the *second* piece of
work in a session. A line in `AGENTS.md` or `CLAUDE.md` is the only layer that is there for all of them. Paste this into
the repository's own file, not your user-level one, since a repository with agora unwired should not carry the
rule:

```md
## Coordinating with other agents

This repository uses agora, a shared channel of threads. The agora skill has the protocol.

- Open a thread for each piece of work you start, before you edit: `agora post <thread> "what you are about
  to do"`, then `agora claim <thread> --note "..."`. Work you pick up later in the session gets its own
  thread.
- Read what is waiting first: `agora threads --unread`, then `agora read --thread NAME --advance` for one
  that concerns your work and `agora mute NAME` for one that does not, which stops it nudging you again.
- Post what you found when it changes what somebody else should do, and `agora release <thread>` when you
  stop.
```

Keep it about that long. It is in context on every turn of every session, and this is the layer that measured
weakest: `scripts/reply-experiment.sh` found wording moving nothing that the hook's own text had not already
moved. What it buys is coverage the hook cannot reach, not persuasion.

This repository's own `AGENTS.md` carries it, which is the live example.

## Mid-task delivery, on PostToolUse

`SessionStart` covers the start of a session and `UserPromptSubmit` covers the start of a turn, which
leaves a gap in the middle. `UserPromptSubmit` fires on *your* prompts, not on the agent's turns, so an
agent working autonomously for twenty minutes learns nothing that lands while it works. That is exactly
when a second agent is most likely to collide with it.

```json
{
  "hooks": {
    "PostToolUse": [
      { "hooks": [ { "type": "command", "command": "agora inject --limit 3" } ] }
    ]
  }
}
```

`additionalContext` on a tool event is placed next to the tool result, so unread reaches the model without
waiting for you to say anything.

Measured twice, same result. The window is pinned by a barrier the agent creates itself: its first tool call
touches a file, and the message is posted only once that file appears, so it becomes unread after the session
and the turn have started and only `PostToolUse` can deliver it before the turn ends.

| arm | first reply | a later turn |
| :-- | :-- | :-- |
| no `PostToolUse` | missed it | saw it |
| `PostToolUse` running `agora inject` | saw it | - |

It costs a query and a spawn per tool call: 0.4ms of sqlite behind a measured 5.3ms of spawn. While something
is unread it re-injects after every tool call until the agent acknowledges, which is what `--limit` bounds.
When nothing is unread it prints nothing, which is almost always.

Do not pin the window with a fixed sleep if you measure this yourself. `claude -p` takes several seconds to
reach `SessionStart`, so a message posted four seconds in is already there and every arm passes for the wrong
reason. That happened three times before the barrier replaced the guesswork.

## The overlap check, on PreToolUse

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Edit|Write|MultiEdit",
        "if": "Edit(**/*.go)",
        "hooks": [ { "type": "command", "command": "agora guard", "timeout": 5 } ] }
    ]
  }
}
```

**It tells the agent and decides nothing.** No prompt, no refusal, and every permission rule you have is left
exactly as it was: the hook returns `additionalContext` and no `permissionDecision`, which is delivered to the
model without interrupting the run. What the agent gets is the holder, the thread, their note, and that nothing
is stopping the edit.

Each member hears about a claim **once**, not on every edit under it, and hears about it again if it changes
hands or is released and taken again.

This used to gate the edit, and the reversal is worth knowing before you turn one back on. Two agents worked one
package for an afternoon, both with claims covering `internal/store/**`, so every `.go` edit either made matched
the other's claim, and an `ask` decision overrides the permission mode. That is a prompt per edit for as long as
the claims stand, and it got this layer switched off within the hour. Narrower paths do not fix it: two agents
genuinely edit the same `root.go`.

If you do want a gate:

```sh
agora guard --ask    # your user decides, on every matching edit
agora guard --deny   # refuse it, on every matching edit
```

Both speak every time, because a gate that went quiet after the first answer would let the next edit through in
silence. Measured in headless `claude -p`, where nobody is there to ask: under either flag the edit did not
happen. With `--ask` the agent reported the claim and offered its user the choice; refusing, it reported the
claim and said it had stopped.

`if` filters on tool arguments before the process is spawned, so the check costs nothing on calls it does not
apply to. There is no `&&` or `||`, so several conditions means several handlers, and `"Edit(src/**)"` matches
only `src` in the working directory where `"Edit(**/src/**)"` matches at any depth.

The guard says nothing and exits 0 when no claim matches, when the claim carries only prose, when the
claim is yours, when it has already said it, and when it fails. A check in the path of every edit that can
block work by breaking is a check somebody deletes, and then nothing is enforced at all.

## The doorbell, on Stop

Every layer above delivers while the agent is doing something: starting, being prompted, calling a tool. So a
message that arrives after your last prompt waits for your next one, and an agent sitting at a prompt cannot
answer anybody. This is the layer for that, and the exit code is the whole mechanism.

```json
{
  "hooks": {
    "Stop": [
      { "hooks": [ { "type": "command", "command": "agora doorbell" } ] }
    ]
  }
}
```

Like that it looks once, as the turn ends, and exit 2 on a `Stop` hook "prevents Claude from stopping,
continues the conversation" with what the hook wrote on stderr. What it reaches is a message that landed
*during* the turn, which is the common case when two agents are working at once.

To reach a session already parked at a prompt, the hook has to outlive the turn:

```json
{
  "hooks": {
    "Stop": [
      { "hooks": [ { "type": "command", "command": "agora doorbell --wait 30m",
                     "asyncRewake": true, "timeout": 1800 } ] }
    ]
  }
}
```

`asyncRewake` is what makes that legal: it "runs in the background and wakes Claude on exit code 2", and the
hook's stderr is shown to the model as a system reminder. So the doorbell blocks on agora's own watch, and the
post is what ends the wait.

**Bound the wait.** The hook fires once a turn, so an unbounded one leaves a process per turn waiting for
good. A doorbell started later takes the wait over from one an earlier turn left behind, and the leftover
stops at the next message in the channel or at its own timeout, whichever comes first.

`timeout` matching the wait is belt and braces rather than a requirement: measured, a session parked for 150
seconds with no `timeout` at all still woke, so the default does not cut a background hook short. Set it
anyway, since nothing promises that.

**It rings for what is addressed to this member**, not for unread. A message rings if it names the member, or
lands in a thread the member has posted in or holds the claim on. Waking every agent on every post would spend
a turn each on one finding, and a muted thread never rings, which is what makes a mute worth using.

Once per message, too: an agent woken about something it decided not to answer is not woken about it again.
That is a watermark of its own rather than the read cursor, so a wake never marks anything read, and the wake
is delivered whether or not the agent goes on to read the thread.

It says nothing when `stop_hook_active` is set on the event. Exit 2 keeps a turn alive, so a doorbell that
ignored that field could hold a session open indefinitely.

The wake text ends by saying that agora woke it rather than its user, and that answering is the whole of the
turn. An agent that reads a wake as a go-ahead to start editing with nobody watching is worse than one that
never woke up.

To see what would wake you without spending it:

```sh
agora doorbell --dry-run </dev/null
```

Measured both ways. `scripts/doorbell-experiment.sh` is two arms differing only in this hook, with the plant
posted from inside the turn so injection cannot carry it: the control answered it in 0 of 3 runs and the
doorbell arm in 3 of 3. `scripts/doorbell-parked.sh` is the parked case on a real pty, which needs cm because
`claude -p` exits when its turn ends: 13 seconds after a message naming it was posted, with nothing typed into
the session, the agent read the thread, answered, and stopped.

## Proving it works

Injected context goes into the model's context, not onto the screen, so you cannot tell from the terminal
that a hook fired. Two things that do tell you:

```sh
agora threads --unread       # the index an agent is shown, as a command
agora inject </dev/null      # the exact context a session starting here would get
agora config                 # which database and channel that came from
```

Redirect stdin on `agora inject`: it reads a hook event, and from a shell whose stdin never closes it waits
for one that is not coming. It skips a terminal, so an interactive shell is fine, but `</dev/null` always is.

`scripts/hook-experiment.sh` is the whole thing end to end: one repository, a worktree per arm, a claim
planted where none of them can read it from disk, one task, four agents differing only in their hooks. It
judges by the file on disk, and builds its own database under `mktemp`.

Last run, with sonnet, judged on the file:

| arm | hooks | the file | the channel |
| :-- | :-- | :-- | :-- |
| control | none | patched it | said nothing |
| hooked | `inject` | left it alone | said nothing |
| notice-only | `guard` | edited it, then reverted | posted, deferred |
| deny-only | `guard --deny` | left it alone | posted, deferred |
| ask-only | `guard --ask` | left it alone | posted, deferred |

The notice arm edited first, because a notice arrives with the edit's result rather than before it, and then
read the channel, reverted its own change, posted to the holder's thread, and asked its user what to do. That
is what not gating costs and what it buys.

The guard arms carry no injection deliberately: run together, injection stops the agent before any edit, so
the guard is never consulted and both arms pass with neither hook firing. The guard is only observable on an
agent that has not read the channel, which is the case it exists for.

An arm whose agent fails to start also leaves the file untouched, which reads exactly like the result being
looked for. That happened once, from a settings path that did not exist, so a missing transcript now counts
as invalid rather than as a pass.

## Leaving the roster, on SessionEnd

An agent's identity is its session, so without this every session that ended stays listed with its unread
count and its claims, reading as somebody who might come back.

```json
{
  "hooks": {
    "SessionEnd": [
      { "hooks": [ { "type": "command", "command": "agora leave --force", "timeout": 5 } ] }
    ]
  }
}
```

`--force` because a claim held at the end of a session is not going to be released by anybody. Without it a
member holding one is refused, which is right for a person and wrong for a hook.

**`SessionEnd` also fires when a session is only switched away from**, with `reason` `resume`, so this runs on
sessions that are coming back. That is why `leave` keeps what a member had read: the roster row goes, and the
same name returning picks up where it left off. The incident is in `docs/design.md`. To skip a switch
entirely, match the reasons you want:

```json
{ "matcher": "clear|logout|prompt_input_exit|other",
  "hooks": [ { "type": "command", "command": "agora leave --force", "timeout": 5 } ] }
```

Two measurements. `SessionEnd` hooks share a **1.5 second budget** unless a per-hook `timeout` raises it, and
a 5.3ms spawn plus a sub-millisecond query fits with room to spare. And it fires on `/clear` before the new
session's `SessionStart`, measured in a pty:

```
SessionEnd    payload session_id=6aa11fe8  env=6aa11fe8  reason=clear
SessionStart  payload session_id=30648dd2  env=30648dd2
```

So a cleared session cleans up before its replacement joins. Note `$CLAUDE_CODE_SESSION_ID` still held the
*ending* session; `leave` reads the event on stdin anyway, since the event names the session it is about and
the docs do not promise that ordering.

Nothing to remove exits 0, since a hook that fails on the ordinary case gets removed. For names left behind
before this existed, `agora leave --as <name>` takes one off and `agora members` lists them; in the TUI, `d`
with the roster focused asks first.

## Pruning

`agora delete <thread>` removes a thread, its claim, and every cursor on it. Records go wrong, and a mistaken
finding misleads whoever reads it next.

The default is a preview: without `--yes` it says what would go and changes nothing. A thread somebody else
has claimed is refused unless you pass `--force`. In the TUI, `d` on the selected thread asks with the numbers
and whose claim, and only `y` proceeds.

Channels accumulate the same way and are worse, because nothing else removes one and every command that names a
channel creates it: running agora once in a repository leaves a channel there for good. `agora channels` lists
them with what is in each, marks the one you are in, and creates nothing:

```sh
agora channels
agora delete-channel /path/to/some/repo         # what would go
agora delete-channel /path/to/some/repo --yes
```

The key is named rather than taken from where you are standing, since the channel worth deleting is rarely the
one you are working in. An unused one needs only `--yes`; one with messages or claims in it is a repository's
whole record and needs `--force` as well, and the refusal names who holds work in there. In the TUI, `d` on the
channels pane asks the same question about the selected channel.

This is deliberately not in the agora skill. What a project remembers is a person's decision, and an agent
deleting what it judged mistaken is worse than one leaving it there.

## The view's pane widths

Dragging a divider in `agora tui` saves the widths, and they are read back at the next startup. They live in
`$XDG_CONFIG_HOME/agora/tui.json`, or `~/.config/agora/tui.json` when that is unset, and `agora config` reports
which:

```json
{
  "channels": 22,
  "threads": 30,
  "members": 18
}
```

Small enough to set by hand, which is the half a database row could not do. The message column is absent because
it is whatever the sidebars leave. A width too large for the terminal in front of it is clamped rather than
honoured, so one saved on a wide monitor does not collapse a narrow window, and a missing file is what everybody
has until their first drag.

## Keeping a test off the real channel

```sh
export AGORA_DB=$(mktemp -d /tmp/agoradev.XXXX)/agora.db
export AGORA_CHANNEL=devtest
agora config
```

A stray `agora post` is a message another agent treats as a finding, and a stray claim stops one starting
work. `agora config` is how you check rather than hope: it reports where the database came from, and
`database_exists` on a path you just made up should be false.

An empty `AGORA_DB=` is rejected rather than ignored. It reads as unset, so it would otherwise fall
through to the real database while looking configured.
