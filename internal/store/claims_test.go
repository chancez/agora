package store_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

func TestClaim(t *testing.T) {
	s, _ := newStore(t)

	got, err := s.Claim(t.Context(), store.ClaimRequest{
		Channel: "repo",
		Thread:  "parser-panic",
		Holder:  "alice",
		Note:    "root cause is in the token loop, do not patch the symptom",
		Paths:   []string{"parser.go", "internal/lex/**"},
	})
	if err != nil {
		t.Fatalf("Claim(): %v", err)
	}
	want := store.ClaimResult{
		Granted: true,
		Claim: store.Claim{
			Channel:   "repo",
			Thread:    "parser-panic",
			Holder:    "alice",
			Note:      "root cause is in the token loop, do not patch the symptom",
			Paths:     []string{"parser.go", "internal/lex/**"},
			CreatedAt: baseTime,
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Claim(), -want +got:\n%s", diff)
	}
}

func TestClaimLostReportsTheHolderAndTheirNote(t *testing.T) {
	s, clock := newStore(t)
	ctx := t.Context()
	if _, err := s.Claim(ctx, store.ClaimRequest{
		Channel: "repo",
		Thread:  "parser-panic",
		Holder:  "alice",
		Note:    "root cause is in the token loop, do not patch the symptom",
		Paths:   []string{"parser.go"},
	}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}

	clock.advance(time.Minute)
	got, err := s.Claim(ctx, store.ClaimRequest{
		Channel: "repo",
		Thread:  "parser-panic",
		Holder:  "bob",
		Note:    "adding a nil check",
		Paths:   []string{"parser.go"},
	})
	if err != nil {
		t.Fatalf("Claim() by a second holder: %v", err)
	}
	// A losing claim exists to name the holder and their note, because that output is what stops
	// duplicate work. Losing is a result rather than an error, and it must not disturb what stands.
	want := store.ClaimResult{
		Granted: false,
		Claim: store.Claim{
			Channel:   "repo",
			Thread:    "parser-panic",
			Holder:    "alice",
			Note:      "root cause is in the token loop, do not patch the symptom",
			Paths:     []string{"parser.go"},
			CreatedAt: baseTime,
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Claim() by a second holder, -want +got:\n%s", diff)
	}
}

func TestClaimAgainByTheHolderUpdatesTheNoteAndKeepsTheAge(t *testing.T) {
	s, clock := newStore(t)
	ctx := t.Context()
	if _, err := s.Claim(ctx, store.ClaimRequest{
		Channel: "repo", Thread: "parser-panic", Holder: "alice", Note: "looking at it", Paths: []string{"parser.go"},
	}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}

	clock.advance(time.Minute)
	got, err := s.Claim(ctx, store.ClaimRequest{
		Channel: "repo", Thread: "parser-panic", Holder: "alice",
		Note:  "root cause is in the token loop",
		Paths: []string{"parser.go", "internal/lex/**"},
	})
	if err != nil {
		t.Fatalf("Claim() again: %v", err)
	}
	// An agent refining its own note is not a conflict. Failing here would teach it to treat a lost
	// claim as noise, which is the one output that has to be believed. The age is when the work was
	// taken, so a refresh must not reset it.
	want := store.ClaimResult{
		Granted: true,
		Claim: store.Claim{
			Channel:   "repo",
			Thread:    "parser-panic",
			Holder:    "alice",
			Note:      "root cause is in the token loop",
			Paths:     []string{"parser.go", "internal/lex/**"},
			CreatedAt: baseTime,
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Claim() again, -want +got:\n%s", diff)
	}
}

func TestClaimsAreScopedToOneChannelAndOrderedByTopic(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	for _, req := range []store.ClaimRequest{
		{Channel: "repo", Thread: "parser-panic", Holder: "alice", Paths: []string{"parser.go"}},
		{Channel: "repo", Thread: "lexer-bounds", Holder: "bob"},
		{Channel: "other-repo", Thread: "parser-panic", Holder: "carol"},
	} {
		if _, err := s.Claim(ctx, req); err != nil {
			t.Fatalf("Claim(%+v): %v", req, err)
		}
	}

	got, err := s.Claims(ctx, "repo")
	if err != nil {
		t.Fatalf("Claims(): %v", err)
	}
	want := []store.Claim{
		{Channel: "repo", Thread: "lexer-bounds", Holder: "bob", CreatedAt: baseTime},
		{Channel: "repo", Thread: "parser-panic", Holder: "alice", Paths: []string{"parser.go"}, CreatedAt: baseTime},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Claims(), -want +got:\n%s", diff)
	}
}

func TestRelease(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	if _, err := s.Claim(ctx, store.ClaimRequest{Channel: "repo", Thread: "parser-panic", Holder: "alice", Note: "on it"}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}

	got, err := s.Release(ctx, store.ReleaseRequest{Channel: "repo", Thread: "parser-panic", Holder: "alice"})
	if err != nil {
		t.Fatalf("Release(): %v", err)
	}
	want := store.ReleaseResult{
		Released: true,
		Claim: store.Claim{
			Channel: "repo", Thread: "parser-panic", Holder: "alice", Note: "on it", CreatedAt: baseTime,
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Release(), -want +got:\n%s", diff)
	}
	claims, err := s.Claims(ctx, "repo")
	if err != nil {
		t.Fatalf("Claims(): %v", err)
	}
	if diff := cmp.Diff([]store.Claim(nil), claims); diff != "" {
		t.Errorf("Claims() after Release(), -want +got:\n%s", diff)
	}
}

func TestReleaseRefusesSomeoneElsesClaimUnlessForced(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	if _, err := s.Claim(ctx, store.ClaimRequest{Channel: "repo", Thread: "parser-panic", Holder: "alice", Note: "on it"}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}
	held := store.Claim{Channel: "repo", Thread: "parser-panic", Holder: "alice", Note: "on it", CreatedAt: baseTime}

	// A claim held by a member that looks idle may belong to an agent waiting on its user.
	got, err := s.Release(ctx, store.ReleaseRequest{Channel: "repo", Thread: "parser-panic", Holder: "bob"})
	if err != nil {
		t.Fatalf("Release() by another member: %v", err)
	}
	if diff := cmp.Diff(store.ReleaseResult{Released: false, Claim: held}, got); diff != "" {
		t.Errorf("Release() by another member, -want +got:\n%s", diff)
	}

	got, err = s.Release(ctx, store.ReleaseRequest{Channel: "repo", Thread: "parser-panic", Holder: "bob", Force: true})
	if err != nil {
		t.Fatalf("Release() forced: %v", err)
	}
	if diff := cmp.Diff(store.ReleaseResult{Released: true, Claim: held}, got); diff != "" {
		t.Errorf("Release() forced, -want +got:\n%s", diff)
	}
}

func TestReleaseWhatNobodyHolds(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()

	for _, tc := range []struct {
		name    string
		channel string
	}{
		{name: "an unused channel", channel: "never-used"},
		{name: "an unclaimed topic", channel: "repo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Release(ctx, store.ReleaseRequest{Channel: tc.channel, Thread: "parser-panic", Holder: "alice"})
			if err != nil {
				t.Fatalf("Release(): %v", err)
			}
			if diff := cmp.Diff(store.ReleaseResult{}, got); diff != "" {
				t.Errorf("Release(), -want +got:\n%s", diff)
			}
		})
	}
}

func TestClaimAndReleaseValidate(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()

	t.Run("claim", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			req  store.ClaimRequest
			want error
		}{
			{name: "no channel", req: store.ClaimRequest{Thread: "t", Holder: "alice"}, want: store.ErrNoChannel},
			{name: "no thread", req: store.ClaimRequest{Channel: "repo", Holder: "alice"}, want: store.ErrNoThread},
			{name: "no holder", req: store.ClaimRequest{Channel: "repo", Thread: "t"}, want: store.ErrNoHolder},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, err := s.Claim(ctx, tc.req)
				if !errors.Is(err, tc.want) {
					t.Errorf("Claim(%+v) error = %v, want %v", tc.req, err, tc.want)
				}
				if diff := cmp.Diff(store.ClaimResult{}, got); diff != "" {
					t.Errorf("Claim(%+v), -want +got:\n%s", tc.req, diff)
				}
			})
		}
	})

	t.Run("release", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			req  store.ReleaseRequest
			want error
		}{
			{name: "no channel", req: store.ReleaseRequest{Thread: "t", Holder: "alice"}, want: store.ErrNoChannel},
			{name: "no thread", req: store.ReleaseRequest{Channel: "repo", Holder: "alice"}, want: store.ErrNoThread},
			{name: "no holder", req: store.ReleaseRequest{Channel: "repo", Thread: "t"}, want: store.ErrNoHolder},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, err := s.Release(ctx, tc.req)
				if !errors.Is(err, tc.want) {
					t.Errorf("Release(%+v) error = %v, want %v", tc.req, err, tc.want)
				}
				if diff := cmp.Diff(store.ReleaseResult{}, got); diff != "" {
					t.Errorf("Release(%+v), -want +got:\n%s", tc.req, diff)
				}
			})
		}
	})
}

