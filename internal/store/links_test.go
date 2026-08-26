package store_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

// TestNamingAThreadLinksIt is the split record made traversable. Measured across 18 sessions: an agent opens a
// thread per site whatever it is told, and what a standing rule does move is that it names the new thread in the
// one it came out of. That pointer was prose, so only a person could follow it.
//
// The backward case is the one that matters, and it is not an edge case: in every run that linked, the pointer
// was posted *before* the thread it named existed. A scan of the threads present at post time would have found
// none of them.
func TestNamingAThreadLinksIt(t *testing.T) {
	s, clock := newStore(t)

	first := post(t, s, "repo", "alice", "parser-empty-input", "fixing the unchecked Fields()[0] in Parse")
	clock.advance(time.Minute)
	// The pointer, written before lex-empty-input exists, and ending the sentence: every reference in every
	// measured run did, and the member boundary rule treats a full stop as part of a name, so this exact shape
	// matched nothing until threadPart existed.
	pointer := post(t, s, "repo", "alice", "parser-empty-input",
		"Lex has the same unchecked slice; tracking that separate fix in lex-empty-input.")
	clock.advance(time.Minute)
	opened := post(t, s, "repo", "alice", "lex-empty-input", "fixing Lex the same way")
	clock.advance(time.Minute)
	// And the forward direction, from a thread that exists to one that already did.
	docs := post(t, s, "repo", "bob", "docs-empty-input",
		"documenting both, see parser-empty-input for the root cause")

	got, err := s.Threads(t.Context(), store.ThreadsRequest{Channel: "repo", Member: "carol"})
	if err != nil {
		t.Fatalf("Threads(): %v", err)
	}
	want := []store.Thread{
		{
			Channel:  "repo",
			Name:     "docs-empty-input",
			Unread:   1,
			First:    &docs,
			Messages: 1,
			LastAt:   baseTime.Add(3 * time.Minute),
			Related:  []string{"parser-empty-input"},
		},
		{
			Channel:  "repo",
			Name:     "lex-empty-input",
			Unread:   1,
			First:    &opened,
			Messages: 1,
			LastAt:   baseTime.Add(2 * time.Minute),
			Related:  []string{"parser-empty-input"},
		},
		{
			Channel:  "repo",
			Name:     "parser-empty-input",
			Unread:   2,
			First:    &first,
			Messages: 2,
			LastAt:   baseTime.Add(time.Minute),
			// Both ends, oldest reference first, and undirected: parser-empty-input named the first and was
			// named by the second, and a reader of it needs both.
			Related: []string{"lex-empty-input", "docs-empty-input"},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Threads() with links, -want +got:\n%s", diff)
	}
	if pointer.Seq != 2 {
		t.Errorf("the pointer landed at seq %d, want 2", pointer.Seq)
	}
}

// TestAOneWordThreadNeedsAHash is the false-link guard. A thread called "general" or "docs" turns up in ordinary
// prose, and a link to a thread that has nothing to do with yours costs the reader the same as a missing one.
func TestAOneWordThreadNeedsAHash(t *testing.T) {
	s, _ := newStore(t)
	post(t, s, "repo", "alice", "general", "anything")
	post(t, s, "repo", "alice", "parser-panic", "in general this is the token loop, and docs are wrong too")

	byName := map[string][]string{}
	for _, thread := range threads(t, s, "carol", false) {
		byName[thread.Name] = thread.Related
	}
	if byName["parser-panic"] != nil {
		t.Errorf("prose linked parser-panic to %v, want nothing", byName["parser-panic"])
	}

	post(t, s, "repo", "alice", "parser-panic", "the answer is in #general")
	byName = map[string][]string{}
	for _, thread := range threads(t, s, "carol", false) {
		byName[thread.Name] = thread.Related
	}
	if diff := cmp.Diff([]string{"general"}, byName["parser-panic"]); diff != "" {
		t.Errorf("#general did not link, -want +got:\n%s", diff)
	}
	if diff := cmp.Diff([]string{"parser-panic"}, byName["general"]); diff != "" {
		t.Errorf("the link is not visible from the other end, -want +got:\n%s", diff)
	}
}

// TestDeletingAThreadTakesItsLinks stops a reader being offered a name that resolves to nothing.
func TestDeletingAThreadTakesItsLinks(t *testing.T) {
	s, _ := newStore(t)
	post(t, s, "repo", "alice", "parser-empty-input", "fixing Parse, and lex-empty-input has the same bug")
	post(t, s, "repo", "alice", "lex-empty-input", "fixing Lex")

	if _, err := s.Delete(t.Context(), store.DeleteRequest{
		Channel: "repo", Member: "alice", Thread: "lex-empty-input",
	}); err != nil {
		t.Fatalf("Delete(): %v", err)
	}

	got := threads(t, s, "carol", false)
	want := []store.Thread{{
		Channel:  "repo",
		Name:     "parser-empty-input",
		Unread:   1,
		First:    new(store.Message),
		Messages: 1,
		LastAt:   baseTime,
	}}
	*want[0].First = store.Message{
		Seq: 1, Number: 1, Channel: "repo", Thread: "parser-empty-input", Author: "alice",
		Body: "fixing Parse, and lex-empty-input has the same bug", CreatedAt: baseTime,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Threads() after deleting the far end, -want +got:\n%s", diff)
	}
}

// TestAFullStopEndsASentenceNotAThreadName is the bug the end-to-end check found and the tests had missed. Every
// reference in every measured run ended a sentence, and the member boundary rule counts '.' as part of a name so
// that alice.dev is one name, so the rule that missed one missed all of them.
func TestAFullStopEndsASentenceNotAThreadName(t *testing.T) {
	s, _ := newStore(t)
	post(t, s, "repo", "alice", "lex-empty-input", "fixing Lex")
	post(t, s, "repo", "alice", "parser-empty-input", "tracking that separate fix in lex-empty-input.")
	// And a filename is still a filename: a stop followed by a name character continues the token.
	post(t, s, "repo", "alice", "docs-rewrite", "the fix is in lex-empty-input.go and nowhere else")

	byName := map[string][]string{}
	for _, thread := range threads(t, s, "carol", false) {
		byName[thread.Name] = thread.Related
	}
	if diff := cmp.Diff([]string{"lex-empty-input"}, byName["parser-empty-input"]); diff != "" {
		t.Errorf("a sentence-final reference did not link, -want +got:\n%s", diff)
	}
	if byName["docs-rewrite"] != nil {
		t.Errorf("a filename linked docs-rewrite to %v, want nothing", byName["docs-rewrite"])
	}
}

// TestRelatedIsBoundedAndKeepsTheRecentEnd pins what one related thread contributes. Bounded for the reason
// everything reaching a model here is: hook output over 10000 characters is replaced by a preview, and a thread
// somebody has worked all day has no natural length. The newest are kept, the same choice dump makes.
func TestRelatedIsBoundedAndKeepsTheRecentEnd(t *testing.T) {
	s, clock := newStore(t)
	post(t, s, "repo", "alice", "parser-panic", "the root cause is in lexer-panic")
	for i := 1; i <= 7; i++ {
		clock.advance(time.Minute)
		post(t, s, "repo", "alice", "lexer-panic", fmt.Sprintf("step %d", i))
	}

	got, err := s.Read(t.Context(), store.ReadRequest{
		Channel: "repo", Member: "carol", Thread: "parser-panic", Related: true,
	})
	if err != nil {
		t.Fatalf("Read(): %v", err)
	}
	if len(got.Related) != 1 {
		t.Fatalf("Read() returned %d related threads, want 1", len(got.Related))
	}
	entry := got.Related[0]
	bodies := make([]string, 0, len(entry.Messages))
	for _, msg := range entry.Messages {
		bodies = append(bodies, msg.Body)
	}
	// The last five, oldest first within the window, and the two it dropped counted rather than hidden: a
	// truncated thread that looks short is worse than one that says where it stopped.
	want := []string{"step 3", "step 4", "step 5", "step 6", "step 7"}
	if diff := cmp.Diff(want, bodies); diff != "" {
		t.Errorf("the related thread's window, -want +got:\n%s", diff)
	}
	if entry.Omitted != 2 {
		t.Errorf("omitted = %d, want the 2 older messages counted", entry.Omitted)
	}
}
