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
		related bool
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

--related reads the threads this one is linked to as well, which is how the other half of
work that got split arrives. It carries what you have already read in them, and that is
the point rather than a detail: a thread with something unread is in your inbox already,
so the only thread a link can add is one you have read, and reading that on its own says
"no unread". It moves no cursor but this thread's, with or without --advance.

--limit caps how many come back and reports what was left behind. A hook should pass one:
hook output over 10000 characters is spilled to a file and replaced with a preview.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			if related && thread == "" {
				// Reading every thread already delivers every unread message in the channel, so there is
				// nothing for this to add and a caller asking for it has misunderstood what it does.
				return fmt.Errorf("--related needs --thread: a read of every thread already carries every unread message")
			}
			result, err := s.Read(cmd.Context(), store.ReadRequest{
				Channel:  cfg.Channel.Value,
				Member:   cfg.Member.Value,
				Thread:   thread,
				Advance:  advance,
				Related:  related,
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
	cmd.Flags().BoolVar(&related, "related", false,
		"read the threads this one is linked to as well, including what you have already read in them")
	cmd.Flags().IntVar(&limit, "limit", 0, "return at most N messages, 0 for all of them")
	return cmd
}

func writeUnread(w io.Writer, result store.ReadResult, advanced bool) {
	if len(result.Messages) == 0 {
		fmt.Fprintf(w, "no unread in %s as %s\n", result.Channel, result.Member)
		// Still worth saying here: a thread you have already read is exactly where somebody put the pointer to
		// the half of the work that went elsewhere.
		writeRelated(w, result.Related)
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
	writeRelated(w, result.Related)
	if advanced {
		fmt.Fprintf(w, "marked read: %s\n", threadList(result.Cursors))
		return
	}
	// Saying this every time is the point: a reader who does not know nothing was marked read will
	// wonder why the same messages keep arriving.
	fmt.Fprintf(w, "nothing marked read, run `agora read --advance` to mark these read,"+
		" or `agora ack --thread NAME` to dismiss one without reading it\n")
}

// writeRelated names the other half of work that got split. Nothing in it was read and no cursor moved, so it
// says what is waiting rather than showing it: following the pointer is the reader's decision, and one read that
// silently consumed two threads would be the failure the display and advance split exists to prevent.
func writeRelated(w io.Writer, related []store.RelatedRead) {
	if len(related) == 0 {
		return
	}
	names := make([]string, 0, len(related))
	carried := false
	for _, entry := range related {
		state := "nothing unread"
		if entry.Thread.Unread > 0 {
			state = fmt.Sprintf("%d unread", entry.Thread.Unread)
		}
		if entry.Thread.Muted {
			state += ", muted"
		}
		names = append(names, fmt.Sprintf("%s (%s)", entry.Thread.Name, state))
		carried = carried || len(entry.Messages) > 0
	}
	fmt.Fprintf(w, "related: %s\n", strings.Join(names, ", "))
	if !carried {
		fmt.Fprintf(w, "  `agora read --thread %s --related` to read one with it, nothing here was read\n",
			related[0].Thread.Name)
		return
	}
	for _, entry := range related {
		if len(entry.Messages) == 0 {
			continue
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  from %s", entry.Thread.Name)
		if entry.Omitted > 0 {
			fmt.Fprintf(w, ", %s before these", plural(entry.Omitted, "message"))
		}
		fmt.Fprintln(w, ":")
		for _, msg := range entry.Messages {
			writeMessage(w, msg)
		}
	}
	fmt.Fprintln(w)
	// Said because it is the surprising half: these were delivered and none of them was marked read, so they
	// arrive again next turn unless their own thread is read.
	fmt.Fprintf(w, "nothing in the related threads was marked read; `agora read --thread NAME --advance` does that\n")
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
