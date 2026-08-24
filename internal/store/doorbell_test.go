package store_test

import (
	"sync"
	"testing"

	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

// TestDoorbellRingsForWhatIsAddressed is the whole filter, and the filter is the design: a wake costs a turn,
// so ringing on every post would spend ten turns on one finding and turn a ledger into a chat room.
func TestDoorbellRingsForWhatIsAddressed(t *testing.T) {
	for _, tc := range []struct {
		name string
		// setup posts the history and returns the message a wake should carry, nil for a case that must
		// stay quiet.
		setup func(t *testing.T, s *store.Store) *store.Message
	}{
		{
			name: "an answer in a thread alice opened",
			setup: func(t *testing.T, s *store.Store) *store.Message {
				mustPost(t, s, "parser-panic", "alice", "looking at the token loop")
				reply := mustPost(t, s, "parser-panic", "bob", "the same panic is in the lexer")
				return &reply
			},
		},
		{
			name: "a message that names alice",
			setup: func(t *testing.T, s *store.Store) *store.Message {
				named := mustPost(t, s, "docs-rewrite", "bob", "alice, does this break your claim?")
				return &named
			},
		},
		{
			name: "a thread alice holds the claim on but has not posted in",
			setup: func(t *testing.T, s *store.Store) *store.Message {
				mustClaim(t, s, "lexer", "alice", "rewriting the scanner")
				reply := mustPost(t, s, "lexer", "bob", "the scanner is also wrong for tabs")
				return &reply
			},
		},
		{
			name: "a thread alice has nothing to do with",
			setup: func(t *testing.T, s *store.Store) *store.Message {
				mustPost(t, s, "docs-rewrite", "bob", "starting on the setup guide")
				return nil
			},
		},
		{
			name: "alice's own message",
			setup: func(t *testing.T, s *store.Store) *store.Message {
				mustPost(t, s, "parser-panic", "alice", "looking at the token loop")
				return nil
			},
		},
		{
			name: "a thread alice muted",
			setup: func(t *testing.T, s *store.Store) *store.Message {
				mustPost(t, s, "parser-panic", "alice", "looking at the token loop")
				if _, err := s.Mute(t.Context(), store.MuteRequest{
					Channel: "repo", Member: "alice", Thread: "parser-panic",
				}); err != nil {
					t.Fatalf("Mute(): %v", err)
				}
				mustPost(t, s, "parser-panic", "bob", "still going in the lexer")
				return nil
			},
		},
		{
			name: "a message alice has already read",
			setup: func(t *testing.T, s *store.Store) *store.Message {
				mustPost(t, s, "parser-panic", "alice", "looking at the token loop")
				mustPost(t, s, "parser-panic", "bob", "the same panic is in the lexer")
				if _, err := s.Read(t.Context(), store.ReadRequest{
					Channel: "repo", Member: "alice", Advance: true,
				}); err != nil {
					t.Fatalf("Read(): %v", err)
				}
				return nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStore(t)
			ring := tc.setup(t, s)

			got, err := s.Doorbell(t.Context(), store.DoorbellRequest{Channel: "repo", Member: "alice"})
			if err != nil {
				t.Fatalf("Doorbell(): %v", err)
			}
			want := store.DoorbellResult{Channel: "repo", Member: "alice", Messages: []store.Message{}}
			if ring != nil {
				want.Messages = []store.Message{*ring}
				want.Rang = ring.Seq
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("Doorbell(), -want +got:\n%s", diff)
			}
		})
	}
}

// TestDoorbellRingsOncePerMessage is what stops a wake becoming a loop: an agent that read a wake and decided
// not to answer must not be woken about it again on the next turn, or two woken agents keep each other awake.
func TestDoorbellRingsOncePerMessage(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	mustPost(t, s, "parser-panic", "alice", "looking at the token loop")
	first := mustPost(t, s, "parser-panic", "bob", "the same panic is in the lexer")

	rang, err := s.Doorbell(ctx, store.DoorbellRequest{Channel: "repo", Member: "alice"})
	if err != nil {
		t.Fatalf("Doorbell(): %v", err)
	}
	want := store.DoorbellResult{
		Channel: "repo", Member: "alice", Messages: []store.Message{first}, Rang: first.Seq,
	}
	if diff := cmp.Diff(want, rang); diff != "" {
		t.Errorf("Doorbell(), -want +got:\n%s", diff)
	}

	again, err := s.Doorbell(ctx, store.DoorbellRequest{Channel: "repo", Member: "alice"})
	if err != nil {
		t.Fatalf("Doorbell() again: %v", err)
	}
	quiet := store.DoorbellResult{
		Channel: "repo", Member: "alice", Messages: []store.Message{}, Rang: first.Seq,
	}
	if diff := cmp.Diff(quiet, again); diff != "" {
		t.Errorf("Doorbell() twice for one message, -want +got:\n%s", diff)
	}

	// The next message is news, and the watermark is not an excuse to stay quiet about it.
	second := mustPost(t, s, "parser-panic", "bob", "and the fix has to cover both")
	third, err := s.Doorbell(ctx, store.DoorbellRequest{Channel: "repo", Member: "alice"})
	if err != nil {
		t.Fatalf("Doorbell() after a second message: %v", err)
	}
	want = store.DoorbellResult{
		Channel: "repo", Member: "alice", Messages: []store.Message{second}, Rang: second.Seq,
	}
	if diff := cmp.Diff(want, third); diff != "" {
		t.Errorf("Doorbell() after a second message, -want +got:\n%s", diff)
	}
}

// TestDoorbellLeavesUnreadAlone is why the watermark is not a cursor. A doorbell that marked what it rang
// about as read would answer a message by hiding it, and the agent it woke would find nothing waiting.
func TestDoorbellLeavesUnreadAlone(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	mustPost(t, s, "parser-panic", "alice", "looking at the token loop")
	reply := mustPost(t, s, "parser-panic", "bob", "the same panic is in the lexer")

	if _, err := s.Doorbell(ctx, store.DoorbellRequest{Channel: "repo", Member: "alice"}); err != nil {
		t.Fatalf("Doorbell(): %v", err)
	}
	got, err := s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "alice"})
	if err != nil {
		t.Fatalf("Read(): %v", err)
	}
	want := store.ReadResult{
		Channel:  "repo",
		Member:   "alice",
		Messages: []store.Message{reply},
		// Where alice's cursor stands, which is still at the start: the ring moved a watermark and not this.
		Cursors: map[string]int64{"parser-panic": 0},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Read() after a ring, -want +got:\n%s", diff)
	}
}

