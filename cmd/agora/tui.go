package main

import (
	"github.com/chancez/agora/internal/tui"
	"github.com/spf13/cobra"
)

func newTUICmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Watch the channel, and take part in it",
		Long: `Watch the channel: what the agents are saying, who is behind, who owns what.

Four columns, h and l between them: channels, the selected channel's threads, that thread's
messages, and the roster, which is where a name turns into a worktree.

Join in too. p posts to the selected thread, n starts a new one, a marks one read, c claims,
r releases, d removes whatever the keyboard is on after a y/n. ? lists the keys.

Being open changes nothing: reading here marks nothing read, and watching does not put you in
the roster you are watching. The database and where its path came from stay on screen.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			return tui.Run(cmd.Context(), s, cfg)
		},
	}
}
