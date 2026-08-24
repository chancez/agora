package store_test

import (
	"testing"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

func threads(t *testing.T, s *store.Store, member string, unreadOnly bool) []store.Thread {
	t.Helper()
	filter := store.AllThreads
	if unreadOnly {
		filter = store.UnreadThreads
	}
	got, err := s.Threads(t.Context(), store.ThreadsRequest{
		Channel: "repo", Member: member, Filter: filter,
	})
	if err != nil {
		t.Fatalf("Threads(): %v", err)
	}
	return got
}

// TestThreadsIsAnIndex is the thing a notification carries. It has to be enough to decide whether a thread
// matters without reading it, which is why the first unread message comes with the count: the oldest one is
// what says what the thread is about.
func TestThreadsIsAnIndex(t *testing.T) {
	s, clock := newStore(t)
	ctx := t.Context()
	first := post(t, s, "repo", "alice", "parser-panic", "empty input reaches the token loop")
	clock.advance(time.Minute)
	post(t, s, "repo", "alice", "parser-panic", "and the lexer has it too")
	clock.advance(time.Minute)
	docs := post(t, s, "repo", "carol", "docs-rewrite", "renaming the config keys")
	if _, err := s.Claim(ctx, store.ClaimRequest{Channel: "repo", Thread: "parser-panic", Holder: "alice"}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}

	want := []store.Thread{
		{
			Channel:  "repo",
			Name:     "docs-rewrite",
			Unread:   1,
			First:    &docs,
			Messages: 1,
			LastAt:   baseTime.Add(2 * time.Minute),
		},
		{
			Channel:  "repo",
			Name:     "parser-panic",
			Unread:   2,
			First:    &first,
			Messages: 2,
			LastAt:   baseTime.Add(time.Minute),
			Claim:    "alice",
		},
	}
	// Most recently active first, since that is the order attention goes in.
	if diff := cmp.Diff(want, threads(t, s, "bob", false)); diff != "" {
		t.Errorf("Threads(), -want +got:\n%s", diff)
	}
}

func TestThreadsSkipsYourOwnPosts(t *testing.T) {
	s, _ := newStore(t)
	post(t, s, "repo", "alice", "parser-panic", "my own finding")

	// Nothing unread, because she wrote it, but the thread still exists and still reports its length.
	want := []store.Thread{{
		Channel: "repo", Name: "parser-panic", Messages: 1, LastAt: baseTime,
	}}
	if diff := cmp.Diff(want, threads(t, s, "alice", false)); diff != "" {
		t.Errorf("Threads() for the author, -want +got:\n%s", diff)
	}
	if got := threads(t, s, "alice", true); len(got) != 0 {
		t.Errorf("Threads(unread only) = %+v, want nothing", got)
	}
}

// TestAClaimedThreadWithNothingPostedStillAppears covers the gap between claiming work and saying anything
// about it. That gap is exactly when somebody else needs to know the work is taken, and the demo produced it
// on the first try: an agent claimed three files and told its user rather than the channel.
func TestAClaimedThreadWithNothingPostedStillAppears(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Claim(t.Context(), store.ClaimRequest{
		Channel: "repo", Thread: "parser-panic", Holder: "alice", Note: "fixing all three call sites",
	}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}

	want := []store.Thread{{Channel: "repo", Name: "parser-panic", LastAt: baseTime, Claim: "alice"}}
	if diff := cmp.Diff(want, threads(t, s, "bob", false)); diff != "" {
		t.Errorf("Threads(), -want +got:\n%s", diff)
	}
}

// TestReadingOneThreadLeavesTheOthersAlone is the whole reason cursors moved per thread. An agent that reads
// the thread it cares about must not have the others marked read behind its back.
func TestReadingOneThreadLeavesTheOthersAlone(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	mine := post(t, s, "repo", "alice", "parser-panic", "the token loop")
	post(t, s, "repo", "carol", "docs-rewrite", "renaming the config keys")

	got, err := s.Read(ctx, store.ReadRequest{
		Channel: "repo", Member: "bob", Thread: "parser-panic", Advance: true,
	})
	if err != nil {
		t.Fatalf("Read(): %v", err)
	}
	want := store.ReadResult{
		Channel:  "repo",
		Member:   "bob",
		Thread:   "parser-panic",
		Messages: []store.Message{mine},
		Cursors:  map[string]int64{"parser-panic": mine.Seq},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Read() of one thread, -want +got:\n%s", diff)
	}

	// The other thread is untouched, and the index says so.
	left := threads(t, s, "bob", true)
	if len(left) != 1 || left[0].Name != "docs-rewrite" || left[0].Unread != 1 {
		t.Errorf("unread after reading one thread = %+v, want only docs-rewrite", left)
	}
}

// TestAckDismissesAThreadWithoutReadingIt is the other half of triage. Deciding a thread is not yours is a
// decision, and without somewhere to record it the same index arrives every turn until somebody reads what
// they already judged irrelevant.
func TestAckDismissesAThreadWithoutReadingIt(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "alice", "parser-panic", "the token loop")
	docs := post(t, s, "repo", "carol", "docs-rewrite", "renaming the config keys")

	got, err := s.Ack(ctx, store.AckRequest{Channel: "repo", Member: "bob", Thread: "docs-rewrite"})
	if err != nil {
		t.Fatalf("Ack(): %v", err)
	}
	want := store.AckResult{
		Channel: "repo",
		Member:  "bob",
		Threads: []string{"docs-rewrite"},
		Cursors: map[string]int64{"docs-rewrite": docs.Seq},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Ack(), -want +got:\n%s", diff)
	}

	left := threads(t, s, "bob", true)
	if len(left) != 1 || left[0].Name != "parser-panic" {
		t.Errorf("unread after acknowledging one thread = %+v, want only parser-panic", left)
	}
}

