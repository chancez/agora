package main

import (
	"fmt"
	"io"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

func newJoinCmd(a *app) *cobra.Command {
	var (
		notify      string
		description string
	)
	cmd := &cobra.Command{
		Use:   "join",
		Short: "Join the channel, say what you are doing, or update how to reach you",
		Long: `Join the channel for this repository. Idempotent: rejoining keeps your cursors.

--description is one line about the work you are doing, and worth setting. Your name is your
session, so a roster of claude-3fb7e7b0 and claude-9ddf2b73 says how many agents are here and
nothing about which to ask. The work, not yourself.

    agora join --description "fixing the empty-input panic in the parser and lexer"

--notify records a command to run when you have unread. A nudge, never a delivery guarantee,
so it should carry the count and channel rather than the message.

    agora join --notify "cm send my-session '[agora] {n} unread in {channel}' --enter"

Omitting either leaves what is recorded alone. An empty string clears one.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			req := store.JoinRequest{
				Channel:  cfg.Channel.Value,
				Member:   cfg.Member.Value,
				Worktree: &cfg.Worktree.Value,
			}
			// Only send what was asked for: the store distinguishes "leave it alone" from "set it
			// to empty", and a join without --notify must not wipe a nudge command an earlier join
			// configured.
			if cmd.Flags().Changed("notify") {
				req.Notify = &notify
			}
			if cmd.Flags().Changed("description") {
				req.Description = &description
			}
			member, err := s.Join(cmd.Context(), req)
			if err != nil {
				return err
			}
			return a.print(member, func(w io.Writer) {
				fmt.Fprintf(w, "joined %s as %s", member.Channel, member.Name)
				if member.Description != "" {
					fmt.Fprintf(w, ", %s", member.Description)
				}
				fmt.Fprintf(w, ", %d unread across %d threads\n", member.Unread, member.UnreadThreads)
			})
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "one line about the work you are doing")
	cmd.Flags().StringVar(&notify, "notify", "", "command to run when you have unread messages")
	return cmd
}
