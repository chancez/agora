package main

import (
	"fmt"
	"io"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

func newDeleteCmd(a *app) *cobra.Command {
	var (
		yes   bool
		force bool
	)
	cmd := &cobra.Command{
		Use:   "delete <thread>",
		Short: "Remove a thread and everything in it",
		Long: `Delete a thread: its messages, its claim, every cursor on it.

The only command that shortens the record, for when a record is wrong: a mistaken finding
misleads whoever reads it next. Without --yes it reports what would go and changes nothing,
so the destructive form is always the second thing you type:

    agora delete stray-thread          # 3 messages, claimed by alice
    agora delete stray-thread --yes

Somebody else's thread needs --force. A release they can see and argue with; a deletion leaves
them nothing to argue about.

Exit status is 1 whenever nothing was deleted, the preview included, so a script cannot read
"here is what I would do" as "done".`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, cfg, err := a.open()
			if err != nil {
				return err
			}
			result, err := s.Delete(cmd.Context(), store.DeleteRequest{
				Channel:  cfg.Channel.Value,
				Thread:   args[0],
				Member:   cfg.Member.Value,
				Force:    force,
				DryRun:   !yes,
				Worktree: &cfg.Worktree.Value,
			})
			if err != nil {
				return err
			}
			if err := a.print(result, func(w io.Writer) { writeDeletion(w, result, yes, cfg.Member.Value) }); err != nil {
				return err
			}
			if !result.Deleted {
				return exitCode(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "actually delete it, rather than reporting what would go")
	cmd.Flags().BoolVar(&force, "force", false, "delete a thread somebody else has claimed")
	return cmd
}

func writeDeletion(w io.Writer, result store.DeleteResult, asked bool, member string) {
	held := result.Claim != "" && result.Claim != member
	switch {
	case result.Deleted:
		fmt.Fprintf(w, "deleted %s: %s, %s\n", result.Thread,
			plural(int(result.Messages), "message"), plural(int(result.Cursors), "cursor"))
	case asked && held:
		fmt.Fprintf(w, "%s is claimed by %s, so it was left alone. Read it first, or pass --force.\n",
			result.Thread, result.Claim)
	case result.Messages == 0 && result.Claim == "":
		fmt.Fprintf(w, "%s has nothing in it\n", result.Thread)
	default:
		// The preview. It names the claim even when it is yours, since that is part of what would go.
		fmt.Fprintf(w, "%s would delete %s and %s\n", result.Thread,
			plural(int(result.Messages), "message"), plural(int(result.Cursors), "cursor"))
		if result.Claim != "" {
			fmt.Fprintf(w, "  and the claim held by %s\n", result.Claim)
		}
		fmt.Fprintf(w, "pass --yes to do it\n")
	}
}
