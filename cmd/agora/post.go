package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

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

A thread is one piece of work, not one turn: posting to a name nobody has used opens one,
and follow-up on work you already announced belongs in the thread that announced it. When
it really is separate, name the new thread in the one it came out of, so a reader of
either can find the other.

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
			return a.print(msg, func(w io.Writer) {
				fmt.Fprintf(w, "posted %d to %s\n", msg.Number, msg.Channel)
			})
		},
	}
	return cmd
}
