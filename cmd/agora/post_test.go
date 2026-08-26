package main

import (
	"strings"
	"testing"

	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

// TestOpeningAThreadNamesWhatYouAlreadyHaveOpen is the thread-per-turn failure at the command line. Measured:
// one Codex session opened three threads in nine minutes for one continuous piece of work, two messages each,
// and the third partly reverted the first. Nothing had told it what it already had open, because unread never
// names your own threads: your own messages are read the moment you write them.
//
// So opening a thread says so and names the rest, which is the same rule a lost claim follows. The output is
// what stops the duplicate, not the exit code.
func TestOpeningAThreadNamesWhatYouAlreadyHaveOpen(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")

	first := decode[postResult](t, alice.mustRun("post", "parser-panic", "fixing the token loop"))
	recent(t, "created_at", &first.CreatedAt)
	wantFirst := postResult{
		Message: store.Message{Seq: 1, Channel: "devtest", Thread: "parser-panic",
			Author: "alice", Body: "fixing the token loop"},
		Opened: true,
	}
	// A member's first thread cannot be a duplicate of anything, so it is reported as opened and nothing else.
	if diff := cmp.Diff(wantFirst, first); diff != "" {
		t.Errorf("opening the first thread, -want +got:\n%s", diff)
	}

	// Continuing that thread is the behaviour being asked for, so it gets no notice at all.
	again := decode[postResult](t, alice.mustRun("post", "parser-panic", "the lexer has it too"))
	recent(t, "created_at", &again.CreatedAt)
	wantAgain := postResult{
		Message: store.Message{Seq: 2, Channel: "devtest", Thread: "parser-panic",
			Author: "alice", Body: "the lexer has it too"},
	}
	if diff := cmp.Diff(wantAgain, again); diff != "" {
		t.Errorf("continuing a thread, -want +got:\n%s", diff)
	}

	bob.mustRun("post", "docs-rewrite", "renaming the config keys")

	second := decode[postResult](t, alice.mustRun("post", "parser-panic-lexer", "same bug, second thread"))
	recent(t, "created_at", &second.CreatedAt)
	if len(second.Open) == 1 {
		recent(t, "open_threads[0].last_at", &second.Open[0].LastAt)
	}
	wantSecond := postResult{
		Message: store.Message{Seq: 4, Channel: "devtest", Thread: "parser-panic-lexer",
			Author: "alice", Body: "same bug, second thread"},
		Opened: true,
		// bob's thread is absent: what alice has open is not what she has to read, and a thread she never
		// spoke in is not a thread she can be continuing.
		Open: []store.Thread{{Channel: "devtest", Name: "parser-panic", Messages: 2}},
	}
	if diff := cmp.Diff(wantSecond, second); diff != "" {
		t.Errorf("opening a second thread, -want +got:\n%s", diff)
	}
}

// TestOpeningAThreadIsBoundedAndNewestFirst is the same rule the briefing follows: bound what gets printed. A
// member that has worked a channel all day would otherwise get its own history back on every new thread, and
// the newest are the ones the current work might belong in.
func TestOpeningAThreadIsBoundedAndNewestFirst(t *testing.T) {
	alice := newCLI(t)
	for _, thread := range []string{"oldest", "second", "third", "fourth"} {
		alice.mustRun("post", thread, "starting "+thread)
	}

	got := decode[postResult](t, alice.mustRun("post", "fifth", "starting fifth"))
	names := make([]string, 0, len(got.Open))
	for _, thread := range got.Open {
		names = append(names, thread.Name)
	}
	if diff := cmp.Diff([]string{"fourth", "third", "second"}, names); diff != "" {
		t.Errorf("open threads named, -want +got:\n%s", diff)
	}
}

// TestOpeningAThreadReadsAsPlainlyForAPerson checks the --text form, since a person who has just split their
// own work in two is the other reader of this.
func TestOpeningAThreadReadsAsPlainlyForAPerson(t *testing.T) {
	alice := newCLI(t)
	alice.mustRun("post", "parser-panic", "fixing the token loop")

	got := alice.mustRun("--text", "post", "parser-panic-lexer", "same bug, second thread")
	want := "posted 1 to devtest, opening parser-panic-lexer\n" +
		"you already have 1 thread open:\n" +
		"  parser-panic  1 message  0s ago\n" +
		"post to whichever of those this work continues, and keep this one for work that is genuinely separate\n"
	if got.stdout != want {
		t.Errorf("agora --text post\n got: %q\nwant: %q", got.stdout, want)
	}

	// And says nothing extra when the post went where it belongs.
	got = alice.mustRun("--text", "post", "parser-panic", "the lexer has it too")
	if want := "posted 2 to devtest\n"; got.stdout != want {
		t.Errorf("agora --text post to an existing thread = %q, want %q", got.stdout, want)
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
