package store_test

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

// post writes a message and fails the test if it cannot, so the tests below read as the sequence of
// events they are describing.
func post(t *testing.T, s *store.Store, channel, author, topic, body string) store.Message {
	t.Helper()
	msg, err := s.Post(t.Context(), store.PostRequest{Channel: channel, Author: author, Thread: topic, Body: body})
	if err != nil {
		t.Fatalf("Post(%q by %q): %v", body, author, err)
	}
	return msg
}

func TestPost(t *testing.T) {
	s, _ := newStore(t)

	got, err := s.Post(t.Context(), store.PostRequest{
		Channel: "repo",
		Author:  "alice",
		Thread:  "parser-panic",
		Body:    "empty input reaches the token loop with no bounds check",
	})
	if err != nil {
		t.Fatalf("Post(): %v", err)
	}
	want := store.Message{
		Seq:       1,
		Number:    1,
		Channel:   "repo",
		Author:    "alice",
		Thread:    "parser-panic",
		Body:      "empty input reaches the token loop with no bounds check",
		CreatedAt: baseTime,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Post(), -want +got:\n%s", diff)
	}
}

func TestPostRecordsThatTheAuthorWasHeardFrom(t *testing.T) {
	s, clock := newStore(t)
	ctx := t.Context()

	if _, err := s.Join(ctx, store.JoinRequest{Channel: "repo", Member: "alice"}); err != nil {
		t.Fatalf("Join(): %v", err)
	}
	clock.advance(5 * time.Minute)
	post(t, s, "repo", "alice", "general", "still here")

	got, err := s.Members(ctx, "repo")
	if err != nil {
		t.Fatalf("Members(): %v", err)
	}
	// Stale detection has nothing to work from unless something moves seen_at, and a post is the
	// strongest evidence a participant is alive.
	want := []store.Member{{
		Channel:  "repo",
		Name:     "alice",
		Posts:    1,
		JoinedAt: baseTime,
		SeenAt:   baseTime.Add(5 * time.Minute),
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Members() after a post, -want +got:\n%s", diff)
	}
}

func TestPostValidates(t *testing.T) {
	s, _ := newStore(t)

	for _, tc := range []struct {
		name string
		req  store.PostRequest
		want error
	}{
		{name: "no channel", req: store.PostRequest{Thread: "general", Author: "alice", Body: "x"}, want: store.ErrNoChannel},
		// A message with no thread is one nobody can triage, which is the whole reason threads exist.
		{name: "no thread", req: store.PostRequest{Channel: "repo", Author: "alice", Body: "x"}, want: store.ErrNoThread},
		{name: "no author", req: store.PostRequest{Channel: "repo", Thread: "general", Body: "x"}, want: store.ErrNoAuthor},
		{name: "no body", req: store.PostRequest{Channel: "repo", Thread: "general", Author: "alice"}, want: store.ErrNoBody},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Post(t.Context(), tc.req)
			if !errors.Is(err, tc.want) {
				t.Errorf("Post(%+v) error = %v, want %v", tc.req, err, tc.want)
			}
			if diff := cmp.Diff(store.Message{}, got); diff != "" {
				t.Errorf("Post(%+v), -want +got:\n%s", tc.req, diff)
			}
		})
	}
}

func TestReadDoesNotAdvanceTheCursorByDefault(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	first := post(t, s, "repo", "bob", "general", "parser.go panics on empty input")

	want := store.ReadResult{
		Channel:  "repo",
		Member:   "alice",
		Messages: []store.Message{first},
		Cursors:  map[string]int64{"general": 0},
	}
	// Twice, deliberately. A hook is the common caller and hook output that never reaches the model
	// would otherwise consume messages nobody read, so displaying and acknowledging are two calls.
	for i := range 2 {
		got, err := s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "alice"})
		if err != nil {
			t.Fatalf("Read() %d: %v", i+1, err)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("Read() %d, -want +got:\n%s", i+1, diff)
		}
	}
}

