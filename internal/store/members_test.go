package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

func TestJoin(t *testing.T) {
	s, _ := newStore(t)

	got, err := s.Join(t.Context(), store.JoinRequest{
		Channel:  "repo",
		Member:   "alice",
		Notify:   new("cm send alice"),
		Worktree: new("/repo/.worktrees/parser"),
	})
	if err != nil {
		t.Fatalf("Join(): %v", err)
	}
	want := store.Member{
		Channel:  "repo",
		Name:     "alice",
		Notify:   "cm send alice",
		Worktree: "/repo/.worktrees/parser",
		JoinedAt: baseTime,
		SeenAt:   baseTime,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Join(), -want +got:\n%s", diff)
	}
}

func TestJoinAgainKeepsTheCursorAndTheNudgeCommand(t *testing.T) {
	s, clock := newStore(t)
	ctx := t.Context()

	if _, err := s.Join(ctx, store.JoinRequest{Channel: "repo", Member: "alice", Notify: new("cm send alice")}); err != nil {
		t.Fatalf("Join(): %v", err)
	}
	if _, err := s.Post(ctx, store.PostRequest{Channel: "repo", Thread: "parser-panic", Author: "bob", Body: "parser.go panics on empty input"}); err != nil {
		t.Fatalf("Post(): %v", err)
	}
	if _, err := s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "alice", Advance: true}); err != nil {
		t.Fatalf("Read(): %v", err)
	}

	clock.advance(time.Minute)
	got, err := s.Join(ctx, store.JoinRequest{Channel: "repo", Member: "alice"})
	if err != nil {
		t.Fatalf("Join() again: %v", err)
	}
	// An agent that re-runs join must not re-read the channel from the beginning, so the message it
	// already read stays read, and a join without --notify must not wipe the nudge command an earlier
	// join configured.
	want := store.Member{
		Channel:  "repo",
		Name:     "alice",
		Notify:   "cm send alice",
		JoinedAt: baseTime,
		SeenAt:   baseTime.Add(time.Minute),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Join() again, -want +got:\n%s", diff)
	}
}

func TestJoinClearsTheNudgeCommandWhenAskedTo(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()

	if _, err := s.Join(ctx, store.JoinRequest{Channel: "repo", Member: "alice", Notify: new("cm send alice")}); err != nil {
		t.Fatalf("Join(): %v", err)
	}
	// An empty string is a value, unlike a nil pointer: a member whose session is gone needs a way
	// to stop agora shouting into it.
	got, err := s.Join(ctx, store.JoinRequest{Channel: "repo", Member: "alice", Notify: new("")})
	if err != nil {
		t.Fatalf("Join() with an empty notify: %v", err)
	}
	want := store.Member{Channel: "repo", Name: "alice", JoinedAt: baseTime, SeenAt: baseTime}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Join() with an empty notify, -want +got:\n%s", diff)
	}
}

func TestMembers(t *testing.T) {
	s, clock := newStore(t)
	ctx := t.Context()

	if _, err := s.Join(ctx, store.JoinRequest{Channel: "repo", Member: "carol"}); err != nil {
		t.Fatalf("Join(): %v", err)
	}
	clock.advance(time.Second)
	if _, err := s.Join(ctx, store.JoinRequest{Channel: "repo", Member: "alice", Notify: new("cm send alice")}); err != nil {
		t.Fatalf("Join(): %v", err)
	}
	clock.advance(time.Second)
	if _, err := s.Join(ctx, store.JoinRequest{Channel: "other", Member: "bob"}); err != nil {
		t.Fatalf("Join(): %v", err)
	}

	got, err := s.Members(ctx, "repo")
	if err != nil {
		t.Fatalf("Members(): %v", err)
	}
	want := []store.Member{
		{Channel: "repo", Name: "alice", Notify: "cm send alice", JoinedAt: baseTime.Add(time.Second), SeenAt: baseTime.Add(time.Second)},
		{Channel: "repo", Name: "carol", JoinedAt: baseTime, SeenAt: baseTime},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Members(), -want +got:\n%s", diff)
	}
}

