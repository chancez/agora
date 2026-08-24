package store_test

import (
	"errors"
	"testing"

	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

// TestDeleteChannelUnused is the case it exists for: a channel a command created in some repository, with a
// member row and nothing else, which nothing else in agora can take out of the database.
func TestDeleteChannelUnused(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	post(t, s, "keep", "alice", "parser-panic", "the token loop")
	// Reading a channel is what puts you in its roster, so an unused channel still has a member.
	if _, err := s.Join(ctx, store.JoinRequest{Channel: "unused", Member: "alice"}); err != nil {
		t.Fatalf("Join(): %v", err)
	}

	got, err := s.DeleteChannel(ctx, store.DeleteChannelRequest{Channel: "unused"})
	if err != nil {
		t.Fatalf("DeleteChannel(): %v", err)
	}
	want := store.DeleteChannelResult{Channel: "unused", Deleted: true, Existed: true, Members: 1}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DeleteChannel(), -want +got:\n%s", diff)
	}

	// Gone, and the channel beside it untouched.
	if _, ok, err := s.Channel(ctx, "unused"); err != nil || ok {
		t.Errorf("Channel() after deleting = ok %v, err %v, want ok false", ok, err)
	}
	left, err := s.Channels(ctx, "bob")
	if err != nil {
		t.Fatalf("Channels(): %v", err)
	}
	wantLeft := []store.Channel{{ID: 1, Key: "keep", CreatedAt: baseTime, Threads: 1, UnreadThreads: 1}}
	if diff := cmp.Diff(wantLeft, left); diff != "" {
		t.Errorf("Channels() after deleting, -want +got:\n%s", diff)
	}
}

// TestDeleteChannelRefusesOneInUse is the counterpart: a channel with the record in it is the whole history of
// a repository, so what stops a thread deletion has to stop this one harder.
func TestDeleteChannelRefusesOneInUse(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "alice", "parser-panic", "the token loop")
	if _, err := s.Claim(ctx, store.ClaimRequest{
		Channel: "repo", Thread: "parser-panic", Holder: "alice", Note: "fixing the token loop",
	}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}
	// A second member with a cursor, so the counts have something to be wrong about.
	if _, err := s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "bob", Advance: true}); err != nil {
		t.Fatalf("Read(): %v", err)
	}

	want := store.DeleteChannelResult{
		Channel: "repo", Existed: true, Threads: 1, Messages: 1, Members: 2, Cursors: 1,
		Claims: []store.Claim{{
			Channel: "repo", Thread: "parser-panic", Holder: "alice", Note: "fixing the token loop",
			CreatedAt: baseTime,
		}},
	}
	got, err := s.DeleteChannel(ctx, store.DeleteChannelRequest{Channel: "repo"})
	if err != nil {
		t.Fatalf("DeleteChannel(): %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DeleteChannel() of a channel in use, -want +got:\n%s", diff)
	}
	if _, ok, err := s.Channel(ctx, "repo"); err != nil || !ok {
		t.Fatalf("the channel went anyway: ok %v, err %v", ok, err)
	}

	forced, err := s.DeleteChannel(ctx, store.DeleteChannelRequest{Channel: "repo", Force: true})
	if err != nil {
		t.Fatalf("DeleteChannel(force): %v", err)
	}
	want.Deleted = true
	if diff := cmp.Diff(want, forced); diff != "" {
		t.Errorf("DeleteChannel(force), -want +got:\n%s", diff)
	}
	if _, ok, err := s.Channel(ctx, "repo"); err != nil || ok {
		t.Errorf("Channel() after forcing = ok %v, err %v, want ok false", ok, err)
	}
}

// TestDeleteChannelClaimAloneStopsIt covers the half that messages do not: a claim with nothing posted under it
// yet is somebody's ownership of work, and that gap is exactly when they have not written anything down.
func TestDeleteChannelClaimAloneStopsIt(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	if _, err := s.Claim(ctx, store.ClaimRequest{Channel: "repo", Thread: "parser-panic", Holder: "alice"}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}

	got, err := s.DeleteChannel(ctx, store.DeleteChannelRequest{Channel: "repo"})
	if err != nil {
		t.Fatalf("DeleteChannel(): %v", err)
	}
	want := store.DeleteChannelResult{
		Channel: "repo", Existed: true, Threads: 1, Members: 1,
		Claims: []store.Claim{{Channel: "repo", Thread: "parser-panic", Holder: "alice", CreatedAt: baseTime}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DeleteChannel() of a claimed channel, -want +got:\n%s", diff)
	}
}

// TestDeleteChannelDryRunTouchesNothing is what a confirmation is built on: saying what would go cannot carry
// any chance of being the deletion.
func TestDeleteChannelDryRunTouchesNothing(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	posted := post(t, s, "repo", "alice", "parser-panic", "the token loop")

	// Force as well as DryRun, so nothing but DryRun can stop it: with Force absent the refusal would stop
	// this deletion anyway and the test would pass with DryRun ignored entirely.
	got, err := s.DeleteChannel(ctx, store.DeleteChannelRequest{Channel: "repo", Force: true, DryRun: true})
	if err != nil {
		t.Fatalf("DeleteChannel(): %v", err)
	}
	want := store.DeleteChannelResult{Channel: "repo", Existed: true, Threads: 1, Messages: 1, Members: 1}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DeleteChannel(dry run), -want +got:\n%s", diff)
	}
	messages, err := s.Messages(ctx, store.MessagesRequest{Channel: "repo"})
	if err != nil {
		t.Fatalf("Messages(): %v", err)
	}
	if diff := cmp.Diff([]store.Message{posted}, messages); diff != "" {
		t.Errorf("a dry run changed the channel, -want +got:\n%s", diff)
	}
}

// TestDeleteChannelThatNeverExisted separates "already gone" from "refused". Both report zero of everything, so
// Existed is the only thing that can tell them apart, and a caller that cannot tell prints the wrong sentence.
func TestDeleteChannelThatNeverExisted(t *testing.T) {
	s, _ := newStore(t)

	got, err := s.DeleteChannel(t.Context(), store.DeleteChannelRequest{Channel: "nothing-here"})
	if err != nil {
		t.Fatalf("DeleteChannel(): %v", err)
	}
	want := store.DeleteChannelResult{Channel: "nothing-here"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DeleteChannel() of an unused key, -want +got:\n%s", diff)
	}
	// And asking did not create it, which is the trap in a channel key that is created by every command that
	// names one: a delete that creates what it failed to find leaves the list longer than it started.
	channels, err := s.Channels(t.Context(), "alice")
	if err != nil {
		t.Fatalf("Channels(): %v", err)
	}
	if len(channels) != 0 {
		t.Errorf("Channels() = %+v, want none created by asking about one", channels)
	}
}

func TestDeleteChannelValidates(t *testing.T) {
	s, _ := newStore(t)

	if _, err := s.DeleteChannel(t.Context(), store.DeleteChannelRequest{}); !errors.Is(err, store.ErrNoChannel) {
		t.Errorf("DeleteChannel() with no channel = %v, want %v", err, store.ErrNoChannel)
	}
}
