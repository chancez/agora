package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

// postResult is what post prints. Message's fields are promoted rather than nested, so output a caller
// already reads keeps its shape.
type postResult struct {
	store.Message
	// Opened is set when this was the thread's first message. It is the fact worth reporting because
	// opening a thread is the one post that can be wrong on its own: a continuation filed as new work
	// leaves the record split, and nothing else in a post's output says which of the two happened.
	Opened bool `json:"opened,omitempty"`
	// Open is what this author already had going, newest first, when it opened one. Named for the same
	// reason a lost claim names its holder: the output is what stops the duplicate, not the exit code.
	Open []store.Thread `json:"open_threads,omitempty"`
}

// postOpenMax bounds how many of the author's own threads a new one is reported beside. Enough to notice a
// thread this work belongs in, few enough that a channel somebody has worked all day does not print a
// history.
const postOpenMax = 3

func newPostCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "post <thread> <body>",
		Short: "Post a message to the channel",
		Long: `Post a finding to a thread.

Post when something you found changes what someone else should do: a shared root
cause, a refactor that invalidates their assumptions, an answer only you have. A
message nobody needs is a message that teaches the next reader to skip them.

The thread is required and comes first, because it is what lets everybody else decide
whether to read this without reading it. It is also what claims are named after, so a
finding and the claim on the work it implies use one name:

    agora post parser-panic "empty input reaches the token loop with no bounds check"
    agora claim parser-panic --paths parser.go

A body of - reads the message from stdin, which is how to post something with
newlines in it.

A thread is one piece of work, not one turn. Posting to a name nobody has used opens a
thread, and the output says so and names what you already have open, since the next turn
of work you already announced belongs in the thread that announced it. Two threads for
one piece of work split the record, and whoever reads it later cannot tell which half is
current.

    agora threads --mine   # what you have open, before opening another`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			thread, body := args[0], args[1]
			if body == "-" {
				raw, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return fmt.Errorf("read the message from stdin: %w", err)
				}
				body = strings.TrimRight(string(raw), "\n")
			}
			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			msg, err := s.Post(cmd.Context(), store.PostRequest{
				Channel:  cfg.Channel.Value,
				Author:   cfg.Member.Value,
				Thread:   thread,
				Body:     body,
				Worktree: &cfg.Worktree.Value,
			})
			if err != nil {
				return err
			}
			result := postResult{Message: msg, Opened: msg.Number == 1}
			if result.Opened {
				// Only for a new thread. On every other post this would be noise beside the thing that
				// went right, and it is the new thread that nobody else can see is a duplicate yet.
				open, err := s.Threads(cmd.Context(), store.ThreadsRequest{
					Channel: cfg.Channel.Value,
					Member:  cfg.Member.Value,
					Author:  cfg.Member.Value,
					// The one just posted to is the most recently active of them, so ask for one more
					// than will be printed and drop it below.
					Limit: postOpenMax + 1,
					// The post above already recorded this member as present.
					Observe:  true,
					Worktree: &cfg.Worktree.Value,
				})
				if err != nil {
					// The message is written, so this cannot fail the command: an agent that saw a
					// failure would post again, and the channel would carry it twice.
					fmt.Fprintf(a.errOut, "agora post: list your open threads: %v\n", err)
				}
				for _, t := range open {
					if t.Name == msg.Thread {
						continue
					}
					if len(result.Open) == postOpenMax {
						break
					}
					result.Open = append(result.Open, t)
				}
			}
			return a.print(result, func(w io.Writer) {
				if !result.Opened {
					fmt.Fprintf(w, "posted %d to %s\n", msg.Number, msg.Channel)
					return
				}
				fmt.Fprintf(w, "posted %d to %s, opening %s\n", msg.Number, msg.Channel, msg.Thread)
				writeOpenThreads(w, result.Open)
			})
		},
	}
	return cmd
}

// writeOpenThreads is the human form of the notice: what this author already has open, with the age of
// each, since a thread that last moved two days ago is plainly not where this work belongs and one that
// moved a minute ago probably is. Nothing is printed when there is nothing to compare against, because a
// member's first thread cannot be a duplicate of anything.
func writeOpenThreads(w io.Writer, open []store.Thread) {
	if len(open) == 0 {
		return
	}
	fmt.Fprintf(w, "you already have %s open:\n", plural(len(open), "thread"))
	for _, thread := range open {
		fmt.Fprintf(w, "  %s  %s  %s ago\n", thread.Name, plural(int(thread.Messages), "message"),
			humanAgo(time.Since(thread.LastAt)))
	}
	fmt.Fprintf(w, "post to whichever of those this work continues, and keep this one for work that is"+
		" genuinely separate\n")
}
