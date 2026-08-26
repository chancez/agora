package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chancez/agora/internal/config"
	"github.com/google/go-cmp/cmp"
)

// hookEventJSON is what the harness sends a PreToolUse hook.
func hookEventJSON(t *testing.T, event string, cwd, tool, path string) string {
	t.Helper()
	return hookEventAsJSON(t, event, cwd, tool, path, "test-session")
}

// hookEventAsJSON is the same event for a named session, for the tests about whose edit it is.
func hookEventAsJSON(t *testing.T, event, cwd, tool, path, session string) string {
	t.Helper()
	input := map[string]string{}
	if tool == "NotebookEdit" {
		input["notebook_path"] = path
	} else if path != "" {
		input["file_path"] = path
	}
	return eventJSON(t, map[string]any{
		"hook_event_name": event,
		"session_id":      session,
		"cwd":             cwd,
		"tool_name":       tool,
		"tool_input":      input,
	})
}

// codexEventJSON is a PreToolUse event as Codex sends one. Codex has no Edit or Write tool: an edit is one
// apply_patch call whose tool_input is the patch text, and the event names no file anywhere else.
func codexEventJSON(t *testing.T, cwd, tool, command string) string {
	t.Helper()
	return eventJSON(t, map[string]any{
		"hook_event_name": "PreToolUse",
		"session_id":      "test-session",
		"cwd":             cwd,
		"tool_name":       tool,
		"tool_input":      map[string]string{"command": command},
	})
}

func eventJSON(t *testing.T, event map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal the hook event: %v", err)
	}
	return string(raw)
}

// said is whatever the guard put in front of the model, whichever posture it was in. The text is the same
// either way: who holds the work, their note, and what to do about it.
func said(d hookDecision) string {
	if d.AdditionalContext != "" {
		return d.AdditionalContext
	}
	return d.PermissionDecisionReason
}

// TestGuardTellsTheAgentAndDecidesNothing is the default, and the whole redesign. Gating measured worse than
// informing: with two agents in one package, --ask was a prompt per edit for as long as the claim stood, and it
// got the layer switched off, which is the failure the design record predicted for it.
//
// Measured: additionalContext on a PreToolUse event reaches the model with no permissionDecision at all, the run
// is not interrupted, and every permission rule the user has is left alone.
func TestGuardTellsTheAgentAndDecidesNothing(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("claim", "parser-panic",
		"--note", "root cause is in the token loop, do not patch the symptom",
		"--paths", "parser.go")

	event := hookEventJSON(t, "PreToolUse", bob.dir, "Edit", filepath.Join(bob.dir, "parser.go"))
	got := bob.stdin(event).run("guard")
	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", got.code, got.stderr)
	}
	decision := decode[hookOutput](t, got).HookSpecificOutput
	if decision.PermissionDecision != "" {
		t.Errorf("permissionDecision = %q, want none: a notice decides nothing", decision.PermissionDecision)
	}
	// Absent from the JSON rather than empty in it, because an empty decision is still a decision the harness
	// has to interpret.
	if strings.Contains(got.stdout, "permissionDecision") {
		t.Errorf("the notice carries a permission field:\n%s", got.stdout)
	}
	for _, want := range []string{
		"alice", "parser-panic",
		"root cause is in the token loop",
		"agora read", "agora post",
		// It has to say that nothing is stopping the edit, or an agent reads a notice as a refusal.
		"Nothing is stopping this edit",
	} {
		if !strings.Contains(decision.AdditionalContext, want) {
			t.Errorf("the notice does not mention %q:\n%s", want, decision.AdditionalContext)
		}
	}
}

