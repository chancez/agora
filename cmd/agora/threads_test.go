package main

import (
	"strings"
	"testing"

	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

// setupThreads leaves bob with two threads waiting: one that concerns him and one that does not, which is
// the situation triage exists for.
func setupThreads(t *testing.T) (*cli, *cli) {
	t.Helper()
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("post", "parser-panic", "empty input reaches the token loop with no bounds check")
	alice.mustRun("post", "parser-panic", "the lexer indexes it the same way")
	alice.as("carol").mustRun("post", "docs-rewrite", "renaming the config keys, nothing to do with the parser")
	alice.mustRun("claim", "parser-panic", "--note", "fixing all three call sites", "--paths", "parser.go")
	return alice, bob
}

func TestThreadsIsTheIndex(t *testing.T) {
	_, bob := setupThreads(t)

	got := decode[[]store.Thread](t, bob.mustRun("threads", "--unread"))
	if len(got) != 2 {
		t.Fatalf("agora threads --unread returned %d threads, want 2: %+v", len(got), got)
	}
	byName := map[string]store.Thread{}
	for _, thread := range got {
		byName[thread.Name] = thread
	}
	parser, ok := byName["parser-panic"]
	if !ok {
		t.Fatalf("parser-panic is missing from %+v", got)
	}
	// The count says how much reading, the first unread message says what it is about, and the claim says
	// who owns it. Those three are what a decision to read or dismiss is made from.
	if parser.Unread != 2 || parser.Messages != 2 || parser.Claim != "alice" {
		t.Errorf("parser-panic = %+v, want 2 unread of 2, claimed by alice", parser)
	}
	if parser.First == nil || !strings.Contains(parser.First.Body, "token loop") {
		t.Errorf("parser-panic's first unread = %+v, want the oldest message", parser.First)
	}

	text := bob.mustRun("threads", "--unread", "--text").stdout
	for _, want := range []string{"parser-panic", "2 unread of 2", "claimed by alice", "token loop", "docs-rewrite"} {
		if !strings.Contains(text, want) {
			t.Errorf("--text output is missing %q:\n%s", want, text)
		}
	}
}

func TestThreadsWithoutUnreadShowsEverything(t *testing.T) {
	_, bob := setupThreads(t)
	bob.mustRun("ack", "--all")

	if unread := decode[[]store.Thread](t, bob.mustRun("threads", "--unread")); len(unread) != 0 {
		t.Errorf("agora threads --unread after dismissing everything = %+v, want nothing", unread)
	}
	// Dismissing is not deleting: the threads are still there to browse, which is what makes it safe to
	// dismiss something rather than a decision to regret.
	all := decode[[]store.Thread](t, bob.mustRun("threads"))
	if len(all) != 2 {
		t.Errorf("agora threads returned %d threads, want both still listed: %+v", len(all), all)
	}
	if got := bob.mustRun("threads").stdout; strings.Contains(got, "null") {
		t.Errorf("agora threads printed a null:\n%s", got)
	}
}

// TestReadingOneThreadLeavesTheOtherWaiting is the property the whole change is for.
func TestReadingOneThreadLeavesTheOtherWaiting(t *testing.T) {
	_, bob := setupThreads(t)

	got := decode[store.ReadResult](t, bob.mustRun("read", "--thread", "parser-panic", "--advance"))
	if len(got.Messages) != 2 || got.Thread != "parser-panic" {
		t.Fatalf("agora read --thread parser-panic = %+v, want both of that thread's messages", got)
	}
	for _, msg := range got.Messages {
		if msg.Thread != "parser-panic" {
			t.Errorf("read --thread returned a message from %q", msg.Thread)
		}
	}

	left := decode[[]store.Thread](t, bob.mustRun("threads", "--unread"))
	if len(left) != 1 || left[0].Name != "docs-rewrite" {
		t.Errorf("still waiting = %+v, want only docs-rewrite", left)
	}
}

func TestAckDismissesOneThread(t *testing.T) {
	_, bob := setupThreads(t)

	got := decode[store.AckResult](t, bob.mustRun("ack", "docs-rewrite"))
	if len(got.Threads) != 1 || got.Threads[0] != "docs-rewrite" {
		t.Errorf("agora ack docs-rewrite = %+v, want it to name that thread", got)
	}
	left := decode[[]store.Thread](t, bob.mustRun("threads", "--unread"))
	if len(left) != 1 || left[0].Name != "parser-panic" {
		t.Errorf("still waiting = %+v, want only parser-panic", left)
	}
	// Dismissing does not deliver the messages, which is the point: it records a decision made from the
	// index rather than from reading.
	if strings.Contains(bob.mustRun("ack", "docs-rewrite", "--text").stdout, "renaming the config keys") {
		t.Error("ack printed the message body, want only what it dismissed")
	}
}

// TestABareAckRefuses guards the easiest mistake in the tool: discarding every unread thread with a
// four-letter command and no argument.
func TestABareAckRefuses(t *testing.T) {
	_, bob := setupThreads(t)

	got := bob.run("ack")
	if got.code != 1 {
		t.Errorf("exit code = %d, want 1 for a bare ack", got.code)
	}
	if !strings.Contains(got.stderr, "--all") {
		t.Errorf("stderr = %q, want it to name the flag that means everything", got.stderr)
	}
	// Nothing was dismissed.
	if left := decode[[]store.Thread](t, bob.mustRun("threads", "--unread")); len(left) != 2 {
		t.Errorf("still waiting = %+v, want both threads untouched", left)
	}

	if both := bob.run("ack", "docs-rewrite", "--all"); both.code != 1 {
		t.Errorf("exit code = %d, want 1 for a thread and --all together", both.code)
	}
}

func TestAckAllDismissesEverything(t *testing.T) {
	_, bob := setupThreads(t)

	got := decode[store.AckResult](t, bob.mustRun("ack", "--all"))
	if len(got.Threads) != 2 {
		t.Errorf("agora ack --all = %+v, want both threads", got)
	}
	if left := decode[[]store.Thread](t, bob.mustRun("threads", "--unread")); len(left) != 0 {
		t.Errorf("still waiting = %+v, want nothing", left)
	}
}

func TestAckingSomethingQuietSaysSo(t *testing.T) {
	c := newCLI(t)

	got := c.mustRun("ack", "never-posted-to", "--text")
	if !strings.Contains(got.stdout, "nothing was waiting") {
		t.Errorf("agora ack of a quiet thread printed %q", got.stdout)
	}
}

func TestPostRequiresAThread(t *testing.T) {
	c := newCLI(t)

	// The thread is positional so it cannot be forgotten, and cobra reports the arity rather than the
	// store reporting a missing field.
	got := c.run("post", "just a body")
	if got.code != 1 {
		t.Errorf("exit code = %d, want 1 when only a body was given", got.code)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing", got.stdout)
	}
}

func TestDeletePreviewsBeforeItDeletes(t *testing.T) {
	_, bob := setupThreads(t)

	// The default is a preview and a nonzero status, so a script cannot mistake "here is what I would do"
	// for "done".
	preview := bob.run("delete", "docs-rewrite")
	if preview.code != 1 {
		t.Errorf("exit code = %d, want 1 for a preview", preview.code)
	}
	result := decode[store.DeleteResult](t, preview)
	if result.Deleted || result.Messages != 1 {
		t.Errorf("preview = %+v, want it not deleted and the count reported", result)
	}
	if left := decode[[]store.Thread](t, bob.mustRun("threads")); len(left) != 2 {
		t.Errorf("the preview changed the channel: %+v", left)
	}

	deleted := decode[store.DeleteResult](t, bob.mustRun("delete", "docs-rewrite", "--yes"))
	if !deleted.Deleted || deleted.Messages != 1 {
		t.Errorf("delete --yes = %+v, want it deleted", deleted)
	}
	left := decode[[]store.Thread](t, bob.mustRun("threads"))
	if len(left) != 1 || left[0].Name != "parser-panic" {
		t.Errorf("threads after deleting = %+v, want only parser-panic", left)
	}
}

// TestDeleteRefusesAnotherMembersThread is the rule that is stricter than release: a release they can see and
// argue with, where a deletion leaves them nothing to argue about.
func TestDeleteRefusesAnotherMembersThread(t *testing.T) {
	_, bob := setupThreads(t)

	refused := bob.run("delete", "parser-panic", "--yes")
	if refused.code != 1 {
		t.Errorf("exit code = %d, want 1 when somebody else has claimed it", refused.code)
	}
	result := decode[store.DeleteResult](t, refused)
	if result.Deleted || result.Claim != "alice" {
		t.Errorf("delete of a claimed thread = %+v, want it refused and alice named", result)
	}
	if text := bob.run("delete", "parser-panic", "--yes", "--text").stdout; !strings.Contains(text, "--force") {
		t.Errorf("the refusal does not say how to override it:\n%s", text)
	}

	forced := decode[store.DeleteResult](t, bob.mustRun("delete", "parser-panic", "--yes", "--force"))
	if !forced.Deleted {
		t.Errorf("delete --force = %+v, want it deleted", forced)
	}
}

func TestDeletingSomethingEmptySaysSo(t *testing.T) {
	c := newCLI(t)

	got := c.run("delete", "never-existed", "--yes")
	if got.code != 1 {
		t.Errorf("exit code = %d, want 1 when there was nothing to delete", got.code)
	}
	if text := c.run("delete", "never-existed", "--text").stdout; !strings.Contains(text, "nothing in it") {
		t.Errorf("printed %q", text)
	}
}

// TestThreadsMineIsWhatToCheckBeforeOpeningAnother is the question the notice raises, asked directly. It
// combines with the filters rather than replacing them: mine, with somebody's reply waiting in it.
func TestThreadsMineIsWhatToCheckBeforeOpeningAnother(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("post", "parser-panic", "fixing the token loop")
	alice.mustRun("post", "tui-widths", "the roster comes back at the minimum")
	bob.mustRun("post", "docs-rewrite", "renaming the config keys")
	bob.mustRun("post", "tui-widths", "Init runs before the first WindowSizeMsg")

	mine := decode[[]store.Thread](t, alice.mustRun("threads", "--mine"))
	names := make([]string, 0, len(mine))
	for _, thread := range mine {
		names = append(names, thread.Name)
	}
	if diff := cmp.Diff([]string{"tui-widths", "parser-panic"}, names); diff != "" {
		t.Errorf("agora threads --mine, -want +got:\n%s", diff)
	}

	waiting := decode[[]store.Thread](t, alice.mustRun("threads", "--mine", "--unread"))
	if len(waiting) != 1 || waiting[0].Name != "tui-widths" {
		t.Errorf("agora threads --mine --unread = %v, want only tui-widths", waiting)
	}
	if !strings.Contains(alice.mustRun("--text", "threads", "--mine").stdout, "parser-panic") {
		t.Error("agora --text threads --mine did not name parser-panic")
	}
}

// TestAReaderOfEitherHalfFindsTheOther is the point of links at the command line. Measured across 18 sessions: an
// agent opens a thread per site whatever the instruction says, and what it does do is name the new thread in the
// one it came out of. Until that pointer was recorded it was prose, so only a person could follow it.
func TestAReaderOfEitherHalfFindsTheOther(t *testing.T) {
	alice := newCLI(t)
	carol := alice.as("carol")
	alice.mustRun("post", "parser-empty-input", "fixing the unchecked Fields()[0] in Parse")
	// Written before the thread it names exists, which is what every measured agent did.
	alice.mustRun("post", "parser-empty-input", "Lex has the same bug; tracking that in lex-empty-input")
	alice.mustRun("post", "lex-empty-input", "fixing Lex the same way")

	from := decode[store.ReadResult](t, carol.mustRun("read", "--thread", "parser-empty-input"))
	if len(from.Related) != 1 || from.Related[0].Name != "lex-empty-input" || from.Related[0].Unread != 1 {
		t.Errorf("reading parser-empty-input did not offer lex-empty-input with its unread: %+v", from.Related)
	}
	// The far end reports the link too, though nobody wrote a word in it about the parser.
	to := decode[store.ReadResult](t, carol.mustRun("read", "--thread", "lex-empty-input"))
	if len(to.Related) != 1 || to.Related[0].Name != "parser-empty-input" {
		t.Errorf("reading lex-empty-input did not offer parser-empty-input: %+v", to.Related)
	}

	// And following one is the reader's decision. A read that consumed a thread nobody asked for is the failure
	// the display and advance split exists to prevent, so the related thread's cursor has to be where it was.
	advanced := decode[store.ReadResult](t, carol.mustRun("read", "--thread", "parser-empty-input", "--advance"))
	if advanced.Cursors["lex-empty-input"] != 0 {
		t.Errorf("advancing parser-empty-input moved lex-empty-input to %d, want 0",
			advanced.Cursors["lex-empty-input"])
	}
	still := decode[store.ReadResult](t, carol.mustRun("read", "--thread", "lex-empty-input"))
	if len(still.Messages) != 1 {
		t.Errorf("lex-empty-input has %d unread after reading the thread that names it, want 1",
			len(still.Messages))
	}

	text := carol.mustRun("--text", "threads").stdout
	if !strings.Contains(text, "related: lex-empty-input") {
		t.Errorf("agora --text threads does not show the link:\n%s", text)
	}
}
