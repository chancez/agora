package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

// memberReport is a member plus whether they still seem to be here. Unread comes from the store, which
// is the only place that gets to decide what unread means.
type memberReport struct {
	store.Member
	Stale bool `json:"stale"`
}

func newMembersCmd(a *app) *cobra.Command {
	var (
		staleOnly  bool
		staleAfter time.Duration
	)
	cmd := &cobra.Command{
		Use:   "members",
		Short: "Show who is in the channel and how far behind they are",
		Long: `Show the channel roster.

"Unread" counts messages from other members that this one has not read, and it is the
number worth looking at: a member far behind has either stopped reading or stopped
running, and their claims stand either way.

--stale narrows to members not heard from recently. A heartbeat moves whenever a
member posts, reads, or claims, so this is a hint rather than a fact: an agent waiting
on its user is idle and very much alive. Never release a claim on the strength of it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			members, err := s.Members(cmd.Context(), cfg.Channel.Value)
			if err != nil {
				return err
			}

			cutoff := time.Now().Add(-staleAfter)
			reports := make([]memberReport, 0, len(members))
			for _, member := range members {
				report := memberReport{Member: member, Stale: member.SeenAt.Before(cutoff)}
				if staleOnly && !report.Stale {
					continue
				}
				reports = append(reports, report)
			}
			return a.print(reports, func(w io.Writer) { writeMembers(w, cfg.Channel.Value, reports) })
		},
	}
	cmd.Flags().BoolVar(&staleOnly, "stale", false, "only members not heard from recently")
	cmd.Flags().DurationVar(&staleAfter, "stale-after", 30*time.Minute, "how long counts as not heard from")
	return cmd
}

func writeMembers(w io.Writer, channel string, reports []memberReport) {
	if len(reports) == 0 {
		fmt.Fprintf(w, "no members in %s\n", channel)
		return
	}
	table := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	// The description sits next to the name, since the question it answers is which of these names to ask.
	fmt.Fprintln(table, "MEMBER\tDOING\tUNREAD\tLAST SEEN\tWORKTREE")
	for _, report := range reports {
		seen := time.Since(report.SeenAt).Round(time.Second).String() + " ago"
		if report.Stale {
			seen += " (stale)"
		}
		doing := report.Description
		if doing == "" {
			doing = "-"
		}
		fmt.Fprintf(table, "%s\t%s\t%d\t%s\t%s\n",
			report.Name, doing, report.Unread, seen, report.Worktree)
	}
	table.Flush()
}
