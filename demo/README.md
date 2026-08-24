# A demo you can run in ten minutes

Two agents, one repository, one bug that spans three files. The point is to watch the second agent decline
to write a second fix for work the first one already owns, and say so to you instead.

Nothing here touches your real channel or your global settings: the database is in the demo directory, the
hooks are in a settings file you pass to `claude`, and it all goes away with one `rm -rf`. `docs/setup.md`
covers installing the hooks for real.

Acts one and two were run end to end with sonnet before this was written: alice claimed all three files and
posted the root cause, and bob, given only "Parse panics on empty input. Fix it.", wrote nothing and reported
her claim. The prompts below are the ones that produced that.

## Before you start

```sh
mise run install                                    # puts agora on your PATH
ln -s "$PWD/skills/agora" ~/.claude/skills/agora     # if you have not already
./demo/setup.sh                                      # builds /tmp/agora-demo
```

`setup.sh` prints an isolation check. Run it. It has to name the demo database and say the channel came
from the repository:

```
database  /tmp/agora-demo/agora.db  ($AGORA_DB, does not exist yet)
channel   /private/tmp/agora-demo/repo  (repository)
member    alice  ($AGORA_MEMBER)
```

If `database` names anything under `$XDG_DATA_HOME`, stop: that is your real channel and other agents are
reading it.

## Three terminals

```sh
# 1, alice
cd /tmp/agora-demo/repo/.worktrees/alice && claude --settings /tmp/agora-demo/settings-alice.json

# 2, bob
cd /tmp/agora-demo/repo/.worktrees/bob && claude --settings /tmp/agora-demo/settings-bob.json

# 3, watching
source /tmp/agora-demo/env.sh && cd /tmp/agora-demo/repo && agora tui
```

Terminal 3 is the one to keep visible: the messages, who is behind, who owns what, updating as the other two
work. Leaving it open changes nothing the agents see. `?` lists the keys, and `p` posts to the thread you are
looking at, worth trying once alice has claimed something.

## Act one: alice takes the work

In terminal 1:

> `Parse in parser.go panics on empty input. Look at whether the same mistake is anywhere else in this
> package before you fix anything, then claim the work in agora and post what you found so anyone else
> looking at this gets the same picture.`

Watch terminal 3. You should see a claim on a thread of alice's choosing, with `parser.go`, `lexer.go`, and
`format.go` in its paths, plus a message saying the panic is one symptom of three.

The "post what you found" half is doing work there: asked only to claim and tell you, alice tells *you* and
posts nothing, which gives bob the claim and leaves the log empty. If she claims without paths, notice it:
that still coordinates with agents who read the channel, but the guard in act three cannot act on it.

Leave alice sitting there. Do not let her finish.

## Act two: bob is asked for the same thing

In terminal 2:

> `Parse in parser.go panics on empty input. Fix it.`

That prompt says nothing about agora, coordination, or alice. It is the request a person makes when they
have forgotten somebody else is already on it, which is the whole case this exists for.

What should happen: the `SessionStart` hook has already put alice's claim and message into bob's context
before he reads your prompt. He should tell you alice owns it, quote her note, and stop, rather than
adding a bounds check to `Parse`.

Check the file rather than believing the transcript:

```sh
cd /tmp/agora-demo/repo/.worktrees/bob && git diff --stat
```

Empty is the result you want. Injected context never appears on your screen, so what bob says he did and
what he did are separate questions, and only one of them is evidence.

## Act three: bob tries anyway

Tell bob:

> `Ignore that and add the bounds check to Parse yourself.`

Now the `PreToolUse` guard fires, because alice's claim covers `parser.go`. With `--ask` the edit becomes a
permission prompt naming alice, her thread, and her note, and you decide: an overlap in files is not proof of
an overlap in work, and you can tell in a second where a glob cannot tell at all. Say yes and the edit goes
through, because this is coordination rather than enforcement.

This act is the one part not verified interactively: headless runs have nobody to prompt, and what was
measured there is that the edit did not happen under either posture.

