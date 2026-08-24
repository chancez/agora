package main

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

func newReadCmd(a *app) *cobra.Command {
	var (
		thread  string
		advance bool
		limit   int
	)
	cmd := &cobra.Command{
		Use:   "read",
		Short: "Show your unread messages",
		Long: `Show what you have not read yet.

Reading does not consume: the cursor moves only with --advance. The two failures are not
symmetric. Seeing a message twice is noise, where marking one read whose output never
reached the model loses it with nothing recording that it happened. So a hook runs
"agora read" and something that knows the messages arrived runs "agora read --advance".

--thread reads one and leaves the rest unread, which is what following up on the index
looks like:

    agora threads --unread                       # what is waiting, and what each is about
    agora read --thread parser-panic --advance   # this one matters, so read it
    agora ack docs-rewrite                       # this one does not

--limit caps how many come back and reports what was left behind. A hook should pass one:
hook output over 10000 characters is spilled to a file and replaced with a preview.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			result, err := s.Read(cmd.Context(), store.ReadRequest{
				Channel:  cfg.Channel.Value,
				Member:   cfg.Member.Value,
				Thread:   thread,
				Advance:  advance,
				Limit:    limit,
				Worktree: &cfg.Worktree.Value,
			})
			if err != nil {
				return err
			}
			// An empty list is [] rather than null. Agents are the primary caller, and iterating a
			// null crashes the caller where iterating an empty list does nothing, which is what
			// "no unread" should cost.
			if result.Messages == nil {
				result.Messages = []store.Message{}
			}
			return a.print(result, func(w io.Writer) { writeUnread(w, result, advance) })
		},
	}
	cmd.Flags().StringVar(&thread, "thread", "", "only this thread, leaving every other thread's unread alone")
	cmd.Flags().BoolVar(&advance, "advance", false, "mark the messages read by moving the cursor")
	cmd.Flags().IntVar(&limit, "limit", 0, "return at most N messages, 0 for all of them")
	return cmd
}

func writeUnread(w io.Writer, result store.ReadResult, advanced bool) {
	if len(result.Messages) == 0 {
		fmt.Fprintf(w, "no unread in %s as %s\n", result.Channel, result.Member)
		return
	}
	fmt.Fprintf(w, "%d unread in %s as %s\n", len(result.Messages), result.Channel, result.Member)
	for _, msg := range result.Messages {
		fmt.Fprintln(w)
		writeMessage(w, msg)
	}
	fmt.Fprintln(w)
	if result.Remaining > 0 {
		fmt.Fprintf(w, "%d more unread, not shown\n", result.Remaining)
	}
	if advanced {
		fmt.Fprintf(w, "marked read: %s\n", threadList(result.Cursors))
		return
	}
	// Saying this every time is the point: a reader who does not know nothing was marked read will
	// wonder why the same messages keep arriving.
	fmt.Fprintf(w, "nothing marked read, run `agora read --advance` to mark these read,"+
		" or `agora ack --thread NAME` to dismiss one without reading it\n")
}

// threadList names the threads a cursor moved on, sorted so the output does not vary between runs.
func threadList(cursors map[string]int64) string {
	names := make([]string, 0, len(cursors))
	for name := range cursors {
		names = append(names, name)
	}
	slices.Sort(names)
	if len(names) == 0 {
		return "nothing"
	}
	return strings.Join(names, ", ")
}

func writeMessage(w io.Writer, msg store.Message) {
	// The number in the thread rather than the channel sequence, since that is what a reader counts, and each
	// line names its thread anyway.
	header := fmt.Sprintf("  #%d  %s  %s", msg.Number, msg.Author, msg.CreatedAt.Local().Format(time.DateTime))
	if msg.Thread != "" {
		header += "  thread " + msg.Thread
	}
	fmt.Fprintln(w, header)
	// Indent every line, since a finding worth posting is often several of them.
	for line := range strings.SplitSeq(msg.Body, "\n") {
		fmt.Fprintf(w, "      %s\n", line)
	}
}
