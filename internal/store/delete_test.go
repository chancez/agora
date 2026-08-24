package store_test

import (
	"testing"

	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

func TestDelete(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "alice", "noise", "never mind")
	post(t, s, "repo", "alice", "noise", "nor this")
	post(t, s, "repo", "alice", "keep", "this one matters")
	if _, err := s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "bob", Advance: true}); err != nil {
		t.Fatalf("Read(): %v", err)
	}

	got, err := s.Delete(ctx, store.DeleteRequest{Channel: "repo", Thread: "noise", Member: "bob"})
	if err != nil {
		t.Fatalf("Delete(): %v", err)
	}
	want := store.DeleteResult{Channel: "repo", Thread: "noise", Deleted: true, Messages: 2, Cursors: 1}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Delete(), -want +got:\n%s", diff)
	}

	// The thread is gone, and the one beside it is untouched.
	left := threads(t, s, "bob", false)
	if len(left) != 1 || left[0].Name != "keep" {
		t.Errorf("threads after deleting = %+v, want only keep", left)
	}
	messages, err := s.Messages(ctx, store.MessagesRequest{Channel: "repo"})
	if err != nil {
		t.Fatalf("Messages(): %v", err)
	}
	if len(messages) != 1 || messages[0].Thread != "keep" {
		t.Errorf("messages after deleting = %+v, want only the kept thread's", messages)
	}

	// The cursor rows are gone too, which the reported count does not prove: that number is read before
	// anything is deleted. A cursor left behind would silently suppress the next thread of the same name.
	after, err := s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "bob", Thread: "noise"})
	if err != nil {
		t.Fatalf("Read(): %v", err)
	}
	if got := after.Cursors["noise"]; got != 0 {
		t.Errorf("bob's cursor on the deleted thread = %d, want no row at all", got)
	}
}

// TestDeleteDryRunTouchesNothing is what a confirmation is built on: it has to be able to say what would go
// without any chance of that being the deletion itself.
func TestDeleteDryRunTouchesNothing(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "alice", "noise", "never mind")
	// Claimed by the same member who is asking, so nothing but DryRun can stop this. The first version of
	// this test had alice's claim against bob's request, and passed because the claim check stopped the
	// deletion: it would have passed with DryRun ignored entirely.
	if _, err := s.Claim(ctx, store.ClaimRequest{Channel: "repo", Thread: "noise", Holder: "bob"}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}

	got, err := s.Delete(ctx, store.DeleteRequest{Channel: "repo", Thread: "noise", Member: "bob", DryRun: true})
	if err != nil {
		t.Fatalf("Delete(): %v", err)
	}
	want := store.DeleteResult{Channel: "repo", Thread: "noise", Messages: 1, Claim: "bob"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Delete(dry run), -want +got:\n%s", diff)
	}
	if left := threads(t, s, "bob", false); len(left) != 1 {
		t.Errorf("a dry run changed the channel: %+v", left)
	}
	claims, err := s.Claims(ctx, "repo")
	if err != nil {
		t.Fatalf("Claims(): %v", err)
	}
	if len(claims) != 1 {
		t.Errorf("a dry run removed the claim: %+v", claims)
	}
}

// TestDeleteRefusesAnotherMembersThread is stronger than the same rule on release. A release they can see and
// argue with; a deletion leaves them nothing to argue about.
func TestDeleteRefusesAnotherMembersThread(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "alice", "parser-panic", "the token loop")
	if _, err := s.Claim(ctx, store.ClaimRequest{Channel: "repo", Thread: "parser-panic", Holder: "alice"}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}

	got, err := s.Delete(ctx, store.DeleteRequest{Channel: "repo", Thread: "parser-panic", Member: "bob"})
	if err != nil {
		t.Fatalf("Delete(): %v", err)
	}
	want := store.DeleteResult{Channel: "repo", Thread: "parser-panic", Messages: 1, Claim: "alice"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Delete() of a claimed thread, -want +got:\n%s", diff)
	}
	if left := threads(t, s, "bob", false); len(left) != 1 {
		t.Errorf("the thread went anyway: %+v", left)
	}

	forced, err := s.Delete(ctx, store.DeleteRequest{
		Channel: "repo", Thread: "parser-panic", Member: "bob", Force: true,
	})
	if err != nil {
		t.Fatalf("Delete(force): %v", err)
	}
	if !forced.Deleted {
		t.Errorf("Delete(force) = %+v, want it deleted", forced)
	}
	if left := threads(t, s, "bob", false); len(left) != 0 {
		t.Errorf("threads after forcing = %+v, want none", left)
	}
}

func TestDeletingYourOwnClaimedThreadNeedsNoForce(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "alice", "mine", "a false start")
	if _, err := s.Claim(ctx, store.ClaimRequest{Channel: "repo", Thread: "mine", Holder: "alice"}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}

	got, err := s.Delete(ctx, store.DeleteRequest{Channel: "repo", Thread: "mine", Member: "alice"})
	if err != nil {
		t.Fatalf("Delete(): %v", err)
	}
	if !got.Deleted || got.Claim != "alice" {
		t.Errorf("Delete() of your own claimed thread = %+v, want it deleted and the claim named", got)
	}
}

func TestDeleteValidates(t *testing.T) {
	s, _ := newStore(t)
	for _, tc := range []struct {
		name string
		req  store.DeleteRequest
		want error
	}{
		{name: "no channel", req: store.DeleteRequest{Thread: "t", Member: "bob"}, want: store.ErrNoChannel},
		{name: "no thread", req: store.DeleteRequest{Channel: "repo", Member: "bob"}, want: store.ErrNoThread},
		{name: "no member", req: store.DeleteRequest{Channel: "repo", Thread: "t"}, want: store.ErrNoMember},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Delete(t.Context(), tc.req); err == nil {
				t.Errorf("Delete(%+v) succeeded, want %v", tc.req, tc.want)
			}
		})
	}
}