func TestReadWithAdvanceConsumesTheMessages(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	first := post(t, s, "repo", "bob", "general", "parser.go panics on empty input")
	second := post(t, s, "repo", "carol", "general", "same root cause as the lexer bug")

	got, err := s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "alice", Advance: true})
	if err != nil {
		t.Fatalf("Read(): %v", err)
	}
	want := store.ReadResult{
		Channel:  "repo",
		Member:   "alice",
		Messages: []store.Message{first, second},
		Cursors:  map[string]int64{"general": second.Seq},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Read() with advance, -want +got:\n%s", diff)
	}

	got, err = s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "alice", Advance: true})
	if err != nil {
		t.Fatalf("Read() again: %v", err)
	}
	want = store.ReadResult{Channel: "repo", Member: "alice", Cursors: map[string]int64{"general": second.Seq}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Read() again, -want +got:\n%s", diff)
	}
}

// TestYourOwnPostsAreNotUnreadToYou is why unread filters by author rather than trusting the cursor
// alone. A hook injects unread into context, so counting a member's own messages would hand an agent its
// own findings back every turn and report it as behind on work it did.
func TestYourOwnPostsAreNotUnreadToYou(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	theirs := post(t, s, "repo", "bob", "general", "from bob")
	post(t, s, "repo", "alice", "general", "from alice")

	got, err := s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "alice", Advance: true})
	if err != nil {
		t.Fatalf("Read(): %v", err)
	}
	// The cursor still moves past her own message, because it moves to the last id delivered and
	// hers is filtered rather than skipped over.
	want := store.ReadResult{
		Channel:  "repo",
		Member:   "alice",
		Messages: []store.Message{theirs},
		Cursors:  map[string]int64{"general": theirs.Seq},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Read(), -want +got:\n%s", diff)
	}
}

func TestReadLimitReportsWhatItLeftBehind(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	var posted []store.Message
	for _, body := range []string{"one", "two", "three", "four"} {
		posted = append(posted, post(t, s, "repo", "bob", "general", body))
	}

	got, err := s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "alice", Advance: true, Limit: 2})
	if err != nil {
		t.Fatalf("Read(): %v", err)
	}
	// Hook output over 10000 characters is spilled to a file and replaced with a preview, so a
	// truncated read has to say so rather than imply the channel is empty, and the cursor must stop
	// at the last message actually handed over.
	want := store.ReadResult{
		Channel:   "repo",
		Member:    "alice",
		Messages:  posted[:2],
		Cursors:   map[string]int64{"general": posted[1].Seq},
		Remaining: 2,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Read() with a limit, -want +got:\n%s", diff)
	}

	got, err = s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "alice", Advance: true, Limit: 2})
	if err != nil {
		t.Fatalf("Read() again: %v", err)
	}
	want = store.ReadResult{
		Channel:  "repo",
		Member:   "alice",
		Messages: posted[2:],
		Cursors:  map[string]int64{"general": posted[3].Seq},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Read() again, -want +got:\n%s", diff)
	}
}

func TestReadRecordsAMemberWhoNeverJoined(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	first := post(t, s, "repo", "bob", "general", "parser.go panics on empty input")

	// An agent's first contact with a channel is usually a hook running read, and a late joiner is
	// supposed to get history rather than need a replay.
	got, err := s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "alice"})
	if err != nil {
		t.Fatalf("Read(): %v", err)
	}
	want := store.ReadResult{Channel: "repo", Member: "alice", Messages: []store.Message{first}, Cursors: map[string]int64{"general": 0}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Read() without a join, -want +got:\n%s", diff)
	}

	members, err := s.Members(ctx, "repo")
	if err != nil {
		t.Fatalf("Members(): %v", err)
	}
	// bob is here too, from posting. Acting in a channel is what puts you in its roster, and his own
	// message is not unread to him.
	wantMembers := []store.Member{
		{Channel: "repo", Name: "alice", Unread: 1, UnreadThreads: 1, JoinedAt: baseTime, SeenAt: baseTime},
		{Channel: "repo", Name: "bob", Posts: 1, JoinedAt: baseTime, SeenAt: baseTime},
	}
	if diff := cmp.Diff(wantMembers, members); diff != "" {
		t.Errorf("Members() after a read, -want +got:\n%s", diff)
	}
}