// TestGuardTellsEachMemberOncePerClaim is what a notice needs and a decision did not: something to remember. The
// same three lines on every edit under one claim is the noise this layer was turned off for.
func TestGuardTellsEachMemberOncePerClaim(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("claim", "parser-panic", "--note", "the token loop", "--paths", "parser.go")
	edit := hookEventJSON(t, "PreToolUse", bob.dir, "Edit", filepath.Join(bob.dir, "parser.go"))

	if first := bob.stdin(edit).run("guard"); first.stdout == "" {
		t.Fatalf("the first edit got no notice\nstderr: %s", first.stderr)
	}
	if again := bob.stdin(edit).run("guard"); again.code != 0 || again.stdout != "" {
		t.Errorf("exit %d, stdout %q, want silence the second time under the same claim", again.code, again.stdout)
	}
	// Another member has heard nothing yet.
	if carol := alice.as("carol").stdin(edit).run("guard"); carol.stdout == "" {
		t.Error("a member who has not been told got no notice")
	}

	// A claim released and taken again is a different piece of work, so it is worth saying once more. Same
	// holder, so this is the case a record keyed on the holder alone would miss.
	alice.mustRun("release", "parser-panic")
	alice.mustRun("claim", "parser-panic", "--note", "second go, the lexer this time", "--paths", "parser.go")
	retaken := bob.stdin(edit).run("guard")
	if retaken.stdout == "" {
		t.Error("a claim taken again said nothing")
	} else if !strings.Contains(said(decode[hookOutput](t, retaken).HookSpecificOutput), "second go") {
		t.Error("the second notice carried the first claim's note")
	}

	// And a different holder on the same thread.
	alice.mustRun("release", "parser-panic")
	alice.as("dave").mustRun("claim", "parser-panic", "--note", "mine now", "--paths", "parser.go")
	if handed := bob.stdin(edit).run("guard"); handed.stdout == "" {
		t.Error("a claim that changed hands said nothing")
	}
}

func TestGuardDeniesAnEditToAClaimedPath(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("claim", "parser-panic",
		"--note", "root cause is in the token loop, do not patch the symptom",
		"--paths", "parser.go,internal/lex/**")

	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "an absolute path", path: filepath.Join(bob.dir, "parser.go")},
		{name: "a path relative to the working directory", path: "parser.go"},
		{name: "a glob matching at depth", path: filepath.Join(bob.dir, "internal", "lex", "scan.go")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := hookEventJSON(t, "PreToolUse", bob.dir, "Edit", tc.path)
			got := bob.stdin(event).run("guard", "--deny")
			if got.code != 0 {
				t.Fatalf("exit code = %d, want 0: a guard signals by its output, never by failing\nstderr: %s", got.code, got.stderr)
			}
			decision := decode[hookOutput](t, got).HookSpecificOutput
			if decision.HookEventName != "PreToolUse" || decision.PermissionDecision != "deny" {
				t.Errorf("decision = %+v, want a PreToolUse deny", decision)
			}
			// The reason is shown to the agent, so it has to say who, what, and what to do instead.
			// A denial that only says no leaves an agent with the same task and no move, which is how
			// one ends up editing the channel by hand.
			for _, want := range []string{
				"alice", "parser-panic",
				"root cause is in the token loop",
				"agora read", "agora post", "agora claim parser-panic",
			} {
				if !strings.Contains(decision.PermissionDecisionReason, want) {
					t.Errorf("reason does not mention %q:\n%s", want, decision.PermissionDecisionReason)
				}
			}
		})
	}
}

// TestGuardCanAskInsteadOfRefusing is the milder of the two gates, for somebody who wants one: an overlap in
// files is not proof of an overlap in work, and a person can tell the difference in a second where a path list
// cannot tell it at all. It is not the default, because it asks on every matching edit.
func TestGuardCanAskInsteadOfRefusing(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("claim", "parser-panic", "--note", "fixing all three call sites together", "--paths", "parser.go")

	event := hookEventJSON(t, "PreToolUse", bob.dir, "Edit", filepath.Join(bob.dir, "parser.go"))
	decision := decode[hookOutput](t, bob.stdin(event).run("guard", "--ask")).HookSpecificOutput
	if decision.PermissionDecision != "ask" {
		t.Errorf("permissionDecision = %q, want ask", decision.PermissionDecision)
	}
	// The reason is read by the user here, not only the agent, so it has to name the decision as
	// theirs to make.
	for _, want := range []string{"alice", "parser-panic", "fixing all three call sites", "your user", "decide"} {
		if !strings.Contains(strings.ToLower(decision.PermissionDecisionReason), strings.ToLower(want)) {
			t.Errorf("ask reason does not mention %q:\n%s", want, decision.PermissionDecisionReason)
		}
	}
	// A gate asks every time. One that went quiet after the first answer would let the next edit under the same
	// claim through in silence, which is worse than either posture on its own.
	if again := bob.stdin(event).run("guard", "--ask"); again.stdout == "" {
		t.Error("the second edit was not asked about")
	}
	if both := bob.stdin(event).run("guard", "--ask", "--deny"); both.code != 1 {
		t.Errorf("exit %d for --ask with --deny, want 1: they ask for opposite things", both.code)
	}
}