// TestAckPutsTheCursorOnASequenceNotARowid is a regression test. Cursors hold a message's place in its
// channel, and Ack was setting them from the rowid, which is global: with any other channel in the same
// database the rowid is larger, so acknowledging left a cursor past the end of the thread and the next
// message posted to it arrived already read. Silent message loss, which is the one thing agora must not do.
func TestAckPutsTheCursorOnASequenceNotARowid(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	// Another channel first, so its rows take the low rowids and the two numbers cannot coincide.
	post(t, s, "other-repo", "alice", "unrelated", "one")
	post(t, s, "other-repo", "alice", "unrelated", "two")
	post(t, s, "repo", "alice", "docs-rewrite", "renaming the config keys")

	if _, err := s.Ack(ctx, store.AckRequest{Channel: "repo", Member: "bob", Thread: "docs-rewrite"}); err != nil {
		t.Fatalf("Ack(): %v", err)
	}
	post(t, s, "repo", "alice", "docs-rewrite", "and the defaults")

	unread := threads(t, s, "bob", true)
	if len(unread) != 1 || unread[0].Unread != 1 {
		t.Errorf("after acknowledging and one new message, unread = %+v, want docs-rewrite with 1", unread)
	}
}

func TestAckWithNoThreadDismissesEverything(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "alice", "parser-panic", "the token loop")
	post(t, s, "repo", "carol", "docs-rewrite", "renaming the config keys")
	last := post(t, s, "repo", "carol", "docs-rewrite", "and the defaults")

	got, err := s.Ack(ctx, store.AckRequest{Channel: "repo", Member: "bob"})
	if err != nil {
		t.Fatalf("Ack(): %v", err)
	}
	want := store.AckResult{
		Channel: "repo",
		Member:  "bob",
		// Most recently active first, the same order the index comes in.
		Threads: []string{"docs-rewrite", "parser-panic"},
		Cursors: map[string]int64{"docs-rewrite": last.Seq, "parser-panic": 1},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Ack() with no thread, -want +got:\n%s", diff)
	}
	if left := threads(t, s, "bob", true); len(left) != 0 {
		t.Errorf("unread after acknowledging everything = %+v, want nothing", left)
	}
}