// TestActingInAChannelPutsYouInTheRoster is the invariant that keeps the roster from depending on
// anyone remembering to join. The commands an agent runs constantly are post and read, and a hook
// running read is usually an agent's first contact with a channel.
func TestActingInAChannelPutsYouInTheRoster(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()

	if _, err := s.Post(ctx, store.PostRequest{
		Channel: "repo", Thread: "the-finding", Author: "poster", Body: "a finding", Worktree: new("/repo/.worktrees/one"),
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}
	if _, err := s.Read(ctx, store.ReadRequest{
		Channel: "repo", Member: "reader", Worktree: new("/repo/.worktrees/two"),
	}); err != nil {
		t.Fatalf("Read(): %v", err)
	}
	if _, err := s.Claim(ctx, store.ClaimRequest{
		Channel: "repo", Thread: "t", Holder: "claimer", Worktree: new("/repo/.worktrees/three"),
	}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}
	if _, err := s.Release(ctx, store.ReleaseRequest{
		Channel: "repo", Thread: "other", Holder: "releaser", Worktree: new("/repo/.worktrees/four"),
	}); err != nil {
		t.Fatalf("Release(): %v", err)
	}

	got, err := s.Members(ctx, "repo")
	if err != nil {
		t.Fatalf("Members(): %v", err)
	}
	// Everyone is one behind the poster's finding, and the poster is not behind their own.
	want := []store.Member{
		{Channel: "repo", Name: "claimer", Unread: 1, UnreadThreads: 1, Worktree: "/repo/.worktrees/three", JoinedAt: baseTime, SeenAt: baseTime},
		{Channel: "repo", Name: "poster", Posts: 1, Worktree: "/repo/.worktrees/one", JoinedAt: baseTime, SeenAt: baseTime},
		{Channel: "repo", Name: "reader", Unread: 1, UnreadThreads: 1, Worktree: "/repo/.worktrees/two", JoinedAt: baseTime, SeenAt: baseTime},
		{Channel: "repo", Name: "releaser", Unread: 1, UnreadThreads: 1, Worktree: "/repo/.worktrees/four", JoinedAt: baseTime, SeenAt: baseTime},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Members(), -want +got:\n%s", diff)
	}
}

func TestActingWithoutAWorktreeKeepsTheRecordedOne(t *testing.T) {
	s, clock := newStore(t)
	ctx := t.Context()
	if _, err := s.Join(ctx, store.JoinRequest{
		Channel: "repo", Member: "alice", Worktree: new("/repo/.worktrees/parser"),
	}); err != nil {
		t.Fatalf("Join(): %v", err)
	}

	clock.advance(time.Minute)
	// A caller that does not know where it is running must not erase where it was last seen running.
	if _, err := s.Post(ctx, store.PostRequest{Channel: "repo", Thread: "general", Author: "alice", Body: "still here"}); err != nil {
		t.Fatalf("Post(): %v", err)
	}

	got, err := s.Members(ctx, "repo")
	if err != nil {
		t.Fatalf("Members(): %v", err)
	}
	want := []store.Member{{
		Channel:  "repo",
		Name:     "alice",
		Posts:    1,
		Worktree: "/repo/.worktrees/parser",
		JoinedAt: baseTime,
		SeenAt:   baseTime.Add(time.Minute),
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Members(), -want +got:\n%s", diff)
	}
}

func TestReadIsScopedToOneChannel(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	mine := post(t, s, "repo", "bob", "general", "for this repo")
	post(t, s, "other-repo", "bob", "general", "for another repo")

	got, err := s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "alice", Advance: true})
	if err != nil {
		t.Fatalf("Read(): %v", err)
	}
	// Message ids are global, so a cursor from one channel would skip past another channel's
	// history if the query were not scoped.
	want := store.ReadResult{
		Channel:  "repo",
		Member:   "alice",
		Messages: []store.Message{mine},
		Cursors:  map[string]int64{"general": mine.Seq},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Read(), -want +got:\n%s", diff)
	}
}

func TestReadValidates(t *testing.T) {
	s, _ := newStore(t)

	for _, tc := range []struct {
		name string
		req  store.ReadRequest
		want error
	}{
		{name: "no channel", req: store.ReadRequest{Member: "alice"}, want: store.ErrNoChannel},
		{name: "no member", req: store.ReadRequest{Channel: "repo"}, want: store.ErrNoMember},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Read(t.Context(), tc.req)
			if !errors.Is(err, tc.want) {
				t.Errorf("Read(%+v) error = %v, want %v", tc.req, err, tc.want)
			}
			if diff := cmp.Diff(store.ReadResult{}, got); diff != "" {
				t.Errorf("Read(%+v), -want +got:\n%s", tc.req, diff)
			}
		})
	}
}

