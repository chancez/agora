package main

import (
	"fmt"
	"io"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

func newDumpCmd(a *app) *cobra.Command {
	var (
		thread string
		limit  int
		after  int64
	)
	cmd := &cobra.Command{
		Use:   "dump",
		Short: "Print the channel's history, ignoring cursors",
		Long: `Print the record itself, without touching anyone's cursor.

This is what makes the channel readable by a human, which is why the messages live in
sqlite rather than in text files: readability is a command, not a storage decision.

--limit keeps the newest, since a truncated history is more useful from the recent
end.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			messages, err := s.Messages(cmd.Context(), store.MessagesRequest{
				Channel: cfg.Channel.Value,
				Thread:  thread,
				After:   after,
				Limit:   limit,
			})
			if err != nil {
				return err
			}
			// [] rather than null on an empty channel, so a caller can iterate the result without
			// checking which of the two it got.
			if messages == nil {
				messages = []store.Message{}
			}
			return a.print(messages, func(w io.Writer) {
				if len(messages) == 0 {
					fmt.Fprintf(w, "no messages in %s\n", cfg.Channel.Value)
					return
				}
				fmt.Fprintf(w, "%d messages in %s\n", len(messages), cfg.Channel.Value)
				for _, msg := range messages {
					fmt.Fprintln(w)
					writeMessage(w, msg)
				}
			})
		},
	}
	cmd.Flags().StringVar(&thread, "thread", "", "only messages under this thread")
	cmd.Flags().IntVar(&limit, "limit", 0, "return at most N messages, keeping the newest, 0 for all")
	cmd.Flags().Int64Var(&after, "after", 0, "only messages with an id greater than this")
	return cmd
}
