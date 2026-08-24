package main

import (
	"strings"
	"testing"

	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

// TestMuteOutlastsTheNextMessage is the contract difference from ack, at the level an agent sees it: dismissing
// with ack lasts until somebody posts again, and dismissing with mute does not.
func TestMuteOutlastsTheNextMessage(t *testing.T) {
	alice, bob := setupThreads(t)

	got := decode[store.MuteResult](t, bob.mustRun("mute", "docs-rewrite"))
	want := store.MuteResult{
		Channel: "devtest",
		Member:  "bob",
		Muted:   true,
		Threads: []string{"docs-rewrite"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("agora mute docs-rewrite, -want +got:\n%s", diff)
	}

	alice.as("carol").mustRun("post", "docs-rewrite", "and the defaults, still nothing to do with the parser")

	if left := decode[[]store.Thread](t, bob.mustRun("threads", "--unread")); len(left) != 1 ||
		left[0].Name != "parser-panic" {
		t.Errorf("still waiting = %+v, want only parser-panic", left)
	}
	// Not lost, though. It is on the list a briefing spends one line on, with what has piled up.
	muted := decode[[]store.Thread](t, bob.mustRun("threads", "--muted"))
	if len(muted) != 1 || muted[0].Name != "docs-rewrite" || muted[0].Unread != 2 || !muted[0].Muted {
		t.Errorf("agora threads --muted = %+v, want docs-rewrite with 2 unread", muted)
	}
}

func TestUnmuteBringsItBack(t *testing.T) {
	_, bob := setupThreads(t)
	bob.mustRun("mute", "docs-rewrite")

	got := decode[store.MuteResult](t, bob.mustRun("unmute", "docs-rewrite"))
	if got.Muted || len(got.Threads) != 1 {
		t.Errorf("agora unmute docs-rewrite = %+v, want muted false and the thread named", got)
	}
	if left := decode[[]store.Thread](t, bob.mustRun("threads", "--unread")); len(left) != 2 {
		t.Errorf("still waiting = %+v, want both threads back", left)
	}
}

// TestMuteAllThenNewThreads is the posture for a channel that is mostly other people's work: silence what is
// here, keep hearing what starts later.
func TestMuteAllThenNewThreads(t *testing.T) {
	alice, bob := setupThreads(t)

	if got := decode[store.MuteResult](t, bob.mustRun("mute", "--all")); len(got.Threads) != 2 {
		t.Errorf("agora mute --all = %+v, want both threads", got)
	}
	if left := decode[[]store.Thread](t, bob.mustRun("threads", "--unread")); len(left) != 0 {
		t.Errorf("still waiting = %+v, want nothing", left)
	}

	alice.mustRun("post", "lexer-bounds", "the lexer drops the last token")
	left := decode[[]store.Thread](t, bob.mustRun("threads", "--unread"))
	if len(left) != 1 || left[0].Name != "lexer-bounds" {
		t.Errorf("still waiting = %+v, want the thread that started after the mute", left)
	}
}

// TestABareMuteRefuses is the same guard ack has, for the same reason: silencing a whole channel should not be
// four letters and no argument.
func TestABareMuteRefuses(t *testing.T) {
	_, bob := setupThreads(t)

	for _, args := range [][]string{{"mute"}, {"unmute"}, {"mute", "docs-rewrite", "--all"}} {
		got := bob.run(args...)
		if got.code != 1 {
			t.Errorf("agora %s exited %d, want 1", strings.Join(args, " "), got.code)
		}
	}
	if left := decode[[]store.Thread](t, bob.mustRun("threads", "--unread")); len(left) != 2 {
		t.Errorf("still waiting = %+v, want both threads untouched", left)
	}
}

// TestMutingTwiceSaysItWasAlreadyMuted is the idempotent case, which has to read as ordinary rather than as a
// failure: an empty list of threads with no explanation looks like the command did not work.
func TestMutingTwiceSaysItWasAlreadyMuted(t *testing.T) {
	_, bob := setupThreads(t)
	bob.mustRun("mute", "docs-rewrite")

	got := bob.mustRun("mute", "docs-rewrite", "--text").stdout
	if !strings.Contains(got, "already muted") {
		t.Errorf("muting twice printed %q, want it to say the thread was already muted", got)
	}
}

// TestUnreadAndMutedAreOppositeQuestions keeps the two filters from being combined into something that reads
// like a query and returns nothing forever.
func TestUnreadAndMutedAreOppositeQuestions(t *testing.T) {
	_, bob := setupThreads(t)

	got := bob.run("threads", "--unread", "--muted")
	if got.code != 1 || !strings.Contains(got.stderr, "opposite") {
		t.Errorf("agora threads --unread --muted exited %d with stderr %q, want it refused", got.code, got.stderr)
	}
}
