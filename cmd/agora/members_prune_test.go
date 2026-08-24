package main

import (
	"strings"
	"testing"

	"github.com/chancez/agora/internal/store"
)

// TestPruneSweepsTheRoster is the command a person runs after noticing a name that has not been heard from in
// hours. The claim holder is the case worth covering: it is reported and left, because releasing somebody's claim
// is the one part of a wrong removal that cannot be taken back.
func TestPruneSweepsTheRoster(t *testing.T) {
	alice := newCLI(t)
	alice.mustRun("post", "parser-panic", "empty input reaches the token loop")
	holder := alice.as("holder")
	holder.mustRun("claim", "release-1-3", "--note", "cutting it tomorrow")
	sweeper := alice.as("sweeper")

	// Nothing is stale yet, so the sweep says so rather than doing something.
	quiet := decode[store.PruneResult](t, sweeper.mustRun("prune"))
	if len(quiet.Pruned) != 0 || len(quiet.Held) != 0 {
		t.Errorf("agora prune with a fresh roster = %+v, want nothing", quiet)
	}

	// Everything counts as stale, which is what a zero window means.
	got := decode[store.PruneResult](t, sweeper.mustRun("prune", "--stale-after", "0s"))
	if len(got.Pruned) != 1 || got.Pruned[0] != "alice" {
		t.Errorf("agora prune removed %v, want alice", got.Pruned)
	}
	if threads, ok := got.Held["holder"]; !ok || len(threads) != 1 || threads[0] != "release-1-3" {
		t.Errorf("held = %v, want holder with its claim", got.Held)
	}

	left := decode[[]memberReport](t, sweeper.mustRun("members"))
	names := make([]string, 0, len(left))
	for _, member := range left {
		names = append(names, member.Name)
	}
	// The holder stays because of its claim, and the sweeper is here because sweeping is an action.
	if len(names) != 2 || names[0] != "holder" || names[1] != "sweeper" {
		t.Errorf("the roster after a sweep = %v, want holder and sweeper", names)
	}
	// And the claim it was holding is untouched.
	if claims := decode[[]store.Claim](t, sweeper.mustRun("claims")); len(claims) != 1 {
		t.Errorf("claims after a sweep = %+v, want the one it left alone", claims)
	}
}

func TestPruneDryRunSaysWhatItWouldTake(t *testing.T) {
	alice := newCLI(t)
	alice.mustRun("post", "parser-panic", "empty input reaches the token loop")
	sweeper := alice.as("sweeper")

	got := sweeper.mustRun("prune", "--dry-run", "--stale-after", "0s", "--text")
	if !strings.Contains(got.stdout, "would remove alice") {
		t.Errorf("agora prune --dry-run printed %q", got.stdout)
	}
	if left := decode[[]memberReport](t, sweeper.mustRun("members")); len(left) != 2 {
		t.Errorf("the roster after a dry run = %+v, want it untouched", left)
	}
}

// TestTheRosterAndTheSweepAgreeOnStale is the property the docs rest on: agora members --stale is how somebody
// checks what agora prune would take, so a different default on either would make that preview a different
// question from the sweep.
func TestTheRosterAndTheSweepAgreeOnStale(t *testing.T) {
	a := &app{}
	roster := newMembersCmd(a).Flags().Lookup("stale-after")
	sweep := newPruneCmd(a).Flags().Lookup("stale-after")
	if roster.DefValue != sweep.DefValue {
		t.Errorf("members --stale-after defaults to %s and prune to %s, want one number",
			roster.DefValue, sweep.DefValue)
	}
	if roster.DefValue != "2h0m0s" {
		t.Errorf("the stale window defaults to %s, want 2h: it is the bar for deleting a row", roster.DefValue)
	}
}
