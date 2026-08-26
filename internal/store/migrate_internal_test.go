package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestOpeningACurrentDatabaseWritesNothing pins the property every watcher depends on: a command that changes
// nothing is invisible to everybody watching the channel.
//
// It was not. Setting `PRAGMA user_version` on open was unconditional, so opening the database committed a write
// that stored the value already there, inside an immediate transaction. Every watcher wakes on a commit from
// another connection, which is what Watch is built on, so `agora claims`, `agora members`, `agora dump` and a
// guard that matched no claim each woke every watcher in the channel. The guard is the one that matters, since it
// runs on every matching edit and took the write lock to do it.
//
// Internal because data_version reports commits from *other* connections, so seeing one needs a second handle on
// the same file, held open the way a watcher holds one.
func TestOpeningACurrentDatabaseWritesNothing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agora.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	defer writer.Close()
	// A channel to read, and the write that creates the schema, which is the one open that must write.
	if _, err := writer.Post(ctx, PostRequest{
		Channel: "repo", Thread: "parser-panic", Author: "alice", Body: "the token loop",
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}

	observer, err := Open(path)
	if err != nil {
		t.Fatalf("Open() a second handle: %v", err)
	}
	defer observer.Close()
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

	quiet := version()
	// Opening the database is what every command does before anything else, and a guard in the edit path does
	// only this and a read.
	for i := range 3 {
		opened, err := Open(path)
		if err != nil {
			t.Fatalf("Open() %d: %v", i, err)
		}
		if _, err := opened.Claims(ctx, "repo"); err != nil {
			t.Fatalf("Claims(): %v", err)
		}
		if err := opened.Close(); err != nil {
			t.Fatalf("Close(): %v", err)
		}
	}
	if moved := version(); moved != quiet {
		t.Errorf("data_version moved from %d to %d by opening and reading: every watcher in the channel woke for"+
			" a command that changed nothing", quiet, moved)
	}

	// And a real write is still visible, or the property above would have been bought by breaking Watch.
	if _, err := writer.Post(ctx, PostRequest{
		Channel: "repo", Thread: "parser-panic", Author: "alice", Body: "and the formatter",
	}); err != nil {
		t.Fatalf("Post() again: %v", err)
	}
	if moved := version(); moved == quiet {
		t.Errorf("data_version stayed at %d after a post, so no watcher can see a message arrive", moved)
	}
}