func TestAckingAThreadWithNothingWaitingIsNotAnError(t *testing.T) {
	s, _ := newStore(t)

	got, err := s.Ack(t.Context(), store.AckRequest{Channel: "repo", Member: "bob", Thread: "quiet"})
	if err != nil {
		t.Fatalf("Ack(): %v", err)
	}
	// Where the cursor stands is more use than silence, and acknowledging twice must not fail the second
	// time: an agent that re-runs it should not have to reason about whether it already did.
	want := store.AckResult{Channel: "repo", Member: "bob", Cursors: map[string]int64{"quiet": 0}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Ack() of a quiet thread, -want +got:\n%s", diff)
	}
}

func TestMemberCountsThreadsAndMessages(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "alice", "parser-panic", "one")
	post(t, s, "repo", "alice", "parser-panic", "two")
	post(t, s, "repo", "carol", "docs-rewrite", "three")

	// Two numbers because they answer different questions: three messages of reading, spread over two
	// decisions about whether to read them.
	var bob store.Member
	if _, err := s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "bob"}); err != nil {
		t.Fatalf("Read(): %v", err)
	}
	members, err := s.Members(ctx, "repo")
	if err != nil {
		t.Fatalf("Members(): %v", err)
	}
	for _, m := range members {
		if m.Name == "bob" {
			bob = m
		}
	}
	if bob.Unread != 3 || bob.UnreadThreads != 2 {
		t.Errorf("bob = %d unread over %d threads, want 3 over 2", bob.Unread, bob.UnreadThreads)
	}
}

func TestChannels(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "alice", "parser-panic", "one")
	post(t, s, "repo", "alice", "docs-rewrite", "two")
	post(t, s, "other", "alice", "release", "three")
	if _, err := s.Claim(ctx, store.ClaimRequest{Channel: "repo", Thread: "unposted", Holder: "alice"}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}
	if _, err := s.Ack(ctx, store.AckRequest{Channel: "repo", Member: "bob", Thread: "docs-rewrite"}); err != nil {
		t.Fatalf("Ack(): %v", err)
	}

	got, err := s.Channels(ctx, "bob")
	if err != nil {
		t.Fatalf("Channels(): %v", err)
	}
	// Unread threads rather than unread messages, because a thread is one decision and that is what a
	// reader picks a project by. The claimed thread with nothing posted counts towards the total.
	want := []store.Channel{
		{ID: 2, Key: "other", CreatedAt: baseTime, Threads: 1, UnreadThreads: 1},
		{ID: 1, Key: "repo", CreatedAt: baseTime, Threads: 3, UnreadThreads: 1},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Channels(), -want +got:\n%s", diff)
	}
}

// TestReadingAQuietThreadStillReportsItsCursor is a small thing that matters to a caller: asking about one
// thread and being told nothing at all cannot be distinguished from the thread not existing.
func TestReadingAQuietThreadStillReportsItsCursor(t *testing.T) {
	s, _ := newStore(t)
	post(t, s, "repo", "alice", "parser-panic", "the token loop")

	got, err := s.Read(t.Context(), store.ReadRequest{Channel: "repo", Member: "bob", Thread: "quiet"})
	if err != nil {
		t.Fatalf("Read(): %v", err)
	}
	want := store.ReadResult{
		Channel: "repo",
		Member:  "bob",
		Thread:  "quiet",
		Cursors: map[string]int64{"quiet": 0},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Read() of a quiet thread, -want +got:\n%s", diff)
	}
}

// TestObservingDoesNotJoin is what a read-only view needs: the numbers without becoming a member. A viewer
// in the roster would change what every agent in the channel sees just by being open.
func TestObservingDoesNotJoin(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "alice", "parser-panic", "the token loop")

	got, err := s.Threads(ctx, store.ThreadsRequest{Channel: "repo", Member: "watcher", Observe: true})
	if err != nil {
		t.Fatalf("Threads(): %v", err)
	}
	if len(got) != 1 || got[0].Unread != 1 {
		t.Errorf("Threads() while observing = %+v, want the unread count anyway", got)
	}
	members, err := s.Members(ctx, "repo")
	if err != nil {
		t.Fatalf("Members(): %v", err)
	}
	for _, m := range members {
		if m.Name == "watcher" {
			t.Errorf("observing added %q to the roster", m.Name)
		}
	}
}
