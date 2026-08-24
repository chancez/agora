package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/chancez/agora/internal/store"
	"github.com/spf13/cobra"
)

// hookEvent is the part of a hook event agora reads. Unknown fields are ignored, which is what
// keeps this from breaking when the harness adds one.
type hookEvent struct {
	HookEventName string `json:"hook_event_name"`
	ToolName      string `json:"tool_name"`
	CWD           string `json:"cwd"`
	// SessionID is who the event is about, which agora leave and agora doorbell resolve their identity
	// from. Every event carries it.
	SessionID string    `json:"session_id"`
	ToolInput toolInput `json:"tool_input"`
	// StopHookActive is set on a Stop event when this turn is only running because a stop hook said it
	// should. The doorbell reads it so that waking a turn cannot become a turn that never ends.
	StopHookActive bool `json:"stop_hook_active"`
}

type toolInput struct {
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
}

// path is the file the tool is about to change, empty for a tool that does not name one.
func (e hookEvent) path() string {
	if e.ToolInput.FilePath != "" {
		return e.ToolInput.FilePath
	}
	return e.ToolInput.NotebookPath
}

type hookOutput struct {
	HookSpecificOutput hookDecision `json:"hookSpecificOutput"`
}

// hookDecision is what the guard writes. The permission fields are omitted when empty, which is the difference
// between the two postures: with them the harness gates the edit, and without them it carries the text to the
// model and leaves every permission rule the user has exactly as it was.
//
// Measured: additionalContext on a PreToolUse event is delivered with no permissionDecision at all, and the run
// is not interrupted.
type hookDecision struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
	AdditionalContext        string `json:"additionalContext,omitempty"`
}

// noteLimit bounds what a claim's note contributes to a deny reason. Hook output over 10000 characters
// is spilled to a file and replaced with a preview, and a guard whose message is replaced by a preview
// has explained nothing.
const noteLimit = 600

func newGuardCmd(a *app) *cobra.Command {
	var (
		ask  bool
		deny bool
	)
	cmd := &cobra.Command{
		Use:   "guard",
		Short: "Hook entry point: tell an agent when its edit lands in somebody else's claim",
		Long: `Read a PreToolUse hook event on stdin and, if the file is covered by somebody else's
claim, say so.

The layer that works even for an agent that never read the channel. Not a lock: what it
catches is two agents about to do one thing twice, which a path list can only guess at, so
what it writes is the thread and the holder's note and the judgement is left to whoever
reads it.

By default it tells the agent and decides nothing. No prompt, no refusal, and every
permission rule you have is left alone. It tells each member about a claim once rather than
on every edit under it, and says it again when the claim changes hands or is released and
taken again.

--ask and --deny gate the edit instead, and both speak every time, since a gate that goes
quiet after the first answer would let the next edit through in silence. Measured with two
agents working one package: --ask is a prompt per edit for as long as the claim stands, and
that is what got this layer switched off. Reach for them when you would rather stop an agent
that has ignored everything else.

Two constraints. It exits 0 and says nothing when no claim matches, because a guard that
complains about unrelated edits gets removed; and it does nothing slow, since it sits in the
path of every matching edit. Both matter more than reporting its own failures, so a broken
guard says so on stderr and lets the edit through.

Wire it per-tool, filtered before the process is spawned:

    { "matcher": "Edit|Write", "if": "Edit(**/*.go)",
      "hooks": [{ "type": "command", "command": "agora guard", "timeout": 5 }] }`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if ask && deny {
				return fmt.Errorf("--ask puts the edit to your user and --deny refuses it, so pass one or neither")
			}
			decision, err := a.guard(cmd.Context(), cmd.InOrStdin(), ask, deny)
			if err != nil {
				// Never the edit's problem. A guard in the path of every matching edit that can
				// block work by failing is a guard that gets deleted, and then nothing is enforced.
				fmt.Fprintf(a.errOut, "agora guard: %v\n", err)
				return nil
			}
			if decision == nil {
				return nil
			}
			return json.NewEncoder(a.out).Encode(hookOutput{HookSpecificOutput: *decision})
		},
	}
	cmd.Flags().BoolVar(&ask, "ask", false, "gate the edit by asking your user, on every matching edit")
	cmd.Flags().BoolVar(&deny, "deny", false, "gate the edit by refusing it, on every matching edit")
	return cmd
}

// guardMode is how the guard is speaking. It changes only the last sentence of what it says, which is the
// sentence about what to do: an agent told "nothing is stopping this" and an agent whose edit was just refused
// have different moves.
type guardMode int

const (
	// modeNotice tells the agent and decides nothing, which is the default.
	modeNotice guardMode = iota
	modeAsk
	modeDeny
)

