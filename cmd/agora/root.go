// Command agora is a shared, durable channel where independent coding agents coordinate.
//
// This package is deliberately thin: parse flags, call internal/store, print JSON. No SQL lives here,
// which is what keeps putting a server behind internal/store a refactor rather than a rewrite.
package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/chancez/agora/internal/config"
	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

// exitCode ends the process with a status and nothing else. A lost claim has already printed the
// holder and their note, which is the output that stops duplicate work, and cobra's "Error:" line on
// top of that JSON would be noise.
type exitCode int

func (c exitCode) Error() string { return fmt.Sprintf("exit status %d", int(c)) }

// app is the state every command shares.
type app struct {
	flags config.Flags
	text  bool

	in     io.Reader
	out    io.Writer
	errOut io.Writer

	// resolver is overridden by tests, which must never read the developer's real environment.
	resolver config.Resolver

	cfg      config.Config
	resolved bool
	store    *store.Store
}

func newRootCmd(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "agora",
		Short: "A shared channel where independent coding agents coordinate",
		Long: `agora is a shared, durable channel where independent coding agents coordinate: post
findings, read what they missed, and claim ownership of work so two of them do not
fix the same bug twice.

Agents are the primary caller, so every command reads and writes JSON. --text is
for humans.

Exit status is 1 for an error, and also for a claim that was lost or a release that
was refused: both print who holds the claim before exiting.`,
		// The output is JSON. A usage dump appended to it on every error would make it unparseable,
		// and the error itself goes to stderr where a caller can find it.
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Cobra's own messages follow the same streams as the command output, so a test sees everything
	// a caller would.
	root.SetOut(a.out)
	root.SetErr(a.errOut)
	if a.in != nil {
		root.SetIn(a.in)
	}

	pf := root.PersistentFlags()
	pf.StringVar(&a.flags.Database, "db", "",
		"sqlite database (default $AGORA_DB, else $XDG_DATA_HOME/agora/agora.db)")
	pf.StringVar(&a.flags.Channel, "channel", "",
		"channel key (default $AGORA_CHANNEL, else the repository this directory belongs to)")
	pf.StringVar(&a.flags.Member, "as", "",
		"who is asking (default $AGORA_MEMBER, else the agent's session, else $USER)")
	pf.BoolVar(&a.text, "text", false, "print for humans instead of JSON")

	root.AddCommand(
		newConfigCmd(a),
		newJoinCmd(a),
		newLeaveCmd(a),
		newPostCmd(a),
		newThreadsCmd(a),
		newReadCmd(a),
		newAckCmd(a),
		newMuteCmd(a, false),
		newMuteCmd(a, true),
		newDeleteCmd(a),
		newChannelsCmd(a),
		newDeleteChannelCmd(a),
		newWatchCmd(a),
		newClaimCmd(a),
		newClaimsCmd(a),
		newReleaseCmd(a),
		newMembersCmd(a),
		newDumpCmd(a),
		newGuardCmd(a),
		newInjectCmd(a),
		newDoorbellCmd(a),
	)
	return root
}

// config resolves the database, channel, and identity once per invocation.
func (a *app) config() (config.Config, error) {
	if a.resolved {
		return a.cfg, nil
	}
	cfg, err := a.resolver.Resolve(a.flags)
	if err != nil {
		return config.Config{}, err
	}
	a.cfg, a.resolved = cfg, true
	return a.cfg, nil
}

// open resolves configuration and opens the database.
func (a *app) open() (*store.Store, config.Config, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, config.Config{}, err
	}
	if a.store == nil {
		if a.store, err = store.Open(cfg.Database.Value); err != nil {
			return nil, config.Config{}, err
		}
	}
	return a.store, cfg, nil
}

func (a *app) close() {
	if a.store != nil {
		a.store.Close()
		a.store = nil
	}
}

// print writes the result: JSON unless --text was given, in which case text does the writing.
//
// JSON is indented because it is read as often as it is parsed, and jq does not care. The exception is
// watch, which writes one compact object per line so a stream can be consumed a line at a time.
func (a *app) print(value any, text func(w io.Writer)) error {
	if a.text {
		text(a.out)
		return nil
	}
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}
