package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

func newWatchCmd(a *app) *cobra.Command {
	var (
		once     bool
		after    int64
		interval time.Duration
		timeout  time.Duration
	)
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Block until a new message arrives",
		Long: `Block and print each new message as it is posted, until interrupted.

agora's own subscribe, so waiting for a post needs no daemon and no other tool. It starts
from the newest message; existing unread is what agora read is for.

--once turns "wait for a post" into an exit code, which is the composable form:

    agora watch --once --timeout 2m >/dev/null && agora read --advance

--timeout bounds the wait, and anything but a person waiting on this should bound it: an
unbounded watch in a tool call hangs the turn until the harness kills it. With --once a
timeout exits 1, so the line above reads as "if something arrived, go read it".

Output is one compact JSON object per line, so a stream can be read a line at a time.
--after N replays from a message's seq in this channel, delivering the backlog immediately.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if timeout > 0 {
				// The alternative is telling every caller to wrap this in timeout(1), which does
				// not exist on darwin.
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			opts := store.WatchOptions{Channel: cfg.Channel.Value, Interval: interval}
			if cmd.Flags().Changed("after") {
				opts.After = &after
			}
			watcher := s.Watch(ctx, opts)
			delivered := 0

			// One object per line, compact and flushed as it arrives: a watcher's output is read by
			// whatever is waiting on it, and a buffered stream would defeat the point.
			encoder := json.NewEncoder(a.out)
			for msg := range watcher.Messages() {
				if a.text {
					writeMessage(a.out, msg)
				} else if err := encoder.Encode(msg); err != nil {
					return err
				}
				delivered++
				if once {
					return watcher.Err()
				}
			}
			// A closed channel means the context ended, which is how this command is meant to end,
			// or that the watch failed, which is not. Err is the only thing that distinguishes them:
			// a quiet channel and a broken one look identical otherwise.
			if err := watcher.Err(); err != nil {
				return err
			}
			if once && delivered == 0 {
				// Asked for one message and got none, so `watch --once && read` does not go on to
				// read nothing. A plain watch that ran out its timeout did what was asked and exits
				// 0.
				return exitCode(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "exit after the first message")
	cmd.Flags().Int64Var(&after, "after", 0, "start after this seq in the channel instead of from now")
	cmd.Flags().DurationVar(&interval, "interval", store.DefaultWatchInterval, "how often to check for new messages")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "give up after this long, 0 to wait forever")
	return cmd
}
