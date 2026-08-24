package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

func newLeaveCmd(a *app) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "leave",
		Short: "Take yourself out of a channel's roster",
		Long: `Take a name off a channel's roster.

Identity is the session, so a session that ended stays listed forever otherwise: its unread
count reads as an agent falling behind and its claim as work in progress.

    agora leave                        # you, at the end of a session
    agora leave --as claude-9ddf2b73   # a name a previous session left behind
    agora leave --force                # and hand back whatever it was holding

It leaves the record alone, and what the member had read: the same name coming back picks up
where it left off. Holding a claim is refused without --force, since a claim whose holder is
not in the roster leaves nobody to ask whether the work was finished.

Run it from a SessionEnd hook rather than remembering to. It takes its identity from the event
on stdin when there is one, which is the only thing that names the session ending.

Exit status is 1 only when a claim stopped it. A member that was not there is nothing to do,
which is the common case for a session that never touched the channel.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// A hook event when there is one, so the identity is the session the event is about. A terminal
			// on stdin means nobody is going to write one, and this is also a command people run by hand.
			if stdin := cmd.InOrStdin(); !isTerminal(stdin) {
				var event hookEvent
				// A body that is not an event is not fatal: identity falls back to the usual chain rather
				// than the command refusing to clean up.
				if err := json.NewDecoder(stdin).Decode(&event); err == nil {
					a.flags.SessionID = event.SessionID
					if event.CWD != "" {
						a.resolver.Dir = event.CWD
					}
				}
			}
			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			result, err := s.Leave(cmd.Context(), store.LeaveRequest{
				Channel: cfg.Channel.Value,
				Member:  cfg.Member.Value,
				Force:   force,
			})
			if err != nil {
				return err
			}
			if err := a.print(result, func(w io.Writer) { writeDeparture(w, result) }); err != nil {
				return err
			}
			if !result.Left && len(result.Claims) > 0 {
				return exitCode(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "give up the claims this member holds")
	return cmd
}

func writeDeparture(w io.Writer, result store.LeaveResult) {
	switch {
	case result.Left && len(result.Claims) > 0:
		fmt.Fprintf(w, "%s left %s, releasing %s\n", result.Member, result.Channel,
			strings.Join(result.Claims, ", "))
	case result.Left:
		fmt.Fprintf(w, "%s left %s, keeping %s\n", result.Member, result.Channel,
			plural(int(result.Cursors), "read thread"))
	case len(result.Claims) > 0:
		fmt.Fprintf(w, "%s still holds %s, so nothing was removed. Release it, or pass --force.\n",
			result.Member, strings.Join(result.Claims, ", "))
	default:
		fmt.Fprintf(w, "%s was not in %s\n", result.Member, result.Channel)
	}
}
