package main

import (
	"strings"
	"testing"

	"github.com/chancez/agora/internal/store"
)

func TestChannelsListsThemAndMarksTheOneYouAreIn(t *testing.T) {
	c := newCLI(t)
	c.mustRun("post", "parser-panic", "the token loop")
	c.mustRun("post", "docs-rewrite", "over in another repository", "--channel", "other-repo")

	channels := decode[[]store.Channel](t, c.mustRun("channels"))
	if len(channels) != 2 {
		t.Fatalf("channels = %+v, want both", channels)
	}

	// The one every other command means is the one worth telling apart, since the interesting case is
	// deleting one of the others.
	text := c.mustRun("channels", "--text").stdout
	if !strings.Contains(text, "* devtest") || !strings.Contains(text, "  other-repo") {
		t.Errorf("channels --text does not mark the current channel:\n%s", text)
	}
}

// TestDeleteChannelTakesTheUnusedOne is the case this exists for, built the way the real one is: a command run
// once in a repository creates the channel and puts the caller in its roster, and nothing else ever removes it.
func TestDeleteChannelTakesTheUnusedOne(t *testing.T) {
	c := newCLI(t)
	c.mustRun("post", "parser-panic", "the token loop")
	c.mustRun("threads", "--channel", "visited-once")

	// The default is a preview and a nonzero status, so a script cannot mistake "here is what I would do"
	// for "done".
	preview := c.run("delete-channel", "visited-once")
	if preview.code != 1 {
		t.Errorf("exit code = %d, want 1 for a preview", preview.code)
	}
	if result := decode[store.DeleteChannelResult](t, preview); result.Deleted || !result.Existed || result.Members != 1 {
		t.Errorf("preview = %+v, want it not deleted, found, and the member counted", result)
	}
	if left := decode[[]store.Channel](t, c.mustRun("channels")); len(left) != 2 {
		t.Errorf("the preview deleted something: %+v", left)
	}

	deleted := decode[store.DeleteChannelResult](t, c.mustRun("delete-channel", "visited-once", "--yes"))
	if !deleted.Deleted {
		t.Errorf("delete-channel --yes = %+v, want it deleted", deleted)
	}
	left := decode[[]store.Channel](t, c.mustRun("channels"))
	if len(left) != 1 || left[0].Key != "devtest" {
		t.Errorf("channels after deleting = %+v, want only devtest", left)
	}
}

// TestDeleteChannelRefusesOneInUse is the stricter half. A channel is every discussion a repository has had, so
// what needs --force here is any record at all, not only somebody else's claim.
func TestDeleteChannelRefusesOneInUse(t *testing.T) {
	c := newCLI(t)
	alice := c.as("alice")
	alice.mustRun("post", "parser-panic", "the token loop", "--channel", "busy")
	alice.mustRun("claim", "parser-panic", "--note", "fixing the token loop", "--channel", "busy")

	bob := c.as("bob")
	refused := bob.run("delete-channel", "busy", "--yes")
	if refused.code != 1 {
		t.Errorf("exit code = %d, want 1 when the channel has been used", refused.code)
	}
	result := decode[store.DeleteChannelResult](t, refused)
	if result.Deleted || result.Messages != 1 || len(result.Claims) != 1 {
		t.Errorf("delete-channel of a used channel = %+v, want it refused with what stopped it", result)
	}

	// The refusal has to say how to override it and who is working in there: that note is what decides
	// whether deleting it is a decision or a mistake.
	text := bob.run("delete-channel", "busy", "--yes", "--text").stdout
	for _, want := range []string{"--force", "alice", "fixing the token loop"} {
		if !strings.Contains(text, want) {
			t.Errorf("the refusal does not name %q:\n%s", want, text)
		}
	}

	forced := decode[store.DeleteChannelResult](t, bob.mustRun("delete-channel", "busy", "--yes", "--force"))
	if !forced.Deleted {
		t.Errorf("delete-channel --yes --force = %+v, want it deleted", forced)
	}
}

func TestDeleteChannelThatNeverExistedSaysSo(t *testing.T) {
	c := newCLI(t)

	got := c.run("delete-channel", "never-existed", "--yes")
	if got.code != 1 {
		t.Errorf("exit code = %d, want 1 when there was nothing to delete", got.code)
	}
	if text := c.run("delete-channel", "never-existed", "--text").stdout; !strings.Contains(text, "no channel") {
		t.Errorf("printed %q", text)
	}
	// Asking must not create what it failed to find, or cleaning up would leave the list longer.
	if left := decode[[]store.Channel](t, c.mustRun("channels")); len(left) != 0 {
		t.Errorf("channels = %+v, want none created by asking about one", left)
	}
}

// TestDeleteChannelNeedsAKey: the channel to delete is named rather than taken from where you are standing,
// because the one worth deleting is rarely the one you are working in.
func TestDeleteChannelNeedsAKey(t *testing.T) {
	c := newCLI(t)
	c.mustRun("post", "parser-panic", "the token loop")

	got := c.run("delete-channel", "--yes")
	if got.code != 1 {
		t.Errorf("exit code = %d, want 1 with no channel named", got.code)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing", got.stdout)
	}
	if left := decode[[]store.Channel](t, c.mustRun("channels")); len(left) != 1 {
		t.Errorf("channels = %+v, want devtest still there", left)
	}
}
