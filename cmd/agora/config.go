package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/chancez/agora/internal/config"
	"github.com/spf13/cobra"
)

// configReport is what agora config prints: every resolved value, where it came from, and whether the database
// exists. The provenance is the point: a stray post against the real channel is a message another agent acts
// on, so isolation has to be checkable rather than hoped for.
type configReport struct {
	Database config.Value `json:"database"`
	// DatabaseExists distinguishes a database from a path where one would go. A test pointed at a
	// throwaway path should report false before its first write.
	DatabaseExists bool         `json:"database_exists"`
	Channel        config.Value `json:"channel"`
	Member         config.Value `json:"member"`
	Worktree       config.Value `json:"worktree"`
	// Layout is the file the TUI keeps its pane widths in, reported here because it is the other path agora
	// writes to and the only one a person is meant to open.
	Layout config.Value `json:"layout"`
}

func newConfigCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Report the resolved database, channel, and identity, and where each came from",
		Long: `Report what this invocation would use, and why.

Run this before testing anything against agora. A test that writes to the real
database posts to a channel real agents act on, so point it somewhere else first:

    export AGORA_DB=$(mktemp -d /tmp/agoradev.XXXX)/agora.db
    export AGORA_CHANNEL=devtest
    agora config

An empty variable is rejected rather than ignored, because AGORA_DB= reads as unset
and would fall through to the real database while looking configured.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			exists, err := fileExists(cfg.Database.Value)
			if err != nil {
				return err
			}
			// Deliberately does not open the database. Reporting where a command would write must
			// not be the thing that creates it.
			report := configReport{
				Database:       cfg.Database,
				DatabaseExists: exists,
				Channel:        cfg.Channel,
				Member:         cfg.Member,
				Worktree:       cfg.Worktree,
				Layout:         cfg.Layout,
			}
			return a.print(report, func(w io.Writer) {
				state := "does not exist yet"
				if exists {
					state = "exists"
				}
				fmt.Fprintf(w, "database  %s  (%s, %s)\n", report.Database.Value, report.Database.Source, state)
				fmt.Fprintf(w, "channel   %s  (%s)\n", report.Channel.Value, report.Channel.Source)
				fmt.Fprintf(w, "member    %s  (%s)\n", report.Member.Value, report.Member.Source)
				fmt.Fprintf(w, "worktree  %s  (%s)\n", report.Worktree.Value, report.Worktree.Source)
				fmt.Fprintf(w, "layout    %s  (%s)\n", report.Layout.Value, report.Layout.Source)
			})
		},
	}
}

func fileExists(path string) (bool, error) {
	switch _, err := os.Stat(path); {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
}
