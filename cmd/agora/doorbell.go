package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

// Bounds on what a wake carries. Hook output over 10000 characters is spilled to a file and replaced with a
// preview, and this one is deliberately tighter than the briefing's: a wake arrives in the middle of somebody
// else's session, and what it is for is getting the agent to go and read the thread rather than reproducing it.
const (
	wakeBudget    = 2000
	wakeBodyLimit = 400
)

func newDoorbellCmd(a *app) *cobra.Command {
	var (
		wait     time.Duration
		interval time.Duration
		limit    int
		dryRun   bool
	)
	cmd := &cobra.Command{
		Use:   "doorbell",
		Short: "Hook entry point: wake this agent when a message it should answer arrives",
		Long: `Read a Stop hook event on stdin and, if something in the channel is addressed to this
member, say so on stderr and exit 2.

The one layer that reaches an agent nobody is talking to. Everything else agora puts in
front of an agent is delivered while that agent is doing something: starting, being
prompted, attempting an edit. A session sitting at a prompt does none of those, so a thread
addressed to it waits for its user to type.

Addressed rather than unread, because a wake costs a turn: a message rings only if it names
this member, or lands in a thread this member has posted in or holds the claim on. It rings
once per message, so an agent that read a wake and decided not to answer is not woken about
it again.

Two wirings, and exit 2 is what carries both. Without --wait it looks once, which reaches a
message that landed during the turn:

    { "hooks": { "Stop": [ { "hooks": [
        { "type": "command", "command": "agora doorbell" } ] } ] } }

With --wait it keeps waiting, which is the only path that reaches a session already parked
at a prompt. asyncRewake is what makes that legal: per the hooks reference it "runs in the
background and wakes Claude on exit code 2", and the hook's stderr is what Claude is shown.

    { "hooks": { "Stop": [ { "hooks": [
        { "type": "command", "command": "agora doorbell --wait 30m",
          "asyncRewake": true } ] } ] } }

Bound the wait. The hook fires once a turn, so an unbounded one would leave a process per
turn waiting forever; a doorbell started later takes the wait over from one left behind by an
earlier turn, and the leftover stops at the next message or at its own timeout.

Codex takes exit 2 on Stop the same way, so the first wiring works there. It has nothing like
asyncRewake, and a background hook there cannot control the turn it came from, so --wait would
hold the turn open instead of outliving it: on Codex, wire it without --wait.

--dry-run, and a terminal on stdin, mean look rather than ring: the result goes to stdout and
nothing is recorded, so seeing what is waiting does not spend the wake for it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// A terminal on stdin means nobody is going to hand this an event, so a person running it is
			// looking rather than wiring it up. Same reasoning as inject, and --dry-run says it explicitly
			// for a script, which has a pipe on stdin like a hook does.
			hook := !dryRun && !isTerminal(cmd.InOrStdin())
			rang, err := a.doorbell(cmd.Context(), cmd.InOrStdin(), hook, wait, interval, limit)
			if err != nil {
				// Never the session's problem. This runs at the end of every turn, and a hook that fails
				// loudly there gets removed, after which nothing wakes anybody. On exit 0 stderr is not
				// shown to the model, so the failure stays findable without being delivered as news.
				fmt.Fprintf(a.errOut, "agora doorbell: %v\n", err)
				return nil
			}
			if rang == nil {
				return nil
			}
			if !hook {
				return a.print(rang, func(w io.Writer) { writeWake(w, *rang) })
			}
			if len(rang.Messages) == 0 {
				// Silence is the ordinary answer, and a hook that says something every turn is one whose
				// output stops being read.
				return nil
			}
			// stderr, because that is what the harness shows the model: an asyncRewake hook is read from
			// stderr first, and a plain Stop hook's exit 2 is read from stderr only.
			writeWake(a.errOut, *rang)
			return exitCode(2)
		},
	}
	cmd.Flags().DurationVar(&wait, "wait", 0,
		"keep waiting this long for something addressed to this member, 0 to look once")
	cmd.Flags().DurationVar(&interval, "interval", store.DefaultWatchInterval,
		"how often to check for new messages while waiting")
	cmd.Flags().IntVar(&limit, "limit", 5, "at most this many messages in one wake")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would wake this member and record nothing")
	return cmd
}

// doorbell answers an event, returning nil for "say nothing at all".
func (a *app) doorbell(ctx context.Context, stdin io.Reader, hook bool, wait, interval time.Duration, limit int) (*store.DoorbellResult, error) {
	event := hookEvent{HookEventName: "Stop"}
	if hook {
		// A body that is not an event is not fatal, the same as leave: identity falls back to the usual
		// chain rather than the doorbell refusing to ring.
		if err := json.NewDecoder(stdin).Decode(&event); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read the hook event: %w", err)
		}
	}
	if event.StopHookActive {
		// This turn is already running because a stop hook said it should, so waking it again is how a
		// doorbell becomes a loop that never lets a session finish.
		return nil, nil
	}
	a.flags.SessionID = event.SessionID
	if event.CWD != "" {
		// Where the agent is, which is not necessarily where the harness ran this.
		a.resolver.Dir = event.CWD
	}
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	// No database means nobody has said anything, so there is nothing to wake anybody for and no reason to
	// create one at the end of every turn.
	exists, err := fileExists(cfg.Database.Value)
	if err != nil || !exists {
		return nil, err
	}
	s, _, err := a.open()
	if err != nil {
		return nil, err
	}

	req := store.DoorbellRequest{
		Channel: cfg.Channel.Value,
		Member:  cfg.Member.Value,
		Limit:   limit,
		// Looking is not ringing: run by hand this reports what is waiting and leaves the wake for the
		// hook that would deliver it.
		DryRun: !hook,
	}
	if wait > 0 {
		req.Waiter = waiterToken()
	}
	rang, err := s.Doorbell(ctx, req)
	if err != nil {
		return nil, err
	}
	if wait <= 0 || len(rang.Messages) > 0 || rang.Waiter != req.Waiter {
		return &rang, nil
	}

	// Nothing waiting, so wait. The watch is a subscription rather than a poll of the message table, and
	// every message is a reason to ask the store again: which of them is addressed to this member is the
	// store's question, not this one's.
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	watcher := s.Watch(ctx, store.WatchOptions{Channel: cfg.Channel.Value, Interval: interval})
	for range watcher.Messages() {
		next, err := s.Doorbell(ctx, req)
		if err != nil {
			// A query caught by the timeout reports its own cancellation, which is this command working
			// as documented rather than a failure worth printing. Same reasoning as watch.
			if ctx.Err() != nil {
				return &rang, nil
			}
			return nil, err
		}
		rang = next
		// A doorbell from a later turn has taken the wait, so this one is the leftover of an earlier turn:
		// it stops rather than sitting on a connection until its timeout.
		if len(rang.Messages) > 0 || rang.Waiter != req.Waiter {
			return &rang, nil
		}
	}
	if err := watcher.Err(); err != nil {
		return nil, err
	}
	return &rang, nil
}

// waiterToken names this process among the doorbells waiting on one member, and has to sort
// chronologically: that ordering is what decides which of two waiting processes is the leftover of an
// earlier turn. Zero padded so the comparison is lexicographic, and the pid breaks a tie between two
// started in the same nanosecond.
func waiterToken() string {
	return fmt.Sprintf("%020d-%d", time.Now().UnixNano(), os.Getpid())
}

// writeWake is what the woken agent reads. It says who is talking, in which thread, and what the oldest
// message says, and then what to do, because an agent woken by nobody has no task: the turn it is about to
// spend was not asked for by its user, and starting work on the strength of that is the failure mode.
func writeWake(w io.Writer, rang store.DoorbellResult) {
	if len(rang.Messages) == 0 {
		fmt.Fprintf(w, "[agora] nothing is addressed to %s in %s.\n", rang.Member, rang.Channel)
		return
	}
	var b strings.Builder
	threads := map[string]bool{}
	for _, msg := range rang.Messages {
		threads[msg.Thread] = true
	}
	fmt.Fprintf(&b, "[agora] %s, %s addressed to you in %s.\n",
		rang.Member, plural(len(rang.Messages), "message"), plural(len(threads), "thread"))
	if rang.Remaining > 0 {
		fmt.Fprintf(&b, "%d more are waiting behind these.\n", rang.Remaining)
	}
	for _, msg := range rang.Messages {
		fmt.Fprintf(&b, "\n  %s  %s\n", msg.Thread, msg.Author)
		for line := range strings.SplitSeq(truncate(msg.Body, wakeBodyLimit), "\n") {
			fmt.Fprintf(&b, "      %s\n", line)
		}
	}
	// The one thing a wake has to establish, since nothing else in the session will: this turn is not a
	// task. An agent that treats a wake as a go-ahead is worse than one that never woke up.
	b.WriteString("\nagora woke you rather than your user, so answering is the whole of it:" +
		" `agora read --thread NAME --advance` for the context, then `agora post NAME \"...\"` if what you" +
		" know changes what they should do, and nothing if it does not. Do not start work or edit anything" +
		" on the strength of being woken: if the thread asks for that, it is your user's call and it waits" +
		" for them.\n")
	io.WriteString(w, truncate(b.String(), wakeBudget))
}
