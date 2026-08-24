package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

func newPruneCmd(a *app) *cobra.Command {
	var (
		staleAfter time.Duration
		dryRun     bool
	)
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Take members that have not been heard from off the roster",
		Long: `Remove every member last heard from longer ago than --stale-after.

The sweep for sessions that ended without saying so. SessionEnd only fires on a clean
end, so a terminal that was killed, or a session that ran before the hook was wired,
leaves a row behind that reads as somebody falling behind.

    agora members --stale   # what a sweep would consider gone
    agora prune --dry-run   # what it would take
    agora prune             # take it

Two members it never takes. One holding a claim is reported instead, since releasing
somebody's claim is the one part of a wrong removal that cannot be taken back:
` + "`agora leave --as NAME --force`" + ` is how you do that on purpose, one name at a time.
And whoever is running it, because running a sweep is not evidence of being gone.

What moves a member's clock is agora activity, not being alive. Measured: a session
worked for five hours after its last agora command, so quiet and gone look the same
from here. That is why this is a command somebody runs and not something a hook does,
and why what it costs is small: cursors survive, so a member pruned by mistake comes
back on its next action having lost only the line about what it was doing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			result, err := s.Prune(cmd.Context(), store.PruneRequest{
				Channel:  cfg.Channel.Value,
				Member:   cfg.Member.Value,
				Before:   time.Now().Add(-staleAfter),
				DryRun:   dryRun,
				Worktree: &cfg.Worktree.Value,
			})
			if err != nil {
				return err
			}
			return a.print(result, func(w io.Writer) {
				verb := "removed"
				if dryRun {
					verb = "would remove"
				}
				if len(result.Pruned) == 0 {
					fmt.Fprintf(w, "nothing to remove from %s\n", result.Channel)
				} else {
					fmt.Fprintf(w, "%s %s\n", verb, strings.Join(result.Pruned, ", "))
				}
				// Named rather than counted: the point of leaving them is that somebody reads who they are.
				for name, threads := range result.Held {
					fmt.Fprintf(w, "kept %s, holding %s\n", name, strings.Join(threads, ", "))
				}
			})
		},
	}
	cmd.Flags().DurationVar(&staleAfter, "stale-after", defaultStaleAfter, "how long counts as not heard from")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would go and change nothing")
	return cmd
}
