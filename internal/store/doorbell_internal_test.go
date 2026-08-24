package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestTakingTheWaitIsVisibleAndRecheckingIsNot pins the two sqlite properties the doorbell's takeover rests on,
// neither of which is visible through the public surface.
//
// A doorbell learns it has been taken over because the takeover is a write and every write moves data_version,
// which is the same mechanism Watch uses. So the first property is that taking the wait is visible at all: a wait
// nobody can see is a wait nobody can hand on.
//
// The second is the reason a doorbell can recheck on every commit without writing on every commit: sqlite does
// not move data_version for an UPDATE that stores the value already there. Measured here rather than assumed,
// because if it ever did, two idle agents rechecking each other's writes would be a write storm at the poll
// interval that looks like nothing at all from outside.
//
// Internal because data_version reports commits from *other* connections, so seeing one needs a second handle on
// the same file.
func TestTakingTheWaitIsVisibleAndRecheckingIsNot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agora.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	defer writer.Close()
	observer, err := Open(path)
	if err != nil {
		t.Fatalf("Open() a second handle: %v", err)
	}
	defer observer.Close()

	// The channel has to exist, or the doorbell looks it up, finds nothing, and writes nothing whatever it is
	// asked. Nothing addressed to bob is posted: a wait is taken on every check, ring or no ring.
	if _, err := writer.Post(ctx, PostRequest{
		Channel: "repo", Thread: "parser-panic", Author: "alice", Body: "looking at the token loop",
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}

	// One connection held for the whole test, the way a watcher holds one: data_version is per connection, so
	// taking a fresh one from the pool each time would compare two unrelated numbers.
	conn, err := observer.db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn(): %v", err)
	}
	defer conn.Close()
	version := func() int64 {
		t.Helper()
		got, err := dataVersion(ctx, conn)
		if err != nil {
			t.Fatalf("read data_version: %v", err)
		}
		return got
	}

	before := version()
	if _, err := writer.Doorbell(ctx, DoorbellRequest{Channel: "repo", Member: "bob", Waiter: "0001-1"}); err != nil {
		t.Fatalf("Doorbell(): %v", err)
	}
	taken := version()
	if taken == before {
		t.Errorf("data_version did not move when the wait was taken: %d, so no other doorbell can see it", taken)
	}
	for i := range 2 {
		if _, err := writer.Doorbell(ctx, DoorbellRequest{Channel: "repo", Member: "bob", Waiter: "0001-1"}); err != nil {
			t.Fatalf("Doorbell() recheck %d: %v", i, err)
		}
		if got := version(); got != taken {
			t.Fatalf("data_version moved on recheck %d: %d, want %d, so a recheck wakes every other watcher", i, got, taken)
		}
	}
}
