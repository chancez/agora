package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

func newAckCmd(a *app) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "ack [thread]",
		Short: "Mark a thread read without reading it",
		Long: `Dismiss a thread you have decided is not yours: the other half of triage.

    agora ack docs-rewrite   # I looked at what this is about; it is not my work
    agora ack --all          # none of it is mine

Without it the same thread arrives every turn, and an agent that learns the channel repeats
itself starts ignoring it.

Dismissing is a judgement about relevance, not a way to clear a backlog: nobody will tell you
again. When unsure, read it. That costs a turn, where being wrong costs the thing agora exists
to prevent. --all is required for everything, since a bare ack would be the easiest mistake
here to make.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var thread string
			if len(args) == 1 {
				thread = args[0]
			}
			switch {
			case thread == "" && !all:
				return fmt.Errorf("name a thread to dismiss, or pass --all to dismiss every thread")
			case thread != "" && all:
				return fmt.Errorf("name a thread or pass --all, not both")
			}

			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			result, err := s.Ack(cmd.Context(), store.AckRequest{
				Channel:  cfg.Channel.Value,
				Member:   cfg.Member.Value,
				Thread:   thread,
				Worktree: &cfg.Worktree.Value,
			})
			if err != nil {
				return err
			}
			return a.print(result, func(w io.Writer) {
				if len(result.Threads) == 0 {
					fmt.Fprintf(w, "nothing was waiting in %s\n", describeThread(thread, cfg.Channel.Value))
					return
				}
				fmt.Fprintf(w, "dismissed %s\n", strings.Join(result.Threads, ", "))
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "dismiss every thread with something unread")
	return cmd
}

func describeThread(thread, channel string) string {
	if thread == "" {
		return channel
	}
	return thread
}
