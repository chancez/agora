package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

func newThreadsCmd(a *app) *cobra.Command {
	var (
		unreadOnly bool
		mutedOnly  bool
		mineOnly   bool
		limit      int
	)
	cmd := &cobra.Command{
		Use:   "threads",
		Short: "Show the channel's threads and what is unread in each",
		Long: `List the threads in this channel, most recently active first.

This is the index, and it is what makes a busy channel usable. Each thread comes with
how much of it you have not read and the oldest thing you have not read, which is
usually enough to decide whether the thread concerns you at all:

    agora read --thread parser-panic --advance   # it does, so read it
    agora ack parser-panic                       # it does not, so dismiss it

Deciding a thread is not yours is a judgement about relevance, not a way to clear a
backlog. What you dismiss, you are saying you did not need.

--unread narrows to threads with something waiting, which is the triage view. Without
it you get every thread, which is the browsing view. --muted is what you have
dismissed, with what has arrived in each since.

--mine is the threads you have posted in, and the question to ask before opening
another: work that continues something you already announced belongs in the thread
that announced it. It combines with the others, so --mine --unread is your own work
somebody has replied to.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			filter := store.AllThreads
			switch {
			case unreadOnly && mutedOnly:
				return fmt.Errorf("--unread and --muted ask for opposite things: a muted thread is not in your inbox")
			case unreadOnly:
				filter = store.UnreadThreads
			case mutedOnly:
				filter = store.MutedThreads
			}
			author := ""
			if mineOnly {
				author = cfg.Member.Value
			}
			threads, err := s.Threads(cmd.Context(), store.ThreadsRequest{
				Channel:  cfg.Channel.Value,
				Member:   cfg.Member.Value,
				Filter:   filter,
				Author:   author,
				Limit:    limit,
				Worktree: &cfg.Worktree.Value,
			})
			if err != nil {
				return err
			}
			// [] rather than null, so a caller can iterate the result without checking which it got.
			if threads == nil {
				threads = []store.Thread{}
			}
			return a.print(threads, func(w io.Writer) { writeThreads(w, cfg.Channel.Value, threads) })
		},
	}
	cmd.Flags().BoolVar(&unreadOnly, "unread", false, "only threads with something you have not read")
	cmd.Flags().BoolVar(&mutedOnly, "muted", false, "only threads you have muted, with what has piled up in each")
	cmd.Flags().BoolVar(&mineOnly, "mine", false, "only threads you have posted in, which is what you have open")
	cmd.Flags().IntVar(&limit, "limit", 0, "at most N threads, keeping the most recently active, 0 for all")
	return cmd
}

func writeThreads(w io.Writer, channel string, threads []store.Thread) {
	if len(threads) == 0 {
		fmt.Fprintf(w, "no threads in %s\n", channel)
		return
	}
	for i, thread := range threads {
		if i > 0 {
			fmt.Fprintln(w)
		}
		state := fmt.Sprintf("%d unread of %d", thread.Unread, thread.Messages)
		if thread.Unread == 0 {
			state = fmt.Sprintf("nothing unread, %s", plural(int(thread.Messages), "message"))
		}
		line := fmt.Sprintf("%s  %s  %s ago", thread.Name, state, humanAgo(time.Since(thread.LastAt)))
		if thread.Claim != "" {
			line += "  claimed by " + thread.Claim
		}
		fmt.Fprintln(w, line)
		if thread.First != nil {
			// The oldest unread message, because it is the one that says what the thread is about, which
			// is what deciding to read or dismiss turns on.
			for line := range strings.SplitSeq(thread.First.Body, "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
		}
	}
}

// plural and humanAgo are shared with the other human-facing output.
func plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// humanAgo is deliberately coarse: the question is whether a thread is live, and seconds of precision on
// an hour-old timestamp is noise.
func humanAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