func TestMembersOfAnUnusedChannelIsEmptyRatherThanAnError(t *testing.T) {
	s, _ := newStore(t)

	got, err := s.Members(t.Context(), "never-used")
	if err != nil {
		t.Fatalf("Members(): %v", err)
	}
	if diff := cmp.Diff([]store.Member(nil), got); diff != "" {
		t.Errorf("Members() of an unused channel, -want +got:\n%s", diff)
	}
	// Asking about a channel must not create it, or a typo in one command invents a channel every
	// later command has to be read past.
	if _, ok, err := s.Channel(t.Context(), "never-used"); err != nil || ok {
		t.Errorf("Channel() after Members() = ok %v, err %v, want ok false, err nil", ok, err)
	}
}

func TestJoinRejectsAnEmptyChannelOrMember(t *testing.T) {
	s, _ := newStore(t)

	for _, tc := range []struct {
		name string
		req  store.JoinRequest
		want error
	}{
		{name: "no channel", req: store.JoinRequest{Member: "alice"}, want: store.ErrNoChannel},
		{name: "no member", req: store.JoinRequest{Channel: "repo"}, want: store.ErrNoMember},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Join(t.Context(), tc.req)
			if !errors.Is(err, tc.want) {
				t.Errorf("Join(%+v) error = %v, want %v", tc.req, err, tc.want)
			}
			if diff := cmp.Diff(store.Member{}, got); diff != "" {
				t.Errorf("Join(%+v), -want +got:\n%s", tc.req, diff)
			}
		})
	}
}

// TestLeave is the counterpart to every operation that puts a name in the roster. What has to survive it is
// the record: a member's messages carry their author as a string, so leaving takes the participant and not
// what they said.
func TestLeave(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()

	if _, err := s.Post(ctx, store.PostRequest{Channel: "repo", Thread: "parser-panic", Author: "alice", Body: "the token loop"}); err != nil {
		t.Fatalf("Post(): %v", err)
	}
	if _, err := s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "bob", Advance: true}); err != nil {
		t.Fatalf("Read(): %v", err)
	}

	got, err := s.Leave(ctx, store.LeaveRequest{Channel: "repo", Member: "bob"})
	if err != nil {
		t.Fatalf("Leave(): %v", err)
	}
	// Cursors is what it keeps rather than what it takes, which is the whole of the reversal above.
	want := store.LeaveResult{Channel: "repo", Member: "bob", Left: true, Cursors: 1, Claims: []string{}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Leave(), -want +got:\n%s", diff)
	}
	// That the cursor survived is asserted by TestLeavingKeepsWhatWasRead, which comes back as bob and finds
	// nothing unread. Reading here would recreate the row this test just watched go.

	members, err := s.Members(ctx, "repo")
	if err != nil {
		t.Fatalf("Members(): %v", err)
	}
	wantMembers := []store.Member{{Channel: "repo", Name: "alice", Posts: 1, JoinedAt: baseTime, SeenAt: baseTime}}
	if diff := cmp.Diff(wantMembers, members); diff != "" {
		t.Errorf("the roster after leaving, -want +got:\n%s", diff)
	}

	// The message is still there, and still says who wrote it.
	messages, err := s.Messages(ctx, store.MessagesRequest{Channel: "repo", Thread: "parser-panic"})
	if err != nil {
		t.Fatalf("Messages(): %v", err)
	}
	wantMessages := []store.Message{{
		Seq: 1, Number: 1, Channel: "repo", Thread: "parser-panic", Author: "alice",
		Body: "the token loop", CreatedAt: baseTime,
	}}
	if diff := cmp.Diff(wantMessages, messages); diff != "" {
		t.Errorf("the record after leaving, -want +got:\n%s", diff)
	}
}

