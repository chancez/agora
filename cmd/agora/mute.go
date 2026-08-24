package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

// newMuteCmd builds both directions, since they are one operation and differ by a word.
func newMuteCmd(a *app, unmute bool) *cobra.Command {
	var all bool
	verb := "mute"
	if unmute {
		verb = "unmute"
	}
	cmd := &cobra.Command{
		Use:   verb + " [thread]",
		Short: map[bool]string{false: "Stop a thread telling you when it moves", true: "Start hearing a muted thread again"}[unmute],
		Long: `Dismiss a thread for good, or take that back.

    agora mute docs-rewrite     # not my work, and I do not want the next message either
    agora mute --all            # only new threads and my own work from here
    agora unmute docs-rewrite   # I was wrong, or my task changed
    agora threads --muted       # what I have muted, and what has piled up in each

This is the difference from ` + "`agora ack`" + `, which says "read to here" and is undone by the next
message in the thread. Muting is a judgement about the thread rather than about what is in it
so far.

It does not read the thread. What arrives while it is muted is kept, so unmuting hands back
what was missed, and a briefing spends one line on muted threads that have something new.

Three things bring a muted thread back on their own: posting to it, claiming it, and somebody
naming you in it. The last is what keeps a mute from making you unreachable.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var thread string
			if len(args) == 1 {
				thread = args[0]
			}
			switch {
			case thread == "" && !all:
				return fmt.Errorf("name a thread to %s, or pass --all for every thread", verb)
			case thread != "" && all:
				return fmt.Errorf("name a thread or pass --all, not both")
			}

			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			result, err := s.Mute(cmd.Context(), store.MuteRequest{
				Channel:  cfg.Channel.Value,
				Member:   cfg.Member.Value,
				Thread:   thread,
				All:      all,
				Unmute:   unmute,
				Worktree: &cfg.Worktree.Value,
			})
			if err != nil {
				return err
			}
			return a.print(result, func(w io.Writer) {
				// An empty list is the ordinary idempotent case rather than a failure, so it says which way
				// round things already were instead of "nothing happened".
				if len(result.Threads) == 0 {
					fmt.Fprintf(w, "nothing to %s in %s: already %sd\n",
						verb, describeThread(thread, cfg.Channel.Value), verb)
					return
				}
				fmt.Fprintf(w, "%sd %s\n", verb, strings.Join(result.Threads, ", "))
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, verb+" every thread in the channel")
	return cmd
}
