package main

import (
	"fmt"
	"io"

	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

func newDeleteChannelCmd(a *app) *cobra.Command {
	var (
		yes   bool
		force bool
	)
	cmd := &cobra.Command{
		Use:   "delete-channel <key>",
		Short: "Remove a channel and everything in it",
		Long: `Delete a channel: every thread, message, member, cursor, and claim in it.

Nothing else takes a channel out of the database, and every command that names one creates
it, so running agora once in a repository leaves a channel there. Cleaning those up is what
this is for, and agora channels is how you find them:

    agora channels
    agora delete-channel /path/to/some/repo         # reports what would go
    agora delete-channel /path/to/some/repo --yes

The key is named rather than taken from the directory you are in, because the channel worth
deleting is rarely the one you are working in.

A channel with messages or claims in it is a repository's whole record and somebody's
ownership of work, so it needs --force as well as --yes. An unused one needs only --yes.

Exit status is 1 whenever nothing was deleted, the preview included, so a script cannot read
"here is what I would do" as "done".`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := a.open()
			if err != nil {
				return err
			}
			result, err := s.DeleteChannel(cmd.Context(), store.DeleteChannelRequest{
				Channel: args[0],
				Force:   force,
				DryRun:  !yes,
			})
			if err != nil {
				return err
			}
			if err := a.print(result, func(w io.Writer) { writeChannelDeletion(w, result, yes) }); err != nil {
				return err
			}
			if !result.Deleted {
				return exitCode(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "actually delete it, rather than reporting what would go")
	cmd.Flags().BoolVar(&force, "force", false, "delete a channel that has messages or claims in it")
	return cmd
}

func writeChannelDeletion(w io.Writer, result store.DeleteChannelResult, asked bool) {
	// What --force is about. A member row is not use: reading a channel is what puts you in its roster, so
	// the channel this command exists for has one member and nothing else.
	inUse := result.Messages > 0 || len(result.Claims) > 0
	counts := fmt.Sprintf("%s, %s, %s", plural(int(result.Threads), "thread"),
		plural(int(result.Messages), "message"), plural(int(result.Members), "member"))
	switch {
	case !result.Existed:
		// Not an error: a key nothing ever used has nothing to delete, and saying so beats a refusal that
		// reads as though something stopped it.
		fmt.Fprintf(w, "no channel %s in this database\n", result.Channel)
	case result.Deleted && !inUse:
		fmt.Fprintf(w, "deleted %s, which had nothing in it\n", result.Channel)
	case result.Deleted:
		fmt.Fprintf(w, "deleted %s: %s\n", result.Channel, counts)
	case asked:
		// --yes was given and it was refused, so the reason is the whole message.
		fmt.Fprintf(w, "%s has %s in it, so it was left alone. Read it first, or pass --force.\n",
			result.Channel, counts)
		writeChannelClaims(w, result)
	default:
		fmt.Fprintf(w, "%s would delete %s\n", result.Channel, counts)
		writeChannelClaims(w, result)
		if inUse {
			fmt.Fprintf(w, "pass --yes --force to do it\n")
			return
		}
		fmt.Fprintf(w, "pass --yes to do it\n")
	}
}

// writeChannelClaims names the holders and their notes, because a claim is somebody working right now and that
// note is what says whether deleting this is a decision or a mistake.
func writeChannelClaims(w io.Writer, result store.DeleteChannelResult) {
	for _, claim := range result.Claims {
		line := fmt.Sprintf("  %s is claimed by %s", claim.Thread, claim.Holder)
		if claim.Note != "" {
			line += ": " + claim.Note
		}
		fmt.Fprintln(w, line)
	}
}