func TestGuardSaysNothing(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("claim", "parser-panic", "--note", "on it", "--paths", "parser.go")
	alice.mustRun("claim", "prose-only", "--note", "the whole lexer, roughly")
	bob.mustRun("claim", "bobs-own", "--paths", "lexer.go")

	for _, tc := range []struct {
		name  string
		why   string
		event string
	}{
		{
			name:  "an unrelated file",
			why:   "a guard that complains about edits nobody claimed gets removed",
			event: hookEventJSON(t, "PreToolUse", bob.dir, "Edit", filepath.Join(bob.dir, "README.md")),
		},
		{
			name:  "a claim of your own",
			why:   "your own claim is permission, not an obstacle",
			event: hookEventJSON(t, "PreToolUse", bob.dir, "Edit", filepath.Join(bob.dir, "lexer.go")),
		},
		{
			name:  "a claim carrying only prose",
			why:   "the guard treats prose as unreadable rather than guessing at what it covers",
			event: hookEventJSON(t, "PreToolUse", bob.dir, "Edit", filepath.Join(bob.dir, "lex.go")),
		},
		{
			name:  "a tool that names no file",
			why:   "there is nothing to match a glob against, and guessing at a command line is a false positive",
			event: hookEventJSON(t, "PreToolUse", bob.dir, "Bash", ""),
		},
		{
			name:  "some other hook event",
			why:   "only PreToolUse carries a decision agora can make",
			event: hookEventJSON(t, "PostToolUse", bob.dir, "Edit", filepath.Join(bob.dir, "parser.go")),
		},
		{
			name:  "a path a pattern only partly matches",
			why:   "parser.go names a file at the root, and internal/parser.go is a different file",
			event: hookEventJSON(t, "PreToolUse", bob.dir, "Edit", filepath.Join(bob.dir, "internal", "parser.go")),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := bob.stdin(tc.event).run("guard")
			if got.code != 0 || got.stdout != "" {
				t.Errorf("exit %d, stdout %q, want 0 and nothing: %s", got.code, got.stdout, tc.why)
			}
		})
	}
}

// TestGuardKnowsWhoseEditItIsFromTheEvent is the same property as TestInjectBriefsTheSessionTheEventNames, in
// the layer where getting it wrong is loudest. A harness that keeps its session id out of a hook's environment
// would have the guard resolve some other member, and then the one claim it is certain to report is the agent's
// own. A warning about your own work on every edit under your own claim is how this layer gets switched off.
func TestGuardKnowsWhoseEditItIsFromTheEvent(t *testing.T) {
	const id = "01a03b15-06a4-7aa3-a7e5-46287dec58e9"
	alice := newCLI(t)
	agent := alice.withEnv(config.EnvMember, "").withEnv(config.EnvCodexThread, id)
	hook := alice.withEnv(config.EnvMember, "").withEnv(config.EnvAgent, "codex")
	agent.mustRun("claim", "parser-panic", "--note", "root cause is in the token loop", "--paths", "parser.go")

	event := hookEventAsJSON(t, "PreToolUse", hook.dir, "Edit", filepath.Join(hook.dir, "parser.go"), id)
	got := hook.stdin(event).run("guard")
	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", got.code, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("the guard warned a session about its own claim:\n%s", got.stdout)
	}

	// Somebody else's claim on the same file still reports, so this is quiet for the right reason.
	alice.mustRun("claim", "parser-rewrite", "--paths", "parser.go")
	if got := hook.stdin(event).run("guard"); !strings.Contains(got.stdout, "parser-rewrite") {
		t.Errorf("the guard missed another member's claim:\nstdout: %s\nstderr: %s", got.stdout, got.stderr)
	}
}