// guard answers an event, returning nil for "say nothing".
func (a *app) guard(ctx context.Context, stdin io.Reader, ask, deny bool) (*hookDecision, error) {
	var event hookEvent
	// A terminal on stdin means nobody is going to hand this an event, so it says so rather than waiting for
	// one. Same reasoning as inject.
	if isTerminal(stdin) {
		return nil, fmt.Errorf("nothing on stdin: this reads a PreToolUse hook event, so a harness runs it")
	}
	if err := json.NewDecoder(stdin).Decode(&event); err != nil {
		return nil, fmt.Errorf("read the hook event: %w", err)
	}
	if event.HookEventName != "" && event.HookEventName != "PreToolUse" {
		return nil, nil
	}
	path := event.path()
	if path == "" {
		// A tool that names no file, such as Bash. There is nothing to compare a glob against, and
		// guessing at a command line is exactly the kind of false positive that gets this removed.
		return nil, nil
	}

	if event.CWD != "" {
		a.resolver.Dir = event.CWD
	}
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	// No database means nothing is claimed. Checked rather than opened, because a guard must not be
	// the thing that creates a database on somebody's edit path.
	exists, err := fileExists(cfg.Database.Value)
	if err != nil || !exists {
		return nil, err
	}
	s, _, err := a.open()
	if err != nil {
		return nil, err
	}
	claims, err := s.Claims(ctx, cfg.Channel.Value)
	if err != nil {
		return nil, err
	}

	held := matchingClaims(claims, cfg.Member.Value, path, event.CWD, cfg.Worktree.Value)
	if len(held) == 0 {
		return nil, nil
	}
	mode := modeNotice
	switch {
	case ask:
		mode = modeAsk
	case deny:
		mode = modeDeny
	}
	if mode == modeNotice {
		// Only what this member has not heard yet. A gate speaks every time because it is being answered every
		// time; a notice repeated on every edit under one claim is the noise this layer was turned off for.
		notice, err := s.Notice(ctx, store.NoticeRequest{
			Channel: cfg.Channel.Value,
			Member:  cfg.Member.Value,
			Claims:  held,
		})
		if err != nil {
			return nil, err
		}
		if len(notice.New) == 0 {
			return nil, nil
		}
		return &hookDecision{
			HookEventName:     "PreToolUse",
			AdditionalContext: overlapReason(path, notice.New, mode),
		}, nil
	}
	decision := "deny"
	if mode == modeAsk {
		decision = "ask"
	}
	return &hookDecision{
		HookEventName:            "PreToolUse",
		PermissionDecision:       decision,
		PermissionDecisionReason: overlapReason(path, held, mode),
	}, nil
}

// matchingClaims returns the claims covering path that somebody else holds.
//
// A claim with no paths never matches: prose is unreadable to a guard, and guessing would stop real work.
//
// Globs match the path relative to the *worktree*, not the channel. The channel is the main checkout, so a file
// in a linked worktree is `.worktrees/parser/parser.go` relative to it and a claim on `parser.go` would match
// nothing anyone edits. The channel key can also be an arbitrary string, via --channel.
func matchingClaims(claims []store.Claim, member, path, cwd, worktree string) []store.Claim {
	absolute := path
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(cwd, absolute)
	}
	candidates := []string{absolute}
	if relative, err := filepath.Rel(worktree, absolute); err == nil && !strings.HasPrefix(relative, "..") {
		candidates = append(candidates, relative)
	}

	var held []store.Claim
	for _, claim := range claims {
		if claim.Holder == member {
			continue
		}
		if matchesAny(claim.Paths, candidates) {
			held = append(held, claim)
		}
	}
	return held
}

func matchesAny(patterns, candidates []string) bool {
	for _, pattern := range patterns {
		for _, candidate := range candidates {
			if ok, err := doublestar.Match(pattern, candidate); err == nil && ok {
				return true
			}
		}
	}
	return false
}

// overlapReason is read by the agent, and by the user when the edit is being gated. It has to say who, what
// work, and what to do next: a stop that only says no leaves an agent with the same task and no move, and a
// notice that only says somebody is nearby leaves it with nothing to decide.
//
// The question is never "is this file taken", which is ordinary and git's problem. It is whether these are two
// goes at one piece of work, which the holder's note answers and a path list only hints at.
func overlapReason(path string, held []store.Claim, mode guardMode) string {
	var b strings.Builder
	for i, claim := range held {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "agora: %s is working on %s, which covers %s (claimed %s ago).",
			claim.Holder, claim.Thread, filepath.Base(path), time.Since(claim.CreatedAt).Round(time.Minute))
		if claim.Note != "" {
			fmt.Fprintf(&b, "\nTheir note: %s", truncate(claim.Note, noteLimit))
		}
		fmt.Fprintf(&b, "\nRun `agora read` for the context, then say which this is with"+
			" `agora post --thread %s`: part of their work, or a different change that happens to touch"+
			" the same file.", claim.Thread)
		switch mode {
		case modeAsk:
			b.WriteString(" Your user can approve this edit either way, and they are the one who gets" +
				" to decide it.")
		case modeDeny:
			fmt.Fprintf(&b, " If it is theirs, do not write a second fix; `agora claim %s` succeeds"+
				" once they have released it.", claim.Thread)
		default:
			fmt.Fprintf(&b, " Nothing is stopping this edit: if it is a second go at their work, tell your"+
				" user who holds it rather than writing a second fix, and `agora claim %s` succeeds once they"+
				" have released it.", claim.Thread)
		}
	}
	return b.String()
}

// isTerminal reports whether a reader is a terminal, which is how a command that reads a hook event on stdin
// tells "nobody is going to write one" from "one is on its way". A character device is the test: a hook's
// stdin is a pipe, and a person's is a tty.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}
