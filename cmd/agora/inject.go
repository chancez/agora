package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

// injectEvent is the part of a SessionStart or UserPromptSubmit event agora reads.
type injectEvent struct {
	HookEventName string `json:"hook_event_name"`
	CWD           string `json:"cwd"`
	// SessionID is the session being briefed, and identity comes from it rather than from the environment.
	// A harness need not put its session id in a hook's environment, and one that does not would have this
	// brief a different member than the agent's own commands are, so the agent would be told about its own
	// messages and never told about anybody else's.
	SessionID string `json:"session_id"`
}

type injectOutput struct {
	HookSpecificOutput injectContext `json:"hookSpecificOutput"`
}

type injectContext struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// Bounds on what reaches the model. Hook output over 10000 characters is spilled to a file and replaced
// with a preview, so an unbounded dump of unread silently stops delivering the messages the hook exists
// to deliver. Each of these is checked independently, because one long note can blow the budget as
// easily as fifty short messages.
const (
	injectBudget     = 6000
	injectBodyLimit  = 500
	injectNoteLimit  = 300
	injectPathsLimit = 200
	injectClaimsMax  = 10
	injectMutedMax   = 5
)

func newInjectCmd(a *app) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "inject",
		Short: "Hook entry point: put unread messages and standing claims into an agent's context",
		Long: `Read a SessionStart or UserPromptSubmit hook event on stdin and write the unread threads,
plus the claims other agents hold, as additional context.

The layer that cannot be forgotten: an instruction to check the channel competes with
whatever the user just asked for, and a hook does not.

It writes an index rather than a transcript, since an agent that learns the channel is
mostly noise stops reading it. The list of threads is never abridged. It prints nothing
when there is nothing to say, which is most turns, and it never advances a cursor: a hook
cannot know whether its output reached the model, so acknowledging is the agent's own
"agora read --advance" or "agora ack NAME".

On a prompt it also asks a member with nothing of its own in the channel to open a thread
for the work this turn starts, since a session that only ever reads leaves the record
one-sided. That stops once the member posts or claims anything.

SessionStart also reports standing claims, since a session whose cursor is already current
would otherwise start knowing nothing about who owns what. Wire both, one query each:

    { "hooks": { "SessionStart": [ { "hooks": [
        { "type": "command", "command": "agora inject" } ] } ],
      "UserPromptSubmit": [ { "hooks": [
        { "type": "command", "command": "agora inject" } ] } ] } }

With no event on stdin it briefs the current directory, which is how to see what a session
would be told.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			injected, err := a.inject(cmd.Context(), cmd.InOrStdin(), limit)
			if err != nil {
				// A hook that fails loudly on every turn gets removed, and then nothing is
				// delivered at all. stderr keeps the failure findable in the transcript.
				fmt.Fprintf(a.errOut, "agora inject: %v\n", err)
				return nil
			}
			if injected == nil {
				return nil
			}
			return json.NewEncoder(a.out).Encode(injectOutput{HookSpecificOutput: *injected})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 10, "at most this many unread threads")
	return cmd
}

func (a *app) inject(ctx context.Context, stdin io.Reader, limit int) (*injectContext, error) {
	event := injectEvent{HookEventName: "SessionStart"}
	// Not read when stdin is a terminal. A hook writes an event and closes, but a person running this by
	// hand to see what a session would be told has a terminal there instead, and decoding from it waits for
	// an event that is never coming. This only covers a terminal: stdin inherited from something that holds
	// a pipe open still blocks, which is why the docs run it as `agora inject </dev/null`.
	if !isTerminal(stdin) {
		if err := json.NewDecoder(stdin).Decode(&event); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read the hook event: %w", err)
		}
	}
	a.flags.SessionID = event.SessionID
	if event.CWD != "" {
		// Where the agent is, which is not necessarily where the harness ran this.
		a.resolver.Dir = event.CWD
	}
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	// No database means no channel yet, so there is nothing to say and no reason to create one on
	// somebody's session start.
	exists, err := fileExists(cfg.Database.Value)
	if err != nil || !exists {
		return nil, err
	}
	s, _, err := a.open()
	if err != nil {
		return nil, err
	}

	threads, err := s.Threads(ctx, store.ThreadsRequest{
		Channel:  cfg.Channel.Value,
		Member:   cfg.Member.Value,
		Filter:   store.UnreadThreads,
		Worktree: &cfg.Worktree.Value,
	})
	if err != nil {
		return nil, err
	}
	// Muted threads are not in the index, so they get one line and no bodies: enough to notice, not enough to
	// undo the mute. A second call because the two filters are one field and cannot both be applied.
	muted, err := s.Threads(ctx, store.ThreadsRequest{
		Channel:  cfg.Channel.Value,
		Member:   cfg.Member.Value,
		Filter:   store.MutedThreads,
		Observe:  true,
		Worktree: &cfg.Worktree.Value,
	})
	if err != nil {
		return nil, err
	}
	stale := make([]store.Thread, 0, len(muted))
	for _, thread := range muted {
		if thread.Unread > 0 {
			stale = append(stale, thread)
		}
	}
	claims, err := s.Claims(ctx, cfg.Channel.Value)
	if err != nil {
		return nil, err
	}
	held := make([]store.Claim, 0, len(claims))
	for _, claim := range claims {
		// Your own claims are not news to you.
		if claim.Holder != cfg.Member.Value {
			held = append(held, claim)
		}
	}

	// The roster says what to ask this member, and one of those questions is the reason a prompt is not
	// allowed to stay quiet, so it is read before deciding whether there is anything to say. One more
	// sub-millisecond query on a turn that already ran two.
	members, err := s.Members(ctx, cfg.Channel.Value)
	if err != nil {
		return nil, err
	}
	self := selfIn(members, claims, cfg.Member.Value)

	sessionStart := event.HookEventName == "SessionStart"
	// Announcing is asked for on a prompt and nowhere else: the prompt is where the work is named, where
	// SessionStart arrives before the agent has been given anything to announce and PostToolUse would repeat
	// it beside every tool result of the turn.
	announce := self.announce && event.HookEventName == "UserPromptSubmit"
	// News from the channel, as opposed to a question about the reader. The distinction is what keeps the
	// standing claims from being repeated every prompt: they are a session-start briefing, and the nudge below
	// is not a reason to send them again.
	news := len(threads) > 0 || len(stale) > 0 || (sessionStart && len(held) > 0)
	if !news && !announce {
		return nil, nil
	}
	if !news {
		held, stale = nil, nil
	}
	return &injectContext{
		HookEventName: event.HookEventName,
		// describe rides along with news rather than with the nudge: in a channel where nothing has been said,
		// a description is a note to nobody.
		AdditionalContext: injectText(threads, stale, held, self.describe && news, announce, cfg.Member.Value, limit),
	}, nil
}

// selfState is what this member's own row says to ask it, which is the part of the briefing that is about the
// reader rather than about the channel.
type selfState struct {
	// describe is set when it has not said what it is doing, so the roster carries a session id and nothing
	// else. No check for whether anybody else is here: it rides along with unread messages, which are by
	// definition from another member.
	describe bool
	// announce is set when it has nothing of its own here, no message it wrote and no claim it holds. That is
	// the state a session is in while it starts work nobody else can see, and taking part in any way at all
	// ends it: a member that posted once knows the channel is there.
	announce bool
}

func selfIn(members []store.Member, claims []store.Claim, member string) selfState {
	self := selfState{describe: true, announce: true}
	for _, m := range members {
		if m.Name == member {
			self.describe = m.Description == ""
			self.announce = m.Posts == 0
			break
		}
	}
	for _, claim := range claims {
		if claim.Holder == member {
			self.announce = false
			break
		}
	}
	return self
}

// injectText is what the model reads: an index rather than a transcript. Every unread thread is named with its
// count and its oldest unread message, and nothing else, since an agent that learns the channel is mostly noise
// stops reading it. The list of threads is never abridged.
//
// It reports rather than instructs, apart from triage and the two questions about the reader: this arrives
// alongside what the user actually asked for, and an agent that treats the channel as a new task is as wrong as
// one that ignores it.
func injectText(threads, muted []store.Thread, held []store.Claim, describe, announce bool, member string, limit int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[agora] You are %q in the channel for this repository.\n", member)

	if len(threads) > 0 {
		messages := int64(0)
		for _, thread := range threads {
			messages += thread.Unread
		}
		para(&b)
		fmt.Fprintf(&b, "%s unread in %s:\n",
			plural(int(messages), "message"), plural(len(threads), "thread"))
		for i, thread := range threads {
			if limit > 0 && i == limit {
				// Naming the count rather than trailing off, because an index that looks complete and
				// is not is worse than one that admits where it stopped.
				fmt.Fprintf(&b, "\n  and %s with unread, see `agora threads --unread`\n",
					plural(len(threads)-i, "more thread"))
				break
			}
			fmt.Fprintf(&b, "\n  %s  (%d unread", thread.Name, thread.Unread)
			if thread.Claim != "" && thread.Claim != member {
				fmt.Fprintf(&b, ", claimed by %s", thread.Claim)
			}
			b.WriteString(")\n")
			if thread.First != nil {
				for line := range strings.SplitSeq(truncate(thread.First.Body, injectBodyLimit), "\n") {
					fmt.Fprintf(&b, "      %s\n", line)
				}
			}
		}
	}

	if len(muted) > 0 {
		// One line, names and counts, no bodies. A mute has to stay discoverable or it is a deletion, and it
		// has to stay quiet or it is not a mute. Bounded like everything else here: a channel can hold more
		// muted threads than a briefing should carry.
		para(&b)
		b.WriteString("muted, with new messages:")
		for i, thread := range muted {
			if i == injectMutedMax {
				fmt.Fprintf(&b, " and %d more,", len(muted)-i)
				break
			}
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, " %s (%d)", thread.Name, thread.Unread)
		}
		b.WriteString(" `agora unmute NAME` to hear one of them again.\n")
	}

	if len(held) > 0 {
		para(&b)
		b.WriteString("Work other agents have claimed:\n")
		for i, claim := range held {
			if i == injectClaimsMax {
				fmt.Fprintf(&b, "\n  and %d more, see `agora claims`\n", len(held)-i)
				break
			}
			fmt.Fprintf(&b, "\n  %s  held by %s for %s\n", claim.Thread, claim.Holder,
				time.Since(claim.CreatedAt).Round(time.Minute))
			if len(claim.Paths) > 0 {
				// A glob list has no natural length, and nothing stops a claim naming fifty.
				fmt.Fprintf(&b, "      covering %s\n", truncate(strings.Join(claim.Paths, ", "), injectPathsLimit))
			}
			if claim.Note != "" {
				fmt.Fprintf(&b, "      note: %s\n", truncate(claim.Note, injectNoteLimit))
			}
		}
	}

	if len(threads) > 0 {
		// Triage spelled out, since an index is only useful if the reader knows what to do with it. Three
		// branches: dismissing stays as available as reading, or the unread piles up and all of it gets
		// ignored, and answering is named because neither of the others does it.
		//
		// One paragraph on purpose. A longer version measured no better over five runs an arm, and this text
		// is in context every turn. scripts/reply-experiment.sh has the numbers.
		para(&b)
		b.WriteString("Decide per thread from the lines above. `agora read --thread NAME --advance` for" +
			" one that concerns your work, `agora post NAME \"...\"` to answer one that asked you" +
			" something, `agora mute NAME` for one that does not concern you: dismissing is a judgement" +
			" about relevance, and a muted thread stops telling you when it moves. Reading a thread marks" +
			" it read for you and tells whoever posted it nothing, so one you read and did not answer is" +
			" one somebody is still waiting on. Until you read or dismiss a thread, it arrives again" +
			" next turn.")
	}
	if announce {
		// The gap this closes, watched happening: a session read the one thread waiting, judged it correctly,
		// then started a feature of its own and posted nothing. Nothing here had asked it to, and an agent
		// satisfies the instruction it was given.
		//
		// Conditional on purpose. A hook cannot tell a question from a piece of work, so the condition is left
		// to the only reader that can, and the nudge stops for good once anything of this member's is here.
		para(&b)
		b.WriteString("Nothing in this channel is from you yet. If this turn starts a piece of work, a" +
			" feature, a bug fix, an investigation, open a thread for it before you edit: `agora post NAME" +
			" \"what you are about to do\"`, then `agora claim NAME --note \"...\"` so an agent starting" +
			" beside you reads it instead of doing it again. A thread nobody needed costs one line, and two" +
			" agents fixing one bug twice is what this exists to stop.")
	}
	if describe {
		// Only when the channel already had something to say, so this costs nothing on a quiet turn. It
		// repeats until answered, which is the same rule the unread threads follow, and it stops for good the
		// moment the member says anything.
		para(&b)
		b.WriteString("Nobody here knows what you are working on. Your name is your session, so it says" +
			" nothing on its own: `agora join --description \"...\"` puts one line about your work beside it" +
			" in the roster, which is what somebody deciding whether to ask you or wait for you has to go on.")
	}
	if len(held) > 0 {
		// Measured: an agent given only "post rather than start a second fix" posts, gets no reply, and
		// then patches the claimed file anyway, because nothing told it who decides. The holder cannot be
		// waited on and a peer cannot grant permission, so the decision is the user's.
		para(&b)
		b.WriteString("If your task needs a file one of those claims covers, leave it alone: post to that" +
			" thread with what you found, then tell your user who holds it and what their note says." +
			" Editing it anyway is your user's decision to make, not yours and not a peer's. Work covered" +
			" by no claim is yours to get on with.")
	}
	b.WriteString(" The agora skill has the protocol.\n")

	return truncate(b.String(), injectBudget)
}

// para separates one block from the last, so blocks can be written in any combination without each one having
// to know what came before it: a blank line between paragraphs, and no stray blank line when the block above
// already ended in one. Every block is optional, and the combinations multiply.
func para(b *strings.Builder) {
	s := b.String()
	switch {
	case s == "", strings.HasSuffix(s, "\n\n"):
	case strings.HasSuffix(s, "\n"):
		b.WriteByte('\n')
	default:
		b.WriteString("\n\n")
	}
}