// TestClaimRaceHasExactlyOneWinner is the measurement docs/design.md rests on, as a test. Ownership is
// the property agora exists to provide, and a sequential test proves nothing about it, so this is 20
// separate connections reaching for one topic at once. Exactly one winner, one row, no errors. Run
// with -race.
func TestClaimRaceHasExactlyOneWinner(t *testing.T) {
	const holders = 20
	path := filepath.Join(t.TempDir(), "agora.db")
	clock := newClock()

	// One store per holder, opened before the race so the only thing being raced is the claim.
	// Sharing a *Store would share a connection pool, which is not the situation agora is in:
	// participants are separate processes.
	stores := make([]*store.Store, holders)
	for i := range stores {
		stores[i] = openStore(t, path, clock)
	}

	results := make([]store.ClaimResult, holders)
	errs := make([]error, holders)
	var (
		start sync.WaitGroup
		done  sync.WaitGroup
	)
	start.Add(1)
	for i := range holders {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			results[i], errs[i] = stores[i].Claim(t.Context(), store.ClaimRequest{
				Channel: "repo",
				Thread:  "parser-panic",
				Holder:  fmt.Sprintf("agent-%02d", i),
				Note:    fmt.Sprintf("agent-%02d is on it", i),
				Paths:   []string{"parser.go"},
			})
		}()
	}
	start.Done()
	done.Wait()

	var winners []string
	for i, err := range errs {
		// No retries, no busy errors: the whole argument for sqlite over a directory of files is
		// that one statement decides this.
		if err != nil {
			t.Errorf("holder %d: Claim(): %v", i, err)
		}
		if results[i].Granted {
			winners = append(winners, results[i].Claim.Holder)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("Claim() granted to %v, want exactly one winner", winners)
	}
	winner := winners[0]

	// Every loser has to have been told the same truth, or the output that stops duplicate work is
	// worse than nothing.
	want := store.Claim{
		Channel:   "repo",
		Thread:    "parser-panic",
		Holder:    winner,
		Note:      winner + " is on it",
		Paths:     []string{"parser.go"},
		CreatedAt: baseTime,
	}
	for i, got := range results {
		if diff := cmp.Diff(want, got.Claim); diff != "" {
			t.Errorf("holder %d saw a different claim, -want +got:\n%s", i, diff)
		}
	}

	claims, err := stores[0].Claims(t.Context(), "repo")
	if err != nil {
		t.Fatalf("Claims(): %v", err)
	}
	if diff := cmp.Diff([]store.Claim{want}, claims); diff != "" {
		t.Errorf("Claims() after the race, -want +got:\n%s", diff)
	}
}
