package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

// baseTime is when every test starts, so an assertion can name the timestamp it expects instead of
// checking that one is merely present.
var baseTime = time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

// testClock is a hand-wound clock. Timestamps are part of the value a store method returns, and a
// test that cannot predict them ends up asserting field by field, which passes while the rest of the
// struct is wrong.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *testClock { return &testClock{now: baseTime} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newStore opens a store on a throwaway database. Every test gets its own file: agora's whole purpose
// is to be read by other agents, so a test sharing a database with anything else is a message someone
// acts on.
func newStore(t *testing.T) (*store.Store, *testClock) {
	t.Helper()
	clock := newClock()
	s := openStore(t, filepath.Join(t.TempDir(), "agora.db"), clock)
	return s, clock
}

func openStore(t *testing.T, path string, clock *testClock) *store.Store {
	t.Helper()
	s, err := store.Open(path, store.WithClock(clock.Now))
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	return s
}

func TestOpenCreatesItsDirectoryPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "agora")
	clock := newClock()
	s := openStore(t, filepath.Join(dir, "agora.db"), clock)

	if got := s.Path(); got != filepath.Join(dir, "agora.db") {
		t.Errorf("Path() = %q, want %q", got, filepath.Join(dir, "agora.db"))
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(%q): %v", dir, err)
	}
	// The channel is a record of what people are working on, and os.MkdirAll's mode is masked by
	// the umask.
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("directory mode = %04o, want 0700", got)
	}
}

func TestOpenLeavesAnExistingDirectoryAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("Chmod(%q): %v", dir, err)
	}
	clock := newClock()
	openStore(t, filepath.Join(dir, "agora.db"), clock)

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(%q): %v", dir, err)
	}
	// A database configured at $HOME/agora.db must not leave agora chmod 0700 the home directory.
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("directory mode = %04o, want it left at 0755", got)
	}
}

func TestOpenRejectsAPathWithAQueryString(t *testing.T) {
	// The driver splits its DSN at the first '?', so accepting this would open a different file than
	// the caller named.
	path := filepath.Join(t.TempDir(), "agora.db?_pragma=journal_mode(delete)")
	s, err := store.Open(path)
	if err == nil {
		s.Close()
		t.Fatalf("Open(%q) succeeded, want an error", path)
	}
	if !strings.Contains(err.Error(), "'?'") {
		t.Errorf("Open(%q) error = %v, want it to name the offending character", path, err)
	}
}

func TestReopenKeepsMessagesAndAppliesTheSchemaOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agora.db")
	clock := newClock()

	first := openStore(t, path, clock)
	posted, err := first.Post(t.Context(), store.PostRequest{Channel: "repo", Thread: "general", Author: "alice", Body: "hello"})
	if err != nil {
		t.Fatalf("Post(): %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	second := openStore(t, path, clock)
	got, err := second.Messages(t.Context(), store.MessagesRequest{Channel: "repo"})
	if err != nil {
		t.Fatalf("Messages(): %v", err)
	}
	if diff := cmp.Diff([]store.Message{posted}, got); diff != "" {
		t.Errorf("Messages() after reopen, -want +got:\n%s", diff)
	}
}

func TestChannelIsCreatedOnFirstUse(t *testing.T) {
	s, _ := newStore(t)

	if _, ok, err := s.Channel(t.Context(), "repo"); err != nil || ok {
		t.Fatalf("Channel() before any use = ok %v, err %v, want ok false, err nil", ok, err)
	}
	if _, err := s.Post(t.Context(), store.PostRequest{Channel: "repo", Thread: "general", Author: "alice", Body: "hello"}); err != nil {
		t.Fatalf("Post(): %v", err)
	}

	got, ok, err := s.Channel(t.Context(), "repo")
	if err != nil {
		t.Fatalf("Channel(): %v", err)
	}
	if !ok {
		t.Fatal("Channel() reports the channel missing after a post")
	}
	want := store.Channel{ID: 1, Key: "repo", CreatedAt: baseTime}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Channel(), -want +got:\n%s", diff)
	}
}
