package store_test

import (
	"testing"

	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

// TestMuteSurvivesTheNextMessage is the whole point of muting, and the difference from Ack. Acknowledging says
// "read to here", so the next message in the thread undoes it and a member that had judged a thread irrelevant
// was told about it again every time it moved.
func TestMuteSurvivesTheNextMessage(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "alice", "docs-rewrite", "renaming the config keys")
	post(t, s, "repo", "alice", "parser-panic", "the token loop")

	got, err := s.Mute(ctx, store.MuteRequest{Channel: "repo", Member: "bob", Thread: "docs-rewrite"})
	if err != nil {
		t.Fatalf("Mute(): %v", err)
	}
	want := store.MuteResult{
		Channel: "repo",
		Member:  "bob",
		Muted:   true,
		Threads: []string{"docs-rewrite"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Mute(), -want +got:\n%s", diff)
	}

	post(t, s, "repo", "alice", "docs-rewrite", "and the defaults")

	unread := threads(t, s, "bob", true)
	if len(unread) != 1 || unread[0].Name != "parser-panic" {
		t.Errorf("threads waiting for bob = %+v, want only parser-panic", unread)
	}
	// Muting a thread does not read it, so what piled up while it was muted is still there to hand back.
	// "muted, 4 new" is what keeps a mute from becoming a thread nobody remembers exists.
	muted := mutedThreads(t, s, "bob")
	if len(muted) != 1 || muted[0].Name != "docs-rewrite" || muted[0].Unread != 2 || !muted[0].Muted {
		t.Errorf("muted threads = %+v, want docs-rewrite with 2 unread", muted)
	}

	// And the roster agrees, because these are the numbers that say whether somebody is behind and a member
	// cannot be behind on what it has dismissed. Both counts, since they are written twice.
	members, err := s.Members(ctx, "repo")
	if err != nil {
		t.Fatalf("Members(): %v", err)
	}
	for _, m := range members {
		if m.Name != "bob" {
			continue
		}
		if m.Unread != 1 || m.UnreadThreads != 1 {
			t.Errorf("bob = %d unread over %d threads, want only the unmuted thread's one message",
				m.Unread, m.UnreadThreads)
		}
	}
}

// TestUnmuteHandsBackWhatWasMissed is the other half: a mute is not a deletion, so taking it off shows
// everything that arrived while it was on rather than starting from now.
func TestUnmuteHandsBackWhatWasMissed(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "alice", "docs-rewrite", "renaming the config keys")
	if _, err := s.Mute(ctx, store.MuteRequest{Channel: "repo", Member: "bob", Thread: "docs-rewrite"}); err != nil {
		t.Fatalf("Mute(): %v", err)
	}
	post(t, s, "repo", "alice", "docs-rewrite", "and the defaults")

	got, err := s.Mute(ctx, store.MuteRequest{
		Channel: "repo", Member: "bob", Thread: "docs-rewrite", Unmute: true,
	})
	if err != nil {
		t.Fatalf("Mute(unmute): %v", err)
	}
	want := store.MuteResult{
		Channel: "repo",
		Member:  "bob",
		Muted:   false,
		Threads: []string{"docs-rewrite"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Mute(unmute), -want +got:\n%s", diff)
	}

	unread := threads(t, s, "bob", true)
	if len(unread) != 1 || unread[0].Unread != 2 {
		t.Errorf("after unmuting, unread = %+v, want docs-rewrite with both messages", unread)
	}
}

// TestMuteReportsOnlyWhatItChanged is what makes the result readable: an empty list means it was already that
// way, for one thread and for --all alike.
func TestMuteReportsOnlyWhatItChanged(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "alice", "docs-rewrite", "renaming the config keys")

	if _, err := s.Mute(ctx, store.MuteRequest{Channel: "repo", Member: "bob", Thread: "docs-rewrite"}); err != nil {
		t.Fatalf("Mute(): %v", err)
	}
	again, err := s.Mute(ctx, store.MuteRequest{Channel: "repo", Member: "bob", Thread: "docs-rewrite"})
	if err != nil {
		t.Fatalf("Mute() twice: %v", err)
	}
	want := store.MuteResult{Channel: "repo", Member: "bob", Muted: true, Threads: []string{}}
	if diff := cmp.Diff(want, again); diff != "" {
		t.Errorf("Mute() on an already muted thread, -want +got:\n%s", diff)
	}

	// A thread nobody has posted to and nobody has claimed does not exist, so there is nothing to mute. Not an
	// error, the same way acknowledging one that has nothing waiting is not.
	nothing, err := s.Mute(ctx, store.MuteRequest{Channel: "repo", Member: "bob", Thread: "never-happened"})
	if err != nil {
		t.Fatalf("Mute() of an unknown thread: %v", err)
	}
	if diff := cmp.Diff(want, nothing); diff != "" {
		t.Errorf("Mute() of an unknown thread, -want +got:\n%s", diff)
	}
}

