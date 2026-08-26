package main

import (
	"strings"
	"testing"

	"github.com/chancez/agora/internal/config"
	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

func TestLeaveTakesYouOutOfTheRoster(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("post", "parser-panic", "the token loop indexes without a bounds check")
	bob.mustRun("read", "--advance")

	got := decode[store.LeaveResult](t, bob.mustRun("leave"))
	want := store.LeaveResult{
		Channel: "devtest",
		Member:  "bob",
		Left:    true,
		Cursors: 1,
		Claims:  []string{},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("agora leave, -want +got:\n%s", diff)
	}

	roster := decode[[]memberReport](t, alice.mustRun("members"))
	if len(roster) != 1 || roster[0].Name != "alice" {
		t.Errorf("the roster after leaving = %+v, want only alice", roster)
	}
	// What alice reads is untouched. A member's messages carry their author as a string, so leaving takes
	// the participant and not what they said.
	if dump := alice.mustRun("dump", "--text").stdout; !strings.Contains(dump, "the token loop") {
		t.Errorf("the record lost something when bob left:\n%s", dump)
	}
}

// TestLeaveRefusesWhileHoldingAClaim: a claim whose holder is not in the roster is worse than one held by
// somebody idle, because there is nobody left to ask and nothing says whether the work was finished.
func TestLeaveRefusesWhileHoldingAClaim(t *testing.T) {
	c := newCLI(t)
	c.mustRun("claim", "parser-panic", "--note", "fixing all three call sites")

	refused := c.run("leave")
	if refused.code != 1 {
		t.Errorf("exit code = %d, want 1 when a claim stopped it", refused.code)
	}
	result := decode[store.LeaveResult](t, refused)
	if result.Left || len(result.Claims) != 1 || result.Claims[0] != "parser-panic" {
		t.Errorf("agora leave holding a claim = %+v, want it refused and the thread named", result)
	}
	if text := c.run("leave", "--text").stdout; !strings.Contains(text, "--force") {
		t.Errorf("the refusal does not say how to override it:\n%s", text)
	}
	// Still there, so the refusal is a refusal.
	if roster := decode[[]memberReport](t, c.mustRun("members")); len(roster) != 1 {
		t.Errorf("the roster after a refused leave = %+v, want alice still there", roster)
	}

	forced := decode[store.LeaveResult](t, c.mustRun("leave", "--force"))
	if !forced.Left {
		t.Errorf("agora leave --force = %+v, want it done", forced)
	}
	if claims := decode[[]store.Claim](t, c.mustRun("claims")); len(claims) != 0 {
		t.Errorf("claims after a forced leave = %+v, want none", claims)
	}
}

// TestLeavingWhenYouWereNeverThereIsNotAFailure is the common case for the hook this exists for: a session
// that never touched the channel still fires SessionEnd, and a hook that fails on the ordinary case is a hook
// somebody removes.
func TestLeavingWhenYouWereNeverThereIsNotAFailure(t *testing.T) {
	c := newCLI(t)

	got := c.run("leave")
	if got.code != 0 {
		t.Errorf("exit code = %d, want 0: nothing to do is not a failure\nstderr: %s", got.code, got.stderr)
	}
	if result := decode[store.LeaveResult](t, got); result.Left {
		t.Errorf("agora leave = %+v, want left false", result)
	}
	if text := c.run("leave", "--text").stdout; !strings.Contains(text, "was not in devtest") {
		t.Errorf("printed %q", text)
	}
}

// TestLeaveTakesItsIdentityFromTheHookEvent keeps this aimed at the session that is ending rather than at
// whichever one the process was spawned from.
//
// The environment agrees today, measured on a real /clear: the hook saw the ending session in both the payload
// and $CLAUDE_CODE_SESSION_ID. But the variable is documented as "updated on /clear" with nothing said about
// whether that happens before the hook runs, and the payload does not depend on that ordering.
func TestLeaveTakesItsIdentityFromTheHookEvent(t *testing.T) {
	const (
		ending   = "11111111-1111-4111-8111-111111111111"
		starting = "22222222-2222-4222-8222-222222222222"
	)
	c := newCLI(t)
	// No $AGORA_MEMBER: a name somebody chose outranks a session either way, and this is about the sessions.
	delete(c.vars, config.EnvMember)
	c.vars[config.EnvClaudeSession] = starting

	// Asked for rather than spelled out, because what a name is derived from is not what this test is about:
	// internal/config pins the derivation, against hashes computed outside the code.
	endingName := memberOf(t, c, ending)
	startingName := memberOf(t, c, starting)

	// Both sessions are in the roster, the one that is ending under the name it was using.
	c.mustRun("post", "parser-panic", "from the session that is about to end", "--as", endingName)
	c.mustRun("post", "parser-panic", "from the session that just started")

	event := `{"hook_event_name":"SessionEnd","session_id":"` + ending + `","reason":"clear","cwd":"` + c.dir + `"}`
	got := decode[store.LeaveResult](t, c.stdin(event).mustRun("leave"))
	if got.Member != endingName || !got.Left {
		t.Fatalf("agora leave with a SessionEnd event = %+v, want %s gone", got, endingName)
	}

	roster := decode[[]memberReport](t, c.mustRun("members"))
	if len(roster) != 1 || roster[0].Name != startingName {
		t.Errorf("the roster = %+v, want only the session that is still running", roster)
	}
}

// memberOf is the name a session resolves to, from agora rather than from a copy of its rule.
func memberOf(t *testing.T, c *cli, session string) string {
	t.Helper()
	got := decode[configReport](t, c.withEnv(config.EnvClaudeSession, session).mustRun("config"))
	return got.Member.Value
}

// TestLeaveWithoutAnEventUsesTheUsualIdentity keeps the stdin read from being a requirement. This is a command
// people also run by hand, and a body that is not an event has to be ignored rather than fatal.
func TestLeaveWithoutAnEventUsesTheUsualIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stdin string
	}{
		{name: "nothing on stdin", stdin: ""},
		{name: "something that is not an event", stdin: "not json at all\n"},
		{name: "an event from another harness with no session in it", stdin: `{"hook_event_name":"SessionEnd"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCLI(t)
			c.mustRun("post", "general", "here")

			got := decode[store.LeaveResult](t, c.stdin(tc.stdin).mustRun("leave"))
			if got.Member != "alice" || !got.Left {
				t.Errorf("agora leave = %+v, want alice gone", got)
			}
		})
	}
}