// TestDoorbellDryRunRecordsNothing keeps looking separate from ringing, the same split read and read --advance
// have. A caller that has not delivered a wake must not have consumed it.
func TestDoorbellDryRunRecordsNothing(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	mustPost(t, s, "parser-panic", "alice", "looking at the token loop")
	reply := mustPost(t, s, "parser-panic", "bob", "the same panic is in the lexer")

	want := store.DoorbellResult{
		Channel: "repo", Member: "alice", Messages: []store.Message{reply},
	}
	for i := range 2 {
		got, err := s.Doorbell(ctx, store.DoorbellRequest{
			Channel: "repo", Member: "alice", Waiter: "0001-1", DryRun: true,
		})
		if err != nil {
			t.Fatalf("Doorbell(dry run) %d: %v", i, err)
		}
		// Waiter comes back because the caller asked as one, and the wait was not taken: a dry run that
		// registered would hand the wait to a process that is not going to wait.
		want.Waiter = "0001-1"
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("Doorbell(dry run) %d, -want +got:\n%s", i, diff)
		}
	}
	// Nothing was recorded, so a real ring still has both the message and an unregistered doorbell to report.
	rang, err := s.Doorbell(ctx, store.DoorbellRequest{Channel: "repo", Member: "alice"})
	if err != nil {
		t.Fatalf("Doorbell(): %v", err)
	}
	if diff := cmp.Diff(store.DoorbellResult{
		Channel: "repo", Member: "alice", Messages: []store.Message{reply}, Rang: reply.Seq,
	}, rang); diff != "" {
		t.Errorf("Doorbell() after two dry runs, -want +got:\n%s", diff)
	}
}