// TestGuardSeesACodexEdit is the difference between this layer being wired on Codex and being decorative there.
// Codex edits through apply_patch: one call, the patch text in tool_input, and no file_path in the event at all.
// A guard reading file_path is therefore silent on every edit Codex makes, which looks exactly like an agent
// whose edits never overlap anybody.
//
// The headers are from codex-rs/apply-patch/src/parser.rs. Move to is a rename's destination, which is a file the
// call writes.
func TestGuardSeesACodexEdit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch string
	}{
		{name: "an update", patch: "*** Begin Patch\n*** Update File: parser.go\n@@\n-old\n+new\n*** End Patch\n"},
		{name: "an addition", patch: "*** Begin Patch\n*** Add File: parser.go\n+package main\n*** End Patch\n"},
		{name: "a deletion", patch: "*** Begin Patch\n*** Delete File: parser.go\n*** End Patch\n"},
		{
			name: "a rename into a claimed file",
			patch: "*** Begin Patch\n*** Update File: lexer.go\n*** Move to: parser.go\n@@\n-old\n+new\n" +
				"*** End Patch\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alice := newCLI(t)
			bob := alice.as("bob")
			alice.mustRun("claim", "parser-panic", "--note", "the token loop", "--paths", "parser.go")

			got := bob.stdin(codexEventJSON(t, bob.dir, "apply_patch", tc.patch)).run("guard")
			if got.code != 0 {
				t.Fatalf("exit code = %d, want 0\nstderr: %s", got.code, got.stderr)
			}
			if got.stdout == "" {
				t.Fatalf("the guard said nothing about an edit to a claimed file\nstderr: %s", got.stderr)
			}
			said := said(decode[hookOutput](t, got).HookSpecificOutput)
			// The file has to be named, since one call can touch several and "somebody is nearby" is not
			// something an agent can act on.
			for _, want := range []string{"alice", "parser-panic", "the token loop", "parser.go"} {
				if !strings.Contains(said, want) {
					t.Errorf("what it said does not mention %q:\n%s", want, said)
				}
			}
		})
	}
}

// TestGuardReadsEveryFileInOnePatch is what makes a patch different from an Edit. A call that touches five files
// runs into a claim if any one of them does, and reading only the first header would miss it.
func TestGuardReadsEveryFileInOnePatch(t *testing.T) {
	alice := newCLI(t)
	carol := alice.as("carol")
	bob := alice.as("bob")
	alice.mustRun("claim", "parser-panic", "--paths", "internal/lex/**")
	carol.mustRun("claim", "docs-rewrite", "--paths", "README.md")

	patch := "*** Begin Patch\n" +
		"*** Update File: CHANGELOG.md\n@@\n+a line\n" +
		"*** Update File: README.md\n@@\n+another\n" +
		"*** Update File: internal/lex/scan.go\n@@\n+bounds check\n" +
		"*** End Patch\n"
	said := said(decode[hookOutput](t, bob.stdin(codexEventJSON(t, bob.dir, "apply_patch", patch)).run("guard")).HookSpecificOutput)

	// Both claims, each named with the file of this call that it covers rather than with the first file in the
	// patch, and the unclaimed file is nobody's business.
	for _, want := range []string{"alice", "parser-panic", "scan.go", "carol", "docs-rewrite", "README.md"} {
		if !strings.Contains(said, want) {
			t.Errorf("what it said does not mention %q:\n%s", want, said)
		}
	}
	if strings.Contains(said, "CHANGELOG.md") {
		t.Errorf("the notice named a file nobody claimed:\n%s", said)
	}
}

// TestGuardDoesNotReadAShellCommandAsAPatch keeps the one rule that makes this layer tolerable. tool_input.command
// is also where Codex puts a shell command, and a guard that read patch headers out of any command would fire on
// a grep for one. Only apply_patch carries a patch.
func TestGuardDoesNotReadAShellCommandAsAPatch(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("claim", "parser-panic", "--paths", "parser.go")

	// A heredoc, so the markers are at the start of a line exactly as they are in a patch. A command that
	// merely mentioned one on the same line as something else would pass whether or not the tool is checked,
	// which is what the first version of this test did.
	command := "cat > /tmp/notes.patch <<'PATCH'\n*** Begin Patch\n*** Update File: parser.go\n@@\n+a note\n" +
		"*** End Patch\nPATCH\n"
	got := bob.stdin(codexEventJSON(t, bob.dir, "Bash", command)).run("guard")
	if got.code != 0 || got.stdout != "" {
		t.Errorf("exit %d, stdout %q, want 0 and nothing: a shell command is not a patch", got.code, got.stdout)
	}
}

// TestCandidatePathsSeesThroughASymlink is the bug that made the guard silent on every edit a Codex agent
// made, found by running scripts/codex-hook-experiment.sh: the arm edited
// /tmp/agoracodex.N6bG3C/repo/.worktrees/deny-only/parser.go while git reported the worktree as
// /private/tmp/agoracodex.N6bG3C/..., because /tmp is a symlink on darwin. The relative path between those two
// is full of "..", so a claim on `parser.go` matched nothing and the layer reported no overlap at all.
//
// The two sides come from different places and cannot be assumed to agree: agora asks git, which resolves, and
// a harness reports whatever path it was handed.
func TestCandidatePathsSeesThroughASymlink(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "internal"), 0o755); err != nil {
		t.Fatalf("create the worktree: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink the worktree: %v", err)
	}

	// The file as the harness names it, through the symlink, against the worktree as git reports it.
	edited := filepath.Join(link, "internal", "lex.go")
	got := candidatePaths(edited, link, real)
	want := []string{edited, filepath.Join("internal", "lex.go")}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("candidatePaths(), -want +got:\n%s", diff)
	}

	// A file the edit is about to create has no path to resolve, and it still has to match: resolving the whole
	// path would fail here and take the relative candidate with it.
	created := filepath.Join(link, "internal", "brand-new.go")
	got = candidatePaths(created, link, real)
	want = []string{created, filepath.Join("internal", "brand-new.go")}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("candidatePaths() for a file that does not exist yet, -want +got:\n%s", diff)
	}
}

