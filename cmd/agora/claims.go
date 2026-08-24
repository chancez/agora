package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

func newClaimsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "claims",
		Short: "Show who owns what in this channel",
		Long: `List the claims standing in this channel, oldest thread first.

Read this before starting work that others might be doing, which is cheaper than
finding out from a lost claim or a denied edit. A claim's note says what its holder
is doing and often what they would rather nobody else did separately.

Paths are what agora guard can act on. A claim listed with none still coordinates
with agents that read the channel.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			claims, err := s.Claims(cmd.Context(), cfg.Channel.Value)
			if err != nil {
				return err
			}
			// [] rather than null, so a caller can iterate the result without checking which it got.
			if claims == nil {
				claims = []store.Claim{}
			}
			return a.print(claims, func(w io.Writer) { writeClaims(w, cfg.Channel.Value, claims) })
		},
	}
	return cmd
}

func writeClaims(w io.Writer, channel string, claims []store.Claim) {
	if len(claims) == 0 {
		fmt.Fprintf(w, "nothing is claimed in %s\n", channel)
		return
	}
	table := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(table, "TOPIC\tHOLDER\tHELD FOR\tPATHS")
	for _, claim := range claims {
		paths := strings.Join(claim.Paths, ", ")
		if paths == "" {
			paths = "-"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", claim.Thread, claim.Holder,
			time.Since(claim.CreatedAt).Round(time.Second), paths)
	}
	table.Flush()
	for _, claim := range claims {
		if claim.Note != "" {
			fmt.Fprintf(w, "\n%s: %s\n", claim.Thread, claim.Note)
		}
	}
}