// TestReadWithAdvanceDeliversEachMessageExactlyOnce is one of the two races that matter. A cursor is
// read and written in the same call, so a sequential test proves nothing about the property: 20
// concurrent readers of one member must partition the messages between them, with no message
// delivered twice and none skipped. Run with -race.
func TestReadWithAdvanceDeliversEachMessageExactlyOnce(t *testing.T) {
	const readers = 20
	s, _ := newStore(t)
	ctx := t.Context()

	var posted []store.Message
	for i := range readers {
		posted = append(posted, post(t, s, "repo", "bob", "general", string(rune('a'+i))))
	}

	type outcome struct {
		messages []store.Message
		err      error
	}
	outcomes := make([]outcome, readers)
	var (
		start sync.WaitGroup
		done  sync.WaitGroup
	)
	start.Add(1)
	for i := range readers {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			// One message each, so the partition is the assertion rather than a coincidence of
			// whichever reader arrived first.
			result, err := s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "alice", Advance: true, Limit: 1})
			outcomes[i] = outcome{messages: result.Messages, err: err}
		}()
	}
	start.Done()
	done.Wait()

	var delivered []store.Message
	for i, got := range outcomes {
		if got.err != nil {
			t.Errorf("reader %d: Read(): %v", i, got.err)
		}
		if len(got.messages) != 1 {
			t.Errorf("reader %d: Read() returned %d messages, want 1", i, len(got.messages))
		}
		delivered = append(delivered, got.messages...)
	}
	if t.Failed() {
		return
	}
	// Sorting by id turns "each reader got a distinct message" into a comparison against the whole
	// posted history, so a duplicate or a gap shows up as a diff rather than as a count that happens
	// to match.
	slices.SortFunc(delivered, byID)
	if diff := cmp.Diff(posted, delivered); diff != "" {
		t.Errorf("messages delivered across %d concurrent readers, -want +got:\n%s", readers, diff)
	}
}

func byID(a, b store.Message) int {
	switch {
	case a.Seq < b.Seq:
		return -1
	case a.Seq > b.Seq:
		return 1
	}
	return 0
}

func TestMessagesFiltersByTopicAndKeepsTheNewestUnderALimit(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	first := post(t, s, "repo", "bob", "parser-panic", "empty input")
	second := post(t, s, "repo", "bob", "lexer", "unrelated")
	third := post(t, s, "repo", "carol", "parser-panic", "same root cause")
	fourth := post(t, s, "repo", "carol", "parser-panic", "fix is in the bounds check")

	for _, tc := range []struct {
		name string
		req  store.MessagesRequest
		want []store.Message
	}{
		{
			name: "everything, oldest first",
			req:  store.MessagesRequest{Channel: "repo"},
			want: []store.Message{first, second, third, fourth},
		},
		{
			name: "one topic",
			req:  store.MessagesRequest{Channel: "repo", Thread: "parser-panic"},
			want: []store.Message{first, third, fourth},
		},
		{
			name: "a limit keeps the newest",
			req:  store.MessagesRequest{Channel: "repo", Thread: "parser-panic", Limit: 2},
			want: []store.Message{third, fourth},
		},
		{
			name: "after an id",
			req:  store.MessagesRequest{Channel: "repo", After: third.Seq},
			want: []store.Message{fourth},
		},
		{
			name: "an unused channel",
			req:  store.MessagesRequest{Channel: "never-used"},
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Messages(ctx, tc.req)
			if err != nil {
				t.Fatalf("Messages(%+v): %v", tc.req, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Messages(%+v), -want +got:\n%s", tc.req, diff)
			}
		})
	}
}