func TestGuardWithNoDatabaseDoesNotCreateOne(t *testing.T) {
	c := newCLI(t)
	event := hookEventJSON(t, "PreToolUse", c.dir, "Edit", filepath.Join(c.dir, "parser.go"))

	got := c.stdin(event).run("guard")
	if got.code != 0 || got.stdout != "" {
		t.Errorf("exit %d, stdout %q, want 0 and nothing when nothing is claimed", got.code, got.stdout)
	}
	// A hook in the path of every matching edit must not be the thing that creates a database.
	if _, err := os.Stat(c.database()); err == nil {
		t.Error("agora guard created the database")
	}
}

func TestGuardLetsAnEditThroughWhenItCannotDecide(t *testing.T) {
	c := newCLI(t)
	c.mustRun("claim", "parser-panic", "--paths", "parser.go")

	for _, tc := range []struct {
		name  string
		stdin string
	}{
		{name: "malformed json", stdin: "{not json"},
		{name: "nothing at all", stdin: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := c.stdin(tc.stdin).run("guard")
			// Reporting its own failure matters less than not blocking work: a guard that can stop an
			// edit by breaking is a guard that gets deleted, and then nothing is enforced.
			if got.code != 0 || got.stdout != "" {
				t.Errorf("exit %d, stdout %q, want 0 and nothing on stdout", got.code, got.stdout)
			}
			if got.stderr == "" {
				t.Error("stderr is empty, want the failure reported somewhere")
			}
		})
	}
}

func TestGuardNamesEveryClaimCoveringTheFile(t *testing.T) {
	alice := newCLI(t)
	carol := alice.as("carol")
	bob := alice.as("bob")
	alice.mustRun("claim", "parser-panic", "--paths", "parser.go")
	carol.mustRun("claim", "parser-rewrite", "--paths", "parser.go,ast.go")

	event := hookEventJSON(t, "PreToolUse", bob.dir, "Write", filepath.Join(bob.dir, "parser.go"))
	decision := decode[hookOutput](t, bob.stdin(event).run("guard")).HookSpecificOutput
	// Two agents have already collided here. Naming one of them would send bob to negotiate with half
	// the people who care.
	for _, want := range []string{"alice", "parser-panic", "carol", "parser-rewrite"} {
		if !strings.Contains(said(decision), want) {
			t.Errorf("what it said does not mention %q:\n%s", want, said(decision))
		}
	}
}

func TestGuardTruncatesALongNote(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	long := strings.Repeat("this note goes on. ", 200)
	alice.mustRun("claim", "parser-panic", "--note", long, "--paths", "parser.go")

	event := hookEventJSON(t, "PreToolUse", bob.dir, "Edit", filepath.Join(bob.dir, "parser.go"))
	decision := decode[hookOutput](t, bob.stdin(event).run("guard")).HookSpecificOutput
	// Hook output over 10000 characters is spilled to a file and replaced with a preview, and a guard
	// whose message became a preview has explained nothing.
	if len(said(decision)) > 2000 {
		t.Errorf("what it said is %d characters, want it bounded well under the 10000 that gets spilled",
			len(said(decision)))
	}
	if !strings.Contains(said(decision), "...") {
		t.Error("a truncated note does not say it was truncated")
	}
}

func TestGuardUsesTheEventsWorkingDirectory(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("claim", "parser-panic", "--paths", "parser.go")

	// A hook runs wherever the harness happens to be, and reports the agent's directory in the event.
	// Resolving against anything else matches the wrong file.
	elsewhere := t.TempDir()
	bob.dir = elsewhere
	event := hookEventJSON(t, "PreToolUse", alice.dir, "Edit", "parser.go")
	got := bob.stdin(event).run("guard")
	if got.stdout == "" {
		t.Errorf("guard said nothing, want a notice resolved against the event's cwd\nstderr: %s", got.stderr)
	}
}
