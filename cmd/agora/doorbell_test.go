package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chancez/agora/internal/store"
)

// stopEventJSON is a Stop event as the harness writes one. stopHookActive is the field that says this turn is
// only running because a stop hook asked for it.
func stopEventJSON(t *testing.T, cwd string, stopHookActive bool) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"hook_event_name":  "Stop",
		"session_id":       "test-session",
		"cwd":              cwd,
		"stop_hook_active": stopHookActive,
	})
	if err != nil {
		t.Fatalf("marshal the hook event: %v", err)
	}
	return string(raw)
}

// TestDoorbellWakesForAnAnswerAddressedToYou is the whole point of the command: the exit code is what wakes an
// idle session, and the text on stderr is what the harness shows it.
func TestDoorbellWakesForAnAnswerAddressedToYou(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("post", "parser-panic", "empty input reaches the token loop with no bounds check")
	bob.mustRun("post", "parser-panic", "the same panic is in the lexer, so one fix has to cover both")

	got := alice.stdin(stopEventJSON(t, alice.dir, false)).run("doorbell")
	if got.code != 2 {
		t.Fatalf("agora doorbell exited %d, want 2: nothing else wakes an idle session\nstderr: %s", got.code, got.stderr)
	}
	// stdout stays empty: exit 2 is read from stderr, and a Stop hook's stdout is the harness's own channel.
	if got.stdout != "" {
		t.Errorf("agora doorbell wrote to stdout: %q", got.stdout)
	}
	for _, want := range []string{
		"alice",
		"parser-panic",
		"bob",
		"the same panic is in the lexer",
		"agora read --thread",
		"agora post",
		// The sentence that keeps a wake from reading as a task. Nobody asked this agent for anything.
		"Do not start work",
	} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the wake is missing %q:\n%s", want, got.stderr)
		}
	}

	// Once per message: an agent that read a wake and decided not to answer is not woken about it again on
	// the next turn, which is what stops two woken agents keeping each other awake.
	again := alice.stdin(stopEventJSON(t, alice.dir, false)).run("doorbell")
	if again.code != 0 || again.stderr != "" || again.stdout != "" {
		t.Errorf("agora doorbell rang twice for one message: exit %d\nstdout: %s\nstderr: %s",
			again.code, again.stdout, again.stderr)
	}
}

func TestDoorbellSaysNothingForWhatIsNotAddressedToYou(t *testing.T) {
	alice := newCLI(t)
	alice.as("bob").mustRun("post", "docs-rewrite", "starting on the setup guide")

	got := alice.stdin(stopEventJSON(t, alice.dir, false)).run("doorbell")
	if got.code != 0 || got.stderr != "" || got.stdout != "" {
		t.Errorf("agora doorbell woke alice for a thread she has nothing to do with: exit %d\nstdout: %s\nstderr: %s",
			got.code, got.stdout, got.stderr)
	}
}

// TestDoorbellSaysNothingWhenAStopHookIsAlreadyRunning is the loop guard. Exit 2 on a plain Stop hook keeps the
// turn going, so a doorbell that ignored this field could hold a session open indefinitely.
func TestDoorbellSaysNothingWhenAStopHookIsAlreadyRunning(t *testing.T) {
	alice := newCLI(t)
	alice.mustRun("post", "parser-panic", "empty input reaches the token loop with no bounds check")
	alice.as("bob").mustRun("post", "parser-panic", "the same panic is in the lexer")

	got := alice.stdin(stopEventJSON(t, alice.dir, true)).run("doorbell")
	if got.code != 0 || got.stderr != "" || got.stdout != "" {
		t.Fatalf("agora doorbell woke a turn that a stop hook is already keeping alive: exit %d\nstdout: %s\nstderr: %s",
			got.code, got.stdout, got.stderr)
	}
	// And it recorded nothing, so the message still arrives the next time the session actually stops.
	next := alice.stdin(stopEventJSON(t, alice.dir, false)).run("doorbell")
	if next.code != 2 {
		t.Errorf("agora doorbell exited %d on the next stop, want 2: the wake was swallowed\nstderr: %s",
			next.code, next.stderr)
	}
}

