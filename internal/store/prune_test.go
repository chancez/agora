package store_test

import (
	"testing"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

// prune sweeps everything last heard from before now, which the test clock makes exact.
func prune(t *testing.T, s *store.Store, member string, before time.Time, dryRun bool) store.PruneResult {
	t.Helper()
	got, err := s.Prune(t.Context(), store.PruneRequest{
		Channel: "repo", Member: member, Before: before, DryRun: dryRun,
	})
	if err != nil {
		t.Fatalf("Prune(): %v", err)
	}
	return got
}

// TestPruneTakesTheStaleAndLeavesTheRest is the sweep for sessions that ended without saying so, which is most
// of them: SessionEnd fires on a clean end, and a killed terminal fires nothing.
func TestPruneTakesTheStaleAndLeavesTheRest(t *testing.T) {
	s, clock := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "gone", "parser-panic", "the token loop")
	clock.advance(time.Hour)
	post(t, s, "repo", "here", "parser-panic", "still working")

	got := prune(t, s, "sweeper", clock.Now().Add(-time.Minute), false)
	want := store.PruneResult{
		Channel: "repo",
		Pruned:  []string{"gone"},
		Held:    map[string][]string{},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Prune(), -want +got:\n%s", diff)
	}

	members, err := s.Members(ctx, "repo")
	if err != nil {
		t.Fatalf("Members(): %v", err)
	}
	names := make([]string, 0, len(members))
	for _, m := range members {
		names = append(names, m.Name)
	}
	// The caller is in the roster too, because a sweep is an action like any other.
	if diff := cmp.Diff([]string{"here", "sweeper"}, names); diff != "" {
		t.Errorf("the roster after a sweep, -want +got:\n%s", diff)
	}
}

// TestPruneLeavesAClaimHolder is the one part of a wrong removal that cannot be taken back. A stale claim holder
// is exactly the case that looks dead and is not: an agent waiting on its user is idle and still owns its work.
func TestPruneLeavesAClaimHolder(t *testing.T) {
	s, clock := newStore(t)
	ctx := t.Context()
	if _, err := s.Claim(ctx, store.ClaimRequest{
		Channel: "repo", Thread: "parser-panic", Holder: "holder", Note: "root cause is in the token loop",
	}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}
	clock.advance(time.Hour)

	got := prune(t, s, "sweeper", clock.Now().Add(-time.Minute), false)
	want := store.PruneResult{
		Channel: "repo",
		Pruned:  []string{},
		// Named with what they hold, since the point of leaving them is that somebody reads why.
		Held: map[string][]string{"holder": {"parser-panic"}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Prune() with a claim held, -want +got:\n%s", diff)
	}

	claims, err := s.Claims(ctx, "repo")
	if err != nil {
		t.Fatalf("Claims(): %v", err)
	}
	if len(claims) != 1 || claims[0].Holder != "holder" {
		t.Errorf("claims after a sweep = %+v, want the one it left alone", claims)
	}
}

// TestPruneNeverTakesTheCaller is the member the cutoff cannot be right about: whoever is running the sweep is
// here by definition, however long ago its clock says it last spoke.
func TestPruneNeverTakesTheCaller(t *testing.T) {
	s, clock := newStore(t)
	post(t, s, "repo", "sweeper", "parser-panic", "a while ago")
	clock.advance(time.Hour)

	got := prune(t, s, "sweeper", clock.Now().Add(-time.Minute), false)
	if len(got.Pruned) != 0 {
		t.Errorf("Prune() removed %v, want nothing: the caller is not gone", got.Pruned)
	}
	if left := threads(t, s, "sweeper", false); len(left) != 1 {
		t.Errorf("threads for the caller = %+v, want the channel intact", left)
	}
}

// TestPruneKeepsWhatWasRead is what makes a wrong sweep cheap. Cursors survive, so a member pruned by mistake
// comes back on its next action rather than re-reading the channel from the beginning.
func TestPruneKeepsWhatWasRead(t *testing.T) {
	s, clock := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "alice", "parser-panic", "the token loop")
	if _, err := s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "gone", Advance: true}); err != nil {
		t.Fatalf("Read(): %v", err)
	}
	clock.advance(time.Hour)

	// alice is stale too, having posted at the same time, so both go: the point here is what survives them.
	if got := prune(t, s, "sweeper", clock.Now().Add(-time.Minute), false); len(got.Pruned) != 2 {
		t.Fatalf("Prune() removed %v, want both members that had gone quiet", got.Pruned)
	}
	// Back as the same name, with nothing unread: the read state was never the roster's to lose.
	if left := threads(t, s, "gone", true); len(left) != 0 {
		t.Errorf("unread for a returning member = %+v, want nothing", left)
	}
}

func TestPruneDryRunChangesNothing(t *testing.T) {
	s, clock := newStore(t)
	post(t, s, "repo", "gone", "parser-panic", "the token loop")
	clock.advance(time.Hour)

	first := prune(t, s, "sweeper", clock.Now().Add(-time.Minute), true)
	if diff := cmp.Diff([]string{"gone"}, first.Pruned); diff != "" {
		t.Errorf("Prune(dry run), -want +got:\n%s", diff)
	}
	// Still there, so the same run reports the same thing rather than nothing.
	second := prune(t, s, "sweeper", clock.Now().Add(-time.Minute), true)
	if diff := cmp.Diff([]string{"gone"}, second.Pruned); diff != "" {
		t.Errorf("Prune(dry run) twice, -want +got:\n%s", diff)
	}
}

func TestPruneValidates(t *testing.T) {
	s, _ := newStore(t)
	for _, tc := range []struct {
		name string
		req  store.PruneRequest
		want error
	}{
		{name: "no channel", req: store.PruneRequest{Member: "sweeper"}, want: store.ErrNoChannel},
		{name: "no member", req: store.PruneRequest{Channel: "repo"}, want: store.ErrNoMember},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Prune(t.Context(), tc.req); err != tc.want {
				t.Errorf("Prune(%+v) = %v, want %v", tc.req, err, tc.want)
			}
		})
	}
}
