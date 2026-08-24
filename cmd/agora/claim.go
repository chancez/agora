package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

func newClaimCmd(a *app) *cobra.Command {
	var (
		note  string
		paths []string
	)
	cmd := &cobra.Command{
		Use:   "claim <thread>",
		Short: "Take ownership of a thread, or find out who has it",
		Long: `Claim a thread before starting work someone else might touch.

    agora claim parser-panic --note "root cause is in the token loop, not the caller" \
        --paths 'parser.go,internal/lex/**'

Losing a claim is information, not an obstacle to route around: the output names the holder
and their note, and that note is prose worth arguing with. Do not claim what you will not do.

--paths is the only part of a claim anything can enforce. A claim carrying only prose is
still a claim, but agora guard cannot act on it, since guessing at prose is worse than
admitting it cannot read it.

Re-claiming what you hold updates the note and keeps the age. Exit status is 1 when the claim
was lost, after printing who holds it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			result, err := s.Claim(cmd.Context(), store.ClaimRequest{
				Channel:  cfg.Channel.Value,
				Thread:   args[0],
				Holder:   cfg.Member.Value,
				Note:     note,
				Paths:    paths,
				Worktree: &cfg.Worktree.Value,
			})
			if err != nil {
				return err
			}
			if err := a.print(result, func(w io.Writer) {
				if result.Granted {
					fmt.Fprintf(w, "claimed %s as %s\n", result.Claim.Thread, result.Claim.Holder)
					return
				}
				writeHeldClaim(w, result.Claim)
			}); err != nil {
				return err
			}
			if !result.Granted {
				// The holder has already been printed, which is the part that decides whether work
				// gets duplicated. The status is so a script notices.
				return exitCode(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "what you are doing, and what you would rather nobody else did")
	cmd.Flags().StringSliceVar(&paths, "paths", nil, "globs this claim covers, which agora guard can enforce")
	return cmd
}

// writeHeldClaim is the human form of a lost claim. It names the holder, the note, and the age,
// because "someone else has it" without those is not enough to decide what to do instead.
func writeHeldClaim(w io.Writer, claim store.Claim) {
	fmt.Fprintf(w, "%s holds %s, claimed %s ago\n", claim.Holder, claim.Thread,
		time.Since(claim.CreatedAt).Round(time.Second))
	if claim.Note != "" {
		fmt.Fprintf(w, "  note: %s\n", claim.Note)
	}
	if len(claim.Paths) > 0 {
		fmt.Fprintf(w, "  paths: %s\n", strings.Join(claim.Paths, ", "))
	}
	fmt.Fprintf(w, "run `agora read` for the context, or post why the scope is wrong\n")
}