// TestDoorbellDryRunLooksWithoutSpendingTheWake keeps looking separate from ringing. A person or a script that
// asks what is waiting must not consume the wake the hook would have delivered.
func TestDoorbellDryRunLooksWithoutSpendingTheWake(t *testing.T) {
	alice := newCLI(t)
	alice.mustRun("post", "parser-panic", "empty input reaches the token loop with no bounds check")
	bob := alice.as("bob")
	bob.mustRun("post", "parser-panic", "the same panic is in the lexer")

	got := alice.stdin(stopEventJSON(t, alice.dir, false)).mustRun("doorbell", "--dry-run")
	if got.stderr != "" {
		t.Errorf("a dry run wrote a wake to stderr: %q", got.stderr)
	}
	rang := decode[store.DoorbellResult](t, got)
	if len(rang.Messages) != 1 || rang.Messages[0].Author != "bob" {
		t.Fatalf("agora doorbell --dry-run reported %+v, want bob's message", rang.Messages)
	}
	if rang.Rang != 0 {
		t.Errorf("agora doorbell --dry-run recorded a ring at %d, want 0", rang.Rang)
	}

	// The hook still has something to deliver, which is the property a dry run exists to keep.
	hooked := alice.stdin(stopEventJSON(t, alice.dir, false)).run("doorbell")
	if hooked.code != 2 {
		t.Errorf("agora doorbell exited %d after a dry run, want 2: looking spent the wake\nstderr: %s",
			hooked.code, hooked.stderr)
	}
}

func TestDoorbellSaysNothingWithNoDatabase(t *testing.T) {
	alice := newCLI(t)

	got := alice.stdin(stopEventJSON(t, alice.dir, false)).run("doorbell")
	if got.code != 0 || got.stderr != "" || got.stdout != "" {
		t.Errorf("agora doorbell with no database: exit %d\nstdout: %s\nstderr: %s",
			got.code, got.stdout, got.stderr)
	}
	// A hook at the end of every turn must not be what creates a database.
	if _, err := os.Stat(alice.database()); err == nil {
		t.Error("agora doorbell created the database")
	}
}

// TestDoorbellWaitRingsForAMessageThatArrivesWhileWaiting is the case the command exists for, and the only one
// that reaches a session already parked at a prompt: nothing is waiting when the doorbell starts.
//
// Real time rather than a synctest bubble, because this drives the command end to end through a real database
// and a real watcher. The wait is long and the interval short, so the assertion is on what happened rather than
// on how fast.
func TestDoorbellWaitRingsForAMessageThatArrivesWhileWaiting(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	// Alice's own thread, so bob's answer is addressed to her, and a channel that already exists so the
	// doorbell is not racing its creation.
	alice.mustRun("post", "parser-panic", "empty input reaches the token loop with no bounds check")

	done := make(chan result, 1)
	go func() {
		done <- alice.stdin(stopEventJSON(t, alice.dir, false)).run("doorbell", "--wait", "30s", "--interval", "5ms")
	}()

	// Long enough that the doorbell is in its wait rather than its first look, which is the path under test.
	// A message that arrives before it starts waiting still rings, so this cannot make the test flaky, only
	// less interesting.
	time.Sleep(200 * time.Millisecond)
	// A message in a thread alice has nothing to do with, first. The watch delivers it, so a wait that woke
	// on any post rather than asking the store again would ring for this one.
	bob.mustRun("post", "docs-rewrite", "starting on the setup guide")
	time.Sleep(50 * time.Millisecond)
	bob.mustRun("post", "parser-panic", "the same panic is in the lexer, so one fix has to cover both")

	select {
	case got := <-done:
		if got.code != 2 {
			t.Fatalf("agora doorbell --wait exited %d, want 2\nstdout: %s\nstderr: %s", got.code, got.stdout, got.stderr)
		}
		if !strings.Contains(got.stderr, "the same panic is in the lexer") {
			t.Errorf("the wake does not carry the message that arrived:\n%s", got.stderr)
		}
		if strings.Contains(got.stderr, "setup guide") {
			t.Errorf("the wake carries a thread alice has nothing to do with:\n%s", got.stderr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("agora doorbell --wait never returned after a message addressed to alice")
	}
}

// TestDoorbellWaitGivesUpQuietly is the other half of the wait: a timeout is this command working, not failing,
// and a hook that reports a timeout as an error every turn is one whose output stops being read.
func TestDoorbellWaitGivesUpQuietly(t *testing.T) {
	alice := newCLI(t)
	alice.mustRun("post", "parser-panic", "empty input reaches the token loop with no bounds check")

	got := alice.stdin(stopEventJSON(t, alice.dir, false)).run("doorbell", "--wait", "50ms", "--interval", "5ms")
	if got.code != 0 || got.stderr != "" || got.stdout != "" {
		t.Errorf("agora doorbell --wait after its timeout: exit %d\nstdout: %s\nstderr: %s",
			got.code, got.stdout, got.stderr)
	}
}