// TestMuteAllLeavesNewThreadsAlone is the posture a busy channel drives an agent to: silence what is here, and
// still hear about work that starts later. It is also what makes an opt-in model unnecessary.
func TestMuteAllLeavesNewThreadsAlone(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "alice", "docs-rewrite", "renaming the config keys")
	post(t, s, "repo", "alice", "parser-panic", "the token loop")
	// A claim with no messages is a thread too, and muting everything has to include it or the next agent's
	// claim keeps arriving.
	if _, err := s.Claim(ctx, store.ClaimRequest{
		Channel: "repo", Thread: "release-1-3", Holder: "carol",
	}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}

	got, err := s.Mute(ctx, store.MuteRequest{Channel: "repo", Member: "bob", All: true})
	if err != nil {
		t.Fatalf("Mute(all): %v", err)
	}
	want := store.MuteResult{
		Channel: "repo",
		Member:  "bob",
		Muted:   true,
		Threads: []string{"docs-rewrite", "parser-panic", "release-1-3"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Mute(all), -want +got:\n%s", diff)
	}
	if left := threads(t, s, "bob", true); len(left) != 0 {
		t.Errorf("after muting everything, %d threads still nudge bob: %+v", len(left), left)
	}

	post(t, s, "repo", "alice", "lexer-bounds", "the lexer drops the last token")
	unread := threads(t, s, "bob", true)
	if len(unread) != 1 || unread[0].Name != "lexer-bounds" {
		t.Errorf("threads waiting for bob = %+v, want the new one", unread)
	}
}

