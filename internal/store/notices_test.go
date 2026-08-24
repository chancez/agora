package store_test

import (
	"testing"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

// TestNoticeSaysEachThingOnce is what the guard needs to stop repeating itself. As a permission decision it had
// nothing to remember, because it was answered every time; as a notice, the same three lines on every edit under
// one claim is the noise that got the layer switched off.
func TestNoticeSaysEachThingOnce(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	claim := mustClaim(t, s, "parser-panic", "alice", "the token loop")

	first, err := s.Notice(ctx, store.NoticeRequest{Channel: "repo", Member: "bob", Claims: []store.Claim{claim}})
	if err != nil {
		t.Fatalf("Notice(): %v", err)
	}
	want := store.NoticeResult{Channel: "repo", Member: "bob", New: []store.Claim{claim}}
	if diff := cmp.Diff(want, first); diff != "" {
		t.Errorf("Notice(), -want +got:\n%s", diff)
	}

	again, err := s.Notice(ctx, store.NoticeRequest{Channel: "repo", Member: "bob", Claims: []store.Claim{claim}})
	if err != nil {
		t.Fatalf("Notice() again: %v", err)
	}
	if diff := cmp.Diff(store.NoticeResult{Channel: "repo", Member: "bob", New: []store.Claim{}}, again); diff != "" {
		t.Errorf("Notice() twice about one claim, -want +got:\n%s", diff)
	}

	// Per member: what bob has heard says nothing about carol.
	carol, err := s.Notice(ctx, store.NoticeRequest{Channel: "repo", Member: "carol", Claims: []store.Claim{claim}})
	if err != nil {
		t.Fatalf("Notice() for carol: %v", err)
	}
	if len(carol.New) != 1 {
		t.Errorf("carol was told nothing: %+v", carol)
	}
}

// TestNoticeSpeaksAgainForNewWork is the other half of saying it once: a claim that changed hands, or was
// released and taken again, is a different piece of work, and staying silent about it would make the record and
// the notice disagree.
func TestNoticeSpeaksAgainForNewWork(t *testing.T) {
	for _, tc := range []struct {
		name   string
		holder string
		// wait separates the two claims in time. The handover case deliberately does not, so that the only
		// thing telling the two claims apart is the holder: with the clock moved as well, a check on the
		// holder could be missing entirely and this would still pass.
		wait time.Duration
	}{
		{name: "taken again by the same holder", holder: "alice", wait: time.Minute},
		{name: "taken by somebody else", holder: "dave"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, clock := newStore(t)
			ctx := t.Context()
			first := mustClaim(t, s, "parser-panic", "alice", "the token loop")
			if _, err := s.Notice(ctx, store.NoticeRequest{
				Channel: "repo", Member: "bob", Claims: []store.Claim{first},
			}); err != nil {
				t.Fatalf("Notice(): %v", err)
			}

			if _, err := s.Release(ctx, store.ReleaseRequest{
				Channel: "repo", Thread: "parser-panic", Holder: "alice",
			}); err != nil {
				t.Fatalf("Release(): %v", err)
			}
			clock.advance(tc.wait)
			second := mustClaim(t, s, "parser-panic", tc.holder, "second go, the lexer this time")

			got, err := s.Notice(ctx, store.NoticeRequest{
				Channel: "repo", Member: "bob", Claims: []store.Claim{second},
			})
			if err != nil {
				t.Fatalf("Notice() after the claim moved: %v", err)
			}
			if diff := cmp.Diff([]store.Claim{second}, got.New); diff != "" {
				t.Errorf("Notice() about the new claim, -want +got:\n%s", diff)
			}
		})
	}
}

// TestNoticeDryRunRecordsNothing keeps looking separate from telling, the same split read and read --advance
// have: a caller that has not delivered its output must not have consumed it.
func TestNoticeDryRunRecordsNothing(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	claim := mustClaim(t, s, "parser-panic", "alice", "the token loop")

	for i := range 2 {
		got, err := s.Notice(ctx, store.NoticeRequest{
			Channel: "repo", Member: "bob", Claims: []store.Claim{claim}, DryRun: true,
		})
		if err != nil {
			t.Fatalf("Notice(dry run) %d: %v", i, err)
		}
		if len(got.New) != 1 {
			t.Errorf("dry run %d reported %+v, want the claim every time", i, got.New)
		}
	}
}

func TestNoticeValidates(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	for _, tc := range []struct {
		name string
		req  store.NoticeRequest
		want error
	}{
		{name: "no channel", req: store.NoticeRequest{Member: "bob"}, want: store.ErrNoChannel},
		{name: "no member", req: store.NoticeRequest{Channel: "repo"}, want: store.ErrNoMember},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Notice(ctx, tc.req); err != tc.want {
				t.Errorf("Notice(%+v) = %v, want %v", tc.req, err, tc.want)
			}
		})
	}

	// Nothing to say is not an error, and it must not create the channel: this sits in the path of every
	// matching edit.
	got, err := s.Notice(ctx, store.NoticeRequest{Channel: "never-used", Member: "bob"})
	if err != nil {
		t.Fatalf("Notice() with no claims: %v", err)
	}
	if diff := cmp.Diff(store.NoticeResult{Channel: "never-used", Member: "bob", New: []store.Claim{}}, got); diff != "" {
		t.Errorf("Notice() with no claims, -want +got:\n%s", diff)
	}
}

func mustClaim(t *testing.T, s *store.Store, thread, holder, note string) store.Claim {
	t.Helper()
	result, err := s.Claim(t.Context(), store.ClaimRequest{
		Channel: "repo", Thread: thread, Holder: holder, Note: note, Paths: []string{"parser.go"},
	})
	if err != nil {
		t.Fatalf("Claim(%q by %q): %v", thread, holder, err)
	}
	if !result.Granted {
		t.Fatalf("Claim(%q by %q) was refused, held by %q", thread, holder, result.Claim.Holder)
	}
	return result.Claim
}
