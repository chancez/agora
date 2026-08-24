package main

import (
	"fmt"
	"io"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

func newChannelsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "channels",
		Short: "Show every channel in this database",
		Long: `List every channel, with how many threads each has and how many of them you have
not read.

A channel is one repository, so this is the list of projects with a record in this
database, and the one you are in is marked. Every command that names a channel creates
it, which means running agora anywhere leaves a channel there, so this is also how you
find the ones nothing ever used:

    agora channels
    agora delete-channel /path/to/some/repo

This is a read: it reports what is there without creating the channel you are standing
in, unlike every other command here.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			channels, err := s.Channels(cmd.Context(), cfg.Member.Value)
			if err != nil {
				return err
			}
			// [] rather than null, so a caller can iterate the result without checking which it got.
			if channels == nil {
				channels = []store.Channel{}
			}
			return a.print(channels, func(w io.Writer) { writeChannels(w, cfg.Channel.Value, channels) })
		},
	}
	return cmd
}

func writeChannels(w io.Writer, current string, channels []store.Channel) {
	if len(channels) == 0 {
		fmt.Fprintln(w, "no channels in this database")
		return
	}
	for _, channel := range channels {
		// The channel this invocation is in, which is the one every other command means.
		marker := "  "
		if channel.Key == current {
			marker = "* "
		}
		state := plural(int(channel.Threads), "thread")
		if channel.Threads == 0 {
			// What "never used" looks like, and the whole reason for reading this list.
			state = "nothing in it"
		}
		if channel.UnreadThreads > 0 {
			state += fmt.Sprintf(", %d unread", channel.UnreadThreads)
		}
		fmt.Fprintf(w, "%s%s  %s  %s old\n", marker, channel.Key, state,
			humanAgo(time.Since(channel.CreatedAt)))
	}
}
