package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/chancez/agora/internal/config"
	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

// codexPayload is a Codex hook event carrying every field that event's generated schema marks required, from
// codex-rs/hooks/schema/generated. The extra fields are what that event adds to the common ones.
//
// The point of building them in full rather than minimally is the two ways this can break without anybody
// noticing: agora starting to depend on a field Codex does not send, and agora stopping to tolerate one it does.
// transcript_path is null because its schema says the type is string or null, and a hook has no business reading
// a transcript anyway.
func codexPayload(t *testing.T, event, cwd, session string, extra map[string]any) string {
	t.Helper()
	payload := map[string]any{
		"session_id":      session,
		"cwd":             cwd,
		"hook_event_name": event,
		"model":           "gpt-5.1-codex",
		"permission_mode": "default",
		"transcript_path": nil,
	}
	for k, v := range extra {
		payload[k] = v
	}
	return eventJSON(t, payload)
}

// TestCodexEventsDriveEveryHook is the compatibility check for the second harness, and it stands in for the
// experiment that could not be run: no Codex session was available, so what is pinned here is that each hook
// command does its job when handed the event Codex actually sends, identity included.
//
// One session id throughout, in the event and nowhere else, because that is the whole of what a Codex hook is
// given: $AGORA_AGENT says which harness, and there is no session variable in the environment to fall back on.
func TestCodexEventsDriveEveryHook(t *testing.T) {
	const id = "01a03b15-06a4-7aa3-a7e5-46287dec58e9"
	const member = "codex-e7784ad5"

	// hooked is the caller a Codex hook is: no identity in its environment at all, only the harness's name.
	hooked := func(c *cli) *cli {
		return c.withEnv(config.EnvMember, "").withEnv(config.EnvAgent, "codex")
	}

	t.Run("inject on SessionStart", func(t *testing.T) {
		alice := newCLI(t)
		hook := hooked(alice)
		alice.mustRun("post", "parser-panic", "empty input reaches the token loop")

		event := codexPayload(t, "SessionStart", hook.dir, id, map[string]any{"source": "startup"})
		got := hook.stdin(event).run("inject")
		if got.code != 0 {
			t.Fatalf("agora inject exited %d\nstderr: %s", got.code, got.stderr)
		}
		text := decode[injectOutput](t, got).HookSpecificOutput.AdditionalContext
		for _, want := range []string{member, "parser-panic", "empty input reaches the token loop"} {
			if !strings.Contains(text, want) {
				t.Errorf("the briefing is missing %q:\n%s", want, text)
			}
		}
	})

	t.Run("guard on PreToolUse", func(t *testing.T) {
		alice := newCLI(t)
		hook := hooked(alice)
		alice.mustRun("claim", "parser-panic", "--note", "the token loop", "--paths", "parser.go")

		event := codexPayload(t, "PreToolUse", hook.dir, id, map[string]any{
			"tool_name":   "apply_patch",
			"tool_use_id": "call_1",
			"turn_id":     "turn_1",
			"tool_input": map[string]string{
				"command": "*** Begin Patch\n*** Update File: parser.go\n@@\n-old\n+new\n*** End Patch\n",
			},
		})
		said := said(decode[hookOutput](t, hook.stdin(event).run("guard")).HookSpecificOutput)
		for _, want := range []string{"alice", "parser-panic", "the token loop", "parser.go"} {
			if !strings.Contains(said, want) {
				t.Errorf("the notice is missing %q:\n%s", want, said)
			}
		}
	})

	t.Run("inject on PostToolUse", func(t *testing.T) {
		// The mid-task layer, which is a tool event rather than a turn boundary, so it carries the tool fields.
		alice := newCLI(t)
		hook := hooked(alice)
		alice.mustRun("post", "lexer-bounds", "the lexer drops the last token")

		event := codexPayload(t, "PostToolUse", hook.dir, id, map[string]any{
			"tool_name":     "Bash",
			"tool_use_id":   "call_1",
			"turn_id":       "turn_1",
			"tool_input":    map[string]string{"command": "go test ./..."},
			"tool_response": map[string]any{"output": "ok"},
		})
		got := hook.stdin(event).run("inject", "--limit", "3")
		if got.code != 0 {
			t.Fatalf("agora inject exited %d\nstderr: %s", got.code, got.stderr)
		}
		if !strings.Contains(got.stdout, "the lexer drops the last token") {
			t.Errorf("nothing reached the model mid-task:\n%s", got.stdout)
		}
	})

	t.Run("doorbell on Stop", func(t *testing.T) {
		alice := newCLI(t)
		hook := hooked(alice)
		// Addressed to the session by name, which is what a wake costs a turn for.
		alice.mustRun("post", "parser-panic", "@"+member+" does your fix cover the lexer as well?")

		event := codexPayload(t, "Stop", hook.dir, id, map[string]any{
			"stop_hook_active":       false,
			"turn_id":                "turn_1",
			"last_assistant_message": "Done.",
		})
		got := hook.stdin(event).run("doorbell")
		if got.code != 2 {
			t.Fatalf("agora doorbell exited %d, want 2: nothing else wakes an idle session\nstderr: %s",
				got.code, got.stderr)
		}
		if !strings.Contains(got.stderr, "does your fix cover the lexer") {
			t.Errorf("the wake does not carry the message:\n%s", got.stderr)
		}

		// stop_hook_active is Codex's field too, and it has to be read, or exit 2 could hold a session open.
		active := codexPayload(t, "Stop", hook.dir, id, map[string]any{
			"stop_hook_active":       true,
			"turn_id":                "turn_2",
			"last_assistant_message": "Answering.",
		})
		if again := hook.stdin(active).run("doorbell"); again.code != 0 || again.stderr != "" {
			t.Errorf("the doorbell rang on a turn a stop hook already continued: exit %d\nstderr: %s",
				again.code, again.stderr)
		}
	})

	t.Run("leave on SessionEnd", func(t *testing.T) {
		alice := newCLI(t)
		hook := hooked(alice)
		// The session has been here and holds work, which is the case --force is for: nobody is going to
		// release a claim after the session ending holds it.
		hook.stdin(codexPayload(t, "SessionStart", hook.dir, id, map[string]any{"source": "startup"})).run("inject")
		hook.withEnv(config.EnvMember, member).mustRun("claim", "parser-panic", "--paths", "parser.go")

		// reason is always "other" on Codex today, so there is nothing to match on to tell an end from a switch.
		event := codexPayload(t, "SessionEnd", hook.dir, id, map[string]any{"reason": "other"})
		got := hook.stdin(event).run("leave", "--force")
		if got.code != 0 {
			t.Fatalf("agora leave exited %d\nstderr: %s", got.code, got.stderr)
		}
		result := decode[store.LeaveResult](t, got)
		// No cursors, and that is the briefing hook doing its job rather than a gap: inject never advances one,
		// since a hook cannot know whether its output reached the model.
		want := store.LeaveResult{
			Channel: "devtest",
			Member:  member,
			Left:    true,
			Claims:  []string{"parser-panic"},
		}
		if diff := cmp.Diff(want, result); diff != "" {
			t.Errorf("agora leave, -want +got:\n%s", diff)
		}
		for _, m := range decode[[]store.Member](t, alice.mustRun("members")) {
			if m.Name == member {
				t.Errorf("%s is still on the roster after its session ended", member)
			}
		}
	})

	t.Run("a notebook path is still a path", func(t *testing.T) {
		// Codex sends no notebook_path, and Claude Code does. The point of one binary for both harnesses is
		// that neither shape stops working, and paths() reads whichever the event has.
		alice := newCLI(t)
		bob := alice.as("bob")
		alice.mustRun("claim", "notebooks", "--paths", "analysis.ipynb")

		event := hookEventJSON(t, "PreToolUse", bob.dir, "NotebookEdit", filepath.Join(bob.dir, "analysis.ipynb"))
		if got := bob.stdin(event).run("guard"); !strings.Contains(got.stdout, "notebooks") {
			t.Errorf("the guard missed a notebook edit:\nstdout: %s\nstderr: %s", got.stdout, got.stderr)
		}
	})
}