// TestMessagesAreNumberedWithinTheirThread separates the two numbers. Seq counts within the channel and is
// what a cursor points at; Number counts within the thread and is what a reader is shown. A sibling thread
// still takes sequence numbers, so a thread whose Seq reads 1, 3 has to read #1, #2.
func TestMessagesAreNumberedWithinTheirThread(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()

	// Interleaved, and one in another channel: the other channel must not consume a number here, which is the
	// whole point of the sequence being per channel.
	for _, post := range []struct{ channel, thread, body string }{
		{"repo", "parser-panic", "the token loop"},
		{"other", "unrelated", "nothing to do with it"},
		{"repo", "docs-rewrite", "renaming the config keys"},
		{"repo", "parser-panic", "the lexer indexes it the same way"},
		{"repo", "docs-rewrite", "and the flags"},
	} {
		if _, err := s.Post(ctx, store.PostRequest{
			Channel: post.channel, Thread: post.thread, Author: "alice", Body: post.body,
		}); err != nil {
			t.Fatalf("Post(): %v", err)
		}
	}

	for _, tc := range []struct {
		thread string
		want   [][2]int64 // seq in the channel, number in the thread
	}{
		{thread: "parser-panic", want: [][2]int64{{1, 1}, {3, 2}}},
		{thread: "docs-rewrite", want: [][2]int64{{2, 1}, {4, 2}}},
	} {
		t.Run(tc.thread, func(t *testing.T) {
			messages, err := s.Messages(ctx, store.MessagesRequest{Channel: "repo", Thread: tc.thread})
			if err != nil {
				t.Fatalf("Messages(): %v", err)
			}
			got := make([][2]int64, 0, len(messages))
			for _, msg := range messages {
				got = append(got, [2]int64{msg.Seq, msg.Number})
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("seq and number, -want +got:\n%s", diff)
			}
		})
	}

	// The other channel starts at 1 of its own rather than continuing this one.
	other, err := s.Messages(ctx, store.MessagesRequest{Channel: "other"})
	if err != nil {
		t.Fatalf("Messages(): %v", err)
	}
	if len(other) != 1 || other[0].Seq != 1 || other[0].Number != 1 {
		t.Errorf("the other channel = %+v, want its own sequence starting at 1", other)
	}

	// A limit keeps the newest, and the number still counts from the start of the thread rather than from the
	// first row returned.
	newest, err := s.Messages(ctx, store.MessagesRequest{Channel: "repo", Thread: "parser-panic", Limit: 1})
	if err != nil {
		t.Fatalf("Messages(): %v", err)
	}
	if len(newest) != 1 || newest[0].Number != 2 {
		t.Errorf("the newest message alone = %+v, want it still numbered 2", newest)
	}

	// Every other path agrees: unread, and a post's own result.
	unread, err := s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "bob", Thread: "docs-rewrite"})
	if err != nil {
		t.Fatalf("Read(): %v", err)
	}
	if len(unread.Messages) != 2 || unread.Messages[0].Number != 1 || unread.Messages[1].Number != 2 {
		t.Errorf("unread = %+v, want them numbered 1 and 2", unread.Messages)
	}
	posted, err := s.Post(ctx, store.PostRequest{
		Channel: "repo", Thread: "docs-rewrite", Author: "bob", Body: "third",
	})
	if err != nil {
		t.Fatalf("Post(): %v", err)
	}
	if posted.Seq != 5 || posted.Number != 3 {
		t.Errorf("a third message in the thread came back as seq %d, #%d", posted.Seq, posted.Number)
	}
}

// TestConcurrentPostsGetDistinctSequences is the race the unique index would catch as an error. Assigning a
// sequence means reading max(seq) before writing, which is only safe because every transaction here opens with
// the write lock: twenty at once must produce twenty numbers, not one collision and nineteen posts.
func TestConcurrentPostsGetDistinctSequences(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()

	const posts = 20
	errs := make(chan error, posts)
	for i := range posts {
		go func() {
			_, err := s.Post(ctx, store.PostRequest{
				Channel: "repo", Thread: "parser-panic", Author: "alice",
				Body: fmt.Sprintf("finding %d", i),
			})
			errs <- err
		}()
	}
	for range posts {
		if err := <-errs; err != nil {
			t.Fatalf("Post(): %v", err)
		}
	}

	messages, err := s.Messages(ctx, store.MessagesRequest{Channel: "repo"})
	if err != nil {
		t.Fatalf("Messages(): %v", err)
	}
	got := make([]int64, 0, len(messages))
	for _, msg := range messages {
		got = append(got, msg.Seq)
	}
	want := make([]int64, posts)
	for i := range want {
		want[i] = int64(i + 1)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("sequences after %d concurrent posts, -want +got:\n%s", posts, diff)
	}
}