// TestDoorbellLimitLeavesTheRestToRing is the bound a wake needs, since hook output over 10000 characters is
// replaced with a preview. The watermark moves only as far as what was handed over, or the messages the limit
// cut off would never ring at all.
func TestDoorbellLimitLeavesTheRestToRing(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	mustPost(t, s, "parser-panic", "alice", "looking at the token loop")
	first := mustPost(t, s, "parser-panic", "bob", "the same panic is in the lexer")
	second := mustPost(t, s, "parser-panic", "bob", "and the fix has to cover both")

	got, err := s.Doorbell(ctx, store.DoorbellRequest{Channel: "repo", Member: "alice", Limit: 1})
	if err != nil {
		t.Fatalf("Doorbell(limit 1): %v", err)
	}
	want := store.DoorbellResult{
		Channel: "repo", Member: "alice", Messages: []store.Message{first}, Remaining: 1, Rang: first.Seq,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Doorbell(limit 1), -want +got:\n%s", diff)
	}

	next, err := s.Doorbell(ctx, store.DoorbellRequest{Channel: "repo", Member: "alice", Limit: 1})
	if err != nil {
		t.Fatalf("Doorbell(limit 1) again: %v", err)
	}
	want = store.DoorbellResult{
		Channel: "repo", Member: "alice", Messages: []store.Message{second}, Rang: second.Seq,
	}
	if diff := cmp.Diff(want, next); diff != "" {
		t.Errorf("Doorbell(limit 1) again, -want +got:\n%s", diff)
	}
}

// TestDoorbellLaterWaiterTakesOver is what keeps a process per turn from accumulating. The hook fires once a
// turn, so without this a session that has taken twenty turns has twenty doorbells polling, and the same
// message wakes it from whichever of them gets there first.
func TestDoorbellLaterWaiterTakesOver(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	mustPost(t, s, "parser-panic", "alice", "looking at the token loop")
	reply := mustPost(t, s, "parser-panic", "bob", "the same panic is in the lexer")

	const earlier, later = "00000000000000000001-11", "00000000000000000002-22"
	if _, err := s.Doorbell(ctx, store.DoorbellRequest{
		Channel: "repo", Member: "alice", Waiter: later, DryRun: true,
	}); err != nil {
		t.Fatalf("Doorbell(dry run) for the later waiter: %v", err)
	}
	// A dry run takes nothing, so the earlier waiter is still the one holding the wait.
	got, err := s.Doorbell(ctx, store.DoorbellRequest{Channel: "repo", Member: "alice", Waiter: earlier})
	if err != nil {
		t.Fatalf("Doorbell() for the earlier waiter: %v", err)
	}
	want := store.DoorbellResult{
		Channel: "repo", Member: "alice", Messages: []store.Message{reply}, Rang: reply.Seq, Waiter: earlier,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Doorbell() for the earlier waiter, -want +got:\n%s", diff)
	}

	// The later doorbell starts, and takes the wait.
	second := mustPost(t, s, "parser-panic", "bob", "and the fix has to cover both")
	took, err := s.Doorbell(ctx, store.DoorbellRequest{Channel: "repo", Member: "alice", Waiter: later})
	if err != nil {
		t.Fatalf("Doorbell() for the later waiter: %v", err)
	}
	want = store.DoorbellResult{
		Channel: "repo", Member: "alice", Messages: []store.Message{second}, Rang: second.Seq, Waiter: later,
	}
	if diff := cmp.Diff(want, took); diff != "" {
		t.Errorf("Doorbell() for the later waiter, -want +got:\n%s", diff)
	}

	// The earlier one says nothing from here, whatever arrives, and reports who took over so it can stop.
	third := mustPost(t, s, "parser-panic", "bob", "the lexer fix is up")
	stale, err := s.Doorbell(ctx, store.DoorbellRequest{Channel: "repo", Member: "alice", Waiter: earlier})
	if err != nil {
		t.Fatalf("Doorbell() for the superseded waiter: %v", err)
	}
	want = store.DoorbellResult{
		Channel: "repo", Member: "alice", Messages: []store.Message{}, Rang: second.Seq, Waiter: later,
	}
	if diff := cmp.Diff(want, stale); diff != "" {
		t.Errorf("Doorbell() for the superseded waiter, -want +got:\n%s", diff)
	}
	// And the message it stayed quiet about is still there for the doorbell that did take over.
	rang, err := s.Doorbell(ctx, store.DoorbellRequest{Channel: "repo", Member: "alice", Waiter: later})
	if err != nil {
		t.Fatalf("Doorbell() for the later waiter: %v", err)
	}
	want = store.DoorbellResult{
		Channel: "repo", Member: "alice", Messages: []store.Message{third}, Rang: third.Seq, Waiter: later,
	}
	if diff := cmp.Diff(want, rang); diff != "" {
		t.Errorf("Doorbell() for the later waiter after a takeover, -want +got:\n%s", diff)
	}
}