// TestLeavingKeepsWhatWasRead is a reversal. Leaving deleted cursors so that a name reused by a different
// session could not inherit somebody else's read state, which cannot happen: a name is a session id. What does
// happen is SessionEnd firing with reason "resume" when a session is only switched away from, and on a real
// channel that cost four messages read and answered eleven hours earlier.
func TestLeavingKeepsWhatWasRead(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()

	if _, err := s.Post(ctx, store.PostRequest{Channel: "repo", Thread: "parser-panic", Author: "alice", Body: "the token loop"}); err != nil {
		t.Fatalf("Post(): %v", err)
	}
	if _, err := s.Ack(ctx, store.AckRequest{Channel: "repo", Member: "bob"}); err != nil {
		t.Fatalf("Ack(): %v", err)
	}
	if _, err := s.Leave(ctx, store.LeaveRequest{Channel: "repo", Member: "bob"}); err != nil {
		t.Fatalf("Leave(): %v", err)
	}

	back, err := s.Join(ctx, store.JoinRequest{Channel: "repo", Member: "bob"})
	if err != nil {
		t.Fatalf("Join(): %v", err)
	}
	want := store.Member{
		Channel: "repo", Name: "bob",
		JoinedAt: baseTime, SeenAt: baseTime,
	}
	if diff := cmp.Diff(want, back); diff != "" {
		t.Errorf("coming back after leaving, -want +got:\n%s", diff)
	}
}

// TestLeavingWithAClaimIsRefused: a claim whose holder is not in the roster is worse than one held by somebody
// idle, because there is nobody left to ask and nothing says whether the work was finished.
func TestLeavingWithAClaimIsRefused(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	for _, thread := range []string{"parser-panic", "docs-rewrite"} {
		if _, err := s.Claim(ctx, store.ClaimRequest{Channel: "repo", Thread: thread, Holder: "bob"}); err != nil {
			t.Fatalf("Claim(): %v", err)
		}
	}

	got, err := s.Leave(ctx, store.LeaveRequest{Channel: "repo", Member: "bob"})
	if err != nil {
		t.Fatalf("Leave(): %v", err)
	}
	// Both threads are named, since what to do about them is the decision being handed back.
	want := store.LeaveResult{
		Channel: "repo", Member: "bob", Left: false, Cursors: 0,
		Claims: []string{"docs-rewrite", "parser-panic"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Leave() holding claims, -want +got:\n%s", diff)
	}
	if members, err := s.Members(ctx, "repo"); err != nil || len(members) != 1 {
		t.Fatalf("the roster after a refused leave = %+v, err %v, want bob still there", members, err)
	}

	forced, err := s.Leave(ctx, store.LeaveRequest{Channel: "repo", Member: "bob", Force: true})
	if err != nil {
		t.Fatalf("Leave(force): %v", err)
	}
	wantForced := want
	wantForced.Left = true
	if diff := cmp.Diff(wantForced, forced); diff != "" {
		t.Errorf("Leave(force), -want +got:\n%s", diff)
	}
	claims, err := s.Claims(ctx, "repo")
	if err != nil {
		t.Fatalf("Claims(): %v", err)
	}
	if len(claims) != 0 {
		t.Errorf("claims after a forced leave = %+v, want none", claims)
	}
}

// TestLeavingSomethingThatIsNotThere has to be quiet rather than an error, because the caller is a hook at
// session end: a session that never touched the channel still fires it, and a hook that fails on the normal
// case gets removed.
func TestLeavingSomethingThatIsNotThere(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	if _, err := s.Post(ctx, store.PostRequest{Channel: "repo", Thread: "t", Author: "alice", Body: "hi"}); err != nil {
		t.Fatalf("Post(): %v", err)
	}

	for _, tc := range []struct {
		name string
		req  store.LeaveRequest
		want store.LeaveResult
	}{
		{
			name: "a member nobody has heard of",
			req:  store.LeaveRequest{Channel: "repo", Member: "nobody"},
			want: store.LeaveResult{Channel: "repo", Member: "nobody", Claims: []string{}},
		},
		{
			name: "a channel nobody has used",
			req:  store.LeaveRequest{Channel: "never-used", Member: "bob"},
			want: store.LeaveResult{Channel: "never-used", Member: "bob", Claims: []string{}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Leave(ctx, tc.req)
			if err != nil {
				t.Fatalf("Leave(): %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Leave(), -want +got:\n%s", diff)
			}
		})
	}
	// And asking must not create the channel, the same as every other read.
	if _, ok, err := s.Channel(ctx, "never-used"); err != nil || ok {
		t.Errorf("Channel() after Leave() = ok %v, err %v, want ok false", ok, err)
	}
}

// TestLeaveDryRun is what the confirmation in the view is built on, so asking has to carry no risk of being
// the removal.
func TestLeaveDryRun(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	if _, err := s.Post(ctx, store.PostRequest{Channel: "repo", Thread: "t", Author: "alice", Body: "hi"}); err != nil {
		t.Fatalf("Post(): %v", err)
	}
	if _, err := s.Ack(ctx, store.AckRequest{Channel: "repo", Member: "bob"}); err != nil {
		t.Fatalf("Ack(): %v", err)
	}
	if _, err := s.Claim(ctx, store.ClaimRequest{Channel: "repo", Thread: "t", Holder: "bob"}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}

	got, err := s.Leave(ctx, store.LeaveRequest{Channel: "repo", Member: "bob", DryRun: true, Force: true})
	if err != nil {
		t.Fatalf("Leave(dry run): %v", err)
	}
	want := store.LeaveResult{Channel: "repo", Member: "bob", Left: false, Cursors: 1, Claims: []string{"t"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Leave(dry run), -want +got:\n%s", diff)
	}
	if members, err := s.Members(ctx, "repo"); err != nil || len(members) != 2 {
		t.Errorf("the roster after a dry run = %+v, err %v, want both still there", members, err)
	}
}

func TestLeaveRejectsAnEmptyChannelOrMember(t *testing.T) {
	s, _ := newStore(t)

	for _, tc := range []struct {
		name string
		req  store.LeaveRequest
		want error
	}{
		{name: "no channel", req: store.LeaveRequest{Member: "alice"}, want: store.ErrNoChannel},
		{name: "no member", req: store.LeaveRequest{Channel: "repo"}, want: store.ErrNoMember},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Leave(t.Context(), tc.req)
			if !errors.Is(err, tc.want) {
				t.Errorf("Leave(%+v) error = %v, want %v", tc.req, err, tc.want)
			}
			if diff := cmp.Diff(store.LeaveResult{}, got); diff != "" {
				t.Errorf("Leave(%+v), -want +got:\n%s", tc.req, diff)
			}
		})
	}
}