// TestAMuteLiftsWhenYouTakePart is what stops a mute being permanent. Speaking in a thread or taking its work
// is caring about it again, and neither should need an unmute first.
func TestAMuteLiftsWhenYouTakePart(t *testing.T) {
	for _, tc := range []struct {
		name string
		act  func(t *testing.T, s *store.Store)
	}{
		{
			name: "posting to it",
			act: func(t *testing.T, s *store.Store) {
				post(t, s, "repo", "bob", "docs-rewrite", "one thought before I go")
			},
		},
		{
			name: "claiming it",
			act: func(t *testing.T, s *store.Store) {
				if _, err := s.Claim(t.Context(), store.ClaimRequest{
					Channel: "repo", Thread: "docs-rewrite", Holder: "bob",
				}); err != nil {
					t.Fatalf("Claim(): %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStore(t)
			ctx := t.Context()
			post(t, s, "repo", "alice", "docs-rewrite", "renaming the config keys")
			if _, err := s.Mute(ctx, store.MuteRequest{
				Channel: "repo", Member: "bob", Thread: "docs-rewrite",
			}); err != nil {
				t.Fatalf("Mute(): %v", err)
			}

			tc.act(t, s)
			post(t, s, "repo", "alice", "docs-rewrite", "and the defaults")

			unread := threads(t, s, "bob", true)
			if len(unread) != 1 || unread[0].Name != "docs-rewrite" || unread[0].Muted {
				t.Errorf("after taking part, threads waiting for bob = %+v, want docs-rewrite unmuted", unread)
			}
		})
	}
}

// TestBeingNamedLiftsAMute is the only way through a mute that does not need the muted member to act, and
// without it "muted" and "unreachable" are the same word.
func TestBeingNamedLiftsAMute(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "alice", "docs-rewrite", "renaming the config keys")
	for _, member := range []string{"claude-3fb7e7b0", "claude-3fb7e7b0-extra"} {
		if _, err := s.Mute(ctx, store.MuteRequest{
			Channel: "repo", Member: member, Thread: "docs-rewrite",
		}); err != nil {
			t.Fatalf("Mute() for %q: %v", member, err)
		}
	}

	// The longer name is the one written, so the shorter one is a substring of it and nothing else.
	post(t, s, "repo", "alice", "docs-rewrite", "@claude-3fb7e7b0-extra does this break your parser work?")

	// The named member hears it again.
	unread := threads(t, s, "claude-3fb7e7b0-extra", true)
	if len(unread) != 1 || unread[0].Muted {
		t.Errorf("the named member's threads = %+v, want docs-rewrite unmuted", unread)
	}
	// The member whose name merely sits inside it does not. Every agent in a channel is named after a session
	// id with the same prefix, so a substring match would lift a mute for a roster full of people the message
	// was not addressed to.
	if still := mutedThreads(t, s, "claude-3fb7e7b0"); len(still) != 1 {
		t.Errorf("a name matched inside a longer one: muted threads = %+v, want docs-rewrite still muted", still)
	}
}

// TestAMutedThreadIsNotInTheInbox covers the verbs that work on "everything": neither reading nor
// acknowledging without a thread should touch a muted one, or clearing an inbox would quietly consume what a
// mute was keeping to one side.
func TestAMutedThreadIsNotInTheInbox(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	post(t, s, "repo", "alice", "docs-rewrite", "renaming the config keys")
	post(t, s, "repo", "alice", "parser-panic", "the token loop")
	if _, err := s.Mute(ctx, store.MuteRequest{
		Channel: "repo", Member: "bob", Thread: "docs-rewrite",
	}); err != nil {
		t.Fatalf("Mute(): %v", err)
	}

	read, err := s.Read(ctx, store.ReadRequest{Channel: "repo", Member: "bob", Advance: true})
	if err != nil {
		t.Fatalf("Read(): %v", err)
	}
	if len(read.Messages) != 1 || read.Messages[0].Thread != "parser-panic" {
		t.Errorf("Read() delivered %+v, want only the unmuted thread", read.Messages)
	}
	if _, err := s.Ack(ctx, store.AckRequest{Channel: "repo", Member: "bob"}); err != nil {
		t.Fatalf("Ack(): %v", err)
	}
	if muted := mutedThreads(t, s, "bob"); len(muted) != 1 || muted[0].Unread != 1 {
		t.Errorf("after reading and acknowledging everything, muted = %+v, want its unread untouched", muted)
	}

	// Naming it still works, both to read it and to dismiss it: explicit beats the filter, or a muted thread
	// would be one you cannot look at on purpose.
	named, err := s.Read(ctx, store.ReadRequest{
		Channel: "repo", Member: "bob", Thread: "docs-rewrite", Advance: true,
	})
	if err != nil {
		t.Fatalf("Read(muted thread): %v", err)
	}
	if len(named.Messages) != 1 {
		t.Errorf("Read() of a muted thread delivered %+v, want the message in it", named.Messages)
	}
}

// TestMuteValidates keeps the two ways of naming nothing apart: no thread and no --all is a caller that forgot
// to say what to mute.
func TestMuteValidates(t *testing.T) {
	s, _ := newStore(t)
	for _, tc := range []struct {
		name string
		req  store.MuteRequest
		want error
	}{
		{name: "no channel", req: store.MuteRequest{Member: "bob", Thread: "t"}, want: store.ErrNoChannel},
		{name: "no member", req: store.MuteRequest{Channel: "repo", Thread: "t"}, want: store.ErrNoMember},
		{name: "no thread", req: store.MuteRequest{Channel: "repo", Member: "bob"}, want: store.ErrNoThread},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Mute(t.Context(), tc.req); err != tc.want {
				t.Errorf("Mute(%+v) = %v, want %v", tc.req, err, tc.want)
			}
		})
	}
}

func mutedThreads(t *testing.T, s *store.Store, member string) []store.Thread {
	t.Helper()
	got, err := s.Threads(t.Context(), store.ThreadsRequest{
		Channel: "repo", Member: member, Filter: store.MutedThreads,
	})
	if err != nil {
		t.Fatalf("Threads(muted): %v", err)
	}
	return got
}