// TestDoorbellRingsOnceUnderRace is the property a sequential test cannot show. Two doorbells for one member
// overlap by design, since the hook that starts them fires every turn, and both reading the watermark before
// either writes it is exactly the window that would wake a session twice for one message.
//
// Run with -race.
func TestDoorbellRingsOnceUnderRace(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	mustPost(t, s, "parser-panic", "alice", "looking at the token loop")
	reply := mustPost(t, s, "parser-panic", "bob", "the same panic is in the lexer")

	const racers = 20
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		rang    []store.DoorbellResult
		failed  []error
		release = make(chan struct{})
	)
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			got, err := s.Doorbell(ctx, store.DoorbellRequest{Channel: "repo", Member: "alice"})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				failed = append(failed, err)
			case len(got.Messages) > 0:
				rang = append(rang, got)
			}
		}()
	}
	close(release)
	wg.Wait()

	if len(failed) > 0 {
		t.Errorf("%d of %d doorbells failed, first: %v", len(failed), racers, failed[0])
	}
	if len(rang) != 1 {
		t.Fatalf("%d of %d doorbells rang, want exactly 1: %+v", len(rang), racers, rang)
	}
	want := store.DoorbellResult{
		Channel: "repo", Member: "alice", Messages: []store.Message{reply}, Rang: reply.Seq,
	}
	if diff := cmp.Diff(want, rang[0]); diff != "" {
		t.Errorf("the doorbell that rang, -want +got:\n%s", diff)
	}
}

func TestDoorbellValidates(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	for _, tc := range []struct {
		name string
		req  store.DoorbellRequest
		want error
	}{
		{name: "no channel", req: store.DoorbellRequest{Member: "alice"}, want: store.ErrNoChannel},
		{name: "no member", req: store.DoorbellRequest{Channel: "repo"}, want: store.ErrNoMember},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Doorbell(ctx, tc.req); err != tc.want {
				t.Errorf("Doorbell(%+v) = %v, want %v", tc.req, err, tc.want)
			}
		})
	}

	// An unused channel is an empty answer rather than a write. This runs at the end of every turn, and a
	// channel nobody has posted in must not be brought into existence by asking whether anything is waiting.
	got, err := s.Doorbell(ctx, store.DoorbellRequest{Channel: "never-used", Member: "alice", Waiter: "0001-1"})
	if err != nil {
		t.Fatalf("Doorbell() on an unused channel: %v", err)
	}
	want := store.DoorbellResult{
		Channel: "never-used", Member: "alice", Messages: []store.Message{}, Waiter: "0001-1",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Doorbell() on an unused channel, -want +got:\n%s", diff)
	}
	if _, ok, err := s.Channel(ctx, "never-used"); err != nil || ok {
		t.Errorf("Channel(%q) = ok %v, err %v, want ok false: asking created the channel", "never-used", ok, err)
	}
}

func mustPost(t *testing.T, s *store.Store, thread, author, body string) store.Message {
	t.Helper()
	msg, err := s.Post(t.Context(), store.PostRequest{
		Channel: "repo", Thread: thread, Author: author, Body: body,
	})
	if err != nil {
		t.Fatalf("Post(%q by %q): %v", thread, author, err)
	}
	return msg
}
