package main

import (
	"fmt"
	"io"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

func newReleaseCmd(a *app) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "release <thread>",
		Short: "Give up a claim you hold",
		Long: `Release a claim when the work is done, or when you are not going to do it.

Releasing someone else's claim needs --force, and needing it should give you pause: a
claim held by a member that looks idle may belong to an agent waiting on its user.
Read the channel first, and post before you take it.

Exit status is 1 when nothing was released, after printing who holds the claim.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			result, err := s.Release(cmd.Context(), store.ReleaseRequest{
				Channel:  cfg.Channel.Value,
				Thread:   args[0],
				Holder:   cfg.Member.Value,
				Force:    force,
				Worktree: &cfg.Worktree.Value,
			})
			if err != nil {
				return err
			}
			if err := a.print(result, func(w io.Writer) {
				switch {
				case result.Released:
					fmt.Fprintf(w, "released %s\n", result.Claim.Thread)
				case result.Claim.Holder == "":
					fmt.Fprintf(w, "nobody holds %s in %s\n", args[0], cfg.Channel.Value)
				default:
					fmt.Fprintf(w, "not yours to release: ")
					writeHeldClaim(w, result.Claim)
				}
			}); err != nil {
				return err
			}
			if !result.Released {
				return exitCode(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "release a claim held by someone else")
	return cmd
}