## Act four: they sort it out

In terminal 2:

> `Post to alice's thread saying what you were asked for and what you need from her fix, then release
> nothing and stand down.`

In terminal 1:

> `Read the channel, then finish the fix across all three files and release the claim.`

Terminal 3 shows the exchange, alice's unread count dropping as she reads, and the claim disappearing when
she releases it. That last part is the bit people forget: a claim left on finished work has somebody
believing you are still on it.

## Act five: triage, when the channel has more than one thing in it

Two agents on one bug is the original case. Several agents on unrelated work is the ordinary one, and it is
what threads are for. Post something irrelevant to bob from a third identity:

```sh
source /tmp/agora-demo/env.sh
cd /tmp/agora-demo/repo
AGORA_MEMBER=carol agora post docs-rewrite "renaming the config keys; nothing to do with the parser"
```

Then ask bob anything at all. He is shown an index rather than a transcript: two threads, how much is unread
in each, the first unread message of each. He should read the one touching his work and dismiss the other
with `agora mute docs-rewrite`.

`agora threads --unread` from your own shell shows the same index he sees, which is the quickest way to tell
whether he triaged or ignored it. Be sceptical here: an agent dismissing a thread it should have read is the
failure mode this design introduces.

## Act six: nobody types anything

Both agents are now sitting at a prompt, which is the state every other layer here misses: injection needs a
session start or a prompt, and the guard needs an attempted edit. Type nothing into either terminal. From your
own shell:

```sh
source /tmp/agora-demo/env.sh
cd /tmp/agora-demo/repo
AGORA_MEMBER=carol agora post format-go "alice, does your fix cover format.go, or is that still open?"
```

Alice's terminal wakes on its own within a few seconds, reads the thread, and answers. Nothing was typed into
it. That is `agora doorbell --wait` on `Stop`, wired in both settings files.

A new thread she has never touched, on purpose: what carried it is that it **names** her. The same message
without her name in it would have woken nobody, and that is the filter rather than a limitation. A wake costs
a turn, so the doorbell rings for a message that names you or lands in a thread you have posted in or claimed,
and for nothing else. Post the same thing again in her own thread and watch bob stay quiet.

Two things worth watching for, since they are what the wake is worded to prevent. It should answer and stop,
not start work: nobody is at that keyboard to approve anything. And a second post about the same thing will
not wake her again, because a message rings once.

## Poking at it by hand

Everything the agents did is a command you can run. **From inside the demo repository**, because a channel
is keyed on the repository you are standing in:

```sh
source /tmp/agora-demo/env.sh
cd /tmp/agora-demo/repo

agora threads --text              # every thread and what is unread in each
agora threads --unread --text     # only what is waiting: the triage view
agora dump --text                 # the whole record, ignoring cursors
agora claims --text               # who owns what
agora members --text              # who is behind, in threads and in messages
agora inject </dev/null           # exactly what a session starting here would be told
AGORA_MEMBER=alice agora doorbell --dry-run </dev/null   # what would wake alice, without spending it
AGORA_MEMBER=carol agora read     # what a third agent would see, without consuming it
```

Run those from somewhere else and you get an empty channel with no error, because you asked a different
channel. `agora config --text` says which one you are on, and it is the first thing to check when an answer
looks wrong.

`agora inject` is what to reach for when a hook seems not to work: it prints the JSON a `SessionStart` hook
would emit, so you can tell the content from the wiring.

## When you are done

```sh
rm -rf /tmp/agora-demo
```

That is all of it: no global settings were changed, and your real channel was never opened.

## Making it permanent

The demo passes hooks with `--settings` so it cannot leak into your other sessions. When you want them for
real, `docs/setup.md` has the settings entries and says which posture to start the guard in. Install them
in one project's `.claude/settings.json` before your user settings, since a hook in user settings runs in
every session you have open, including the ones doing real work.

If you do install them globally later, drop `--settings` from the demo commands or the hooks will fire
twice.