// TestJoinDescribesTheMember is the answer to "who is claude-3fb7e7b0": a name is a session id, so the roster
// carries one line the member writes about itself.
func TestJoinDescribesTheMember(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()

	got, err := s.Join(ctx, store.JoinRequest{
		Channel:     "repo",
		Member:      "claude-3fb7e7b0",
		Description: new("refactoring the lexer's bounds checks"),
	})
	if err != nil {
		t.Fatalf("Join(): %v", err)
	}
	want := store.Member{
		Channel:     "repo",
		Name:        "claude-3fb7e7b0",
		Description: "refactoring the lexer's bounds checks",
		JoinedAt:    baseTime,
		SeenAt:      baseTime,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Join() with a description, -want +got:\n%s", diff)
	}

	// Every other operation records the member too, and none of them knows what it is doing, so none of them
	// may wipe what it said.
	if _, err := s.Post(ctx, store.PostRequest{
		Channel: "repo", Thread: "lexer-bounds", Author: "claude-3fb7e7b0", Body: "found it",
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}
	if _, err := s.Ack(ctx, store.AckRequest{Channel: "repo", Member: "claude-3fb7e7b0"}); err != nil {
		t.Fatalf("Ack(): %v", err)
	}
	after, err := s.Members(ctx, "repo")
	if err != nil {
		t.Fatalf("Members(): %v", err)
	}
	if len(after) != 1 || after[0].Description != want.Description {
		t.Errorf("the roster after acting = %+v, want the description kept", after)
	}

	// Rejoining without one leaves it alone, and an empty one clears it: the same two decisions as the nudge
	// command, for the same reason.
	kept, err := s.Join(ctx, store.JoinRequest{Channel: "repo", Member: "claude-3fb7e7b0"})
	if err != nil {
		t.Fatalf("Join(): %v", err)
	}
	if kept.Description != want.Description {
		t.Errorf("description after a rejoin = %q, want it kept", kept.Description)
	}
	cleared, err := s.Join(ctx, store.JoinRequest{
		Channel: "repo", Member: "claude-3fb7e7b0", Description: new(""),
	})
	if err != nil {
		t.Fatalf("Join(): %v", err)
	}
	if cleared.Description != "" {
		t.Errorf("description after an empty one = %q, want it cleared", cleared.Description)
	}
}
