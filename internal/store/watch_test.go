package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

const testInterval = 200 * time.Millisecond

// receive takes the next message, or fails rather than hanging. Inside a synctest bubble the deadline
// is fake time, so this costs nothing when it is not needed.
func receive(t *testing.T, w *store.Watcher) store.Message {
	t.Helper()
	select {
	case msg, ok := <-w.Messages():
		if !ok {
			t.Fatalf("watch ended before delivering a message: %v", w.Err())
		}
		return msg
	case <-time.After(10 * testInterval):
		t.Fatal("no message delivered within 10 poll intervals")
		return store.Message{}
	}
}

func TestWatchDeliversNewMessagesWithinOneInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := newStore(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		w := s.Watch(ctx, store.WatchOptions{Channel: "repo", Interval: testInterval})
		// Wait for the watcher to have taken its baseline. Without this the post below could land
		// first, and a watcher that starts from the newest message would correctly never deliver it,
		// which reads as a bug in the watch rather than a race in the test.
		synctest.Wait()

		start := time.Now()
		first := post(t, s, "repo", "bob", "general", "parser.go panics on empty input")
		got := receive(t, w)
		if diff := cmp.Diff(first, got); diff != "" {
			t.Errorf("watch delivered, -want +got:\n%s", diff)
		}
		// Fake time makes this exact rather than a guess at a safe bound: a post is seen on the next
		// tick, so the tail latency of the poll loop is the interval and nothing more.
		if elapsed := time.Since(start); elapsed > testInterval {
			t.Errorf("delivery took %v, want at most one interval of %v", elapsed, testInterval)
		}

		second := post(t, s, "repo", "carol", "general", "same root cause as the lexer bug")
		if diff := cmp.Diff(second, receive(t, w)); diff != "" {
			t.Errorf("watch delivered second, -want +got:\n%s", diff)
		}
	})
}

func TestWatchStartsFromTheNewestMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := newStore(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		post(t, s, "repo", "bob", "general", "posted before the watch started")

		w := s.Watch(ctx, store.WatchOptions{Channel: "repo", Interval: testInterval})
		synctest.Wait()
		// Nothing is waiting, so the watcher is idle rather than replaying history: existing unread
		// is what agora read is for.
		select {
		case msg, ok := <-w.Messages():
			t.Fatalf("watch delivered %+v (open %v), want nothing before a new post", msg, ok)
		default:
		}

		next := post(t, s, "repo", "carol", "general", "posted after")
		if diff := cmp.Diff(next, receive(t, w)); diff != "" {
			t.Errorf("watch delivered, -want +got:\n%s", diff)
		}
	})
}

func TestWatchAfterAnIDDeliversTheBacklogImmediately(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := newStore(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		first := post(t, s, "repo", "bob", "general", "one")
		second := post(t, s, "repo", "bob", "general", "two")

		start := time.Now()
		w := s.Watch(ctx, store.WatchOptions{Channel: "repo", After: new(int64(0)), Interval: testInterval})
		for i, want := range []store.Message{first, second} {
			if diff := cmp.Diff(want, receive(t, w)); diff != "" {
				t.Errorf("watch delivered backlog %d, -want +got:\n%s", i, diff)
			}
		}
		// A watcher asked for a backlog must not sit through a tick first, or a caller combining a
		// cursor with a watch pays the interval for messages that were already there.
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("backlog took %v, want it before the first tick", elapsed)
		}
	})
}

func TestWatchIgnoresOtherChannels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := newStore(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		w := s.Watch(ctx, store.WatchOptions{Channel: "repo", Interval: testInterval})
		synctest.Wait()

		// data_version is a property of the whole database, so an unrelated commit wakes the poll
		// loop. Waking is not delivering.
		post(t, s, "other-repo", "bob", "general", "not for this channel")
		mine := post(t, s, "repo", "carol", "general", "for this channel")
		if diff := cmp.Diff(mine, receive(t, w)); diff != "" {
			t.Errorf("watch delivered, -want +got:\n%s", diff)
		}
	})
}

func TestWatchSeesAnotherProcessesPost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "agora.db")
		clock := newClock()
		watching := openStore(t, path, clock)
		posting := openStore(t, path, clock)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		w := watching.Watch(ctx, store.WatchOptions{Channel: "repo", Interval: testInterval})
		synctest.Wait()

		// The deployment agora is actually in: every participant is a separate process with its own
		// handle on the file.
		posted, err := posting.Post(ctx, store.PostRequest{Channel: "repo", Thread: "general", Author: "bob", Body: "from another process"})
		if err != nil {
			t.Fatalf("Post(): %v", err)
		}
		if diff := cmp.Diff(posted, receive(t, w)); diff != "" {
			t.Errorf("watch delivered, -want +got:\n%s", diff)
		}
	})
}

func TestWatchEndsCleanlyWhenTheContextEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := newStore(t)
		ctx, cancel := context.WithCancel(t.Context())

		w := s.Watch(ctx, store.WatchOptions{Channel: "repo", Interval: testInterval})
		synctest.Wait()
		cancel()

		if msg, ok := <-w.Messages(); ok {
			t.Errorf("watch delivered %+v after the context ended, want the channel closed", msg)
		}
		// A context ending is how agora watch exits on a signal, so it is not a failure to report.
		if err := w.Err(); err != nil {
			t.Errorf("Err() = %v, want nil after the context ended", err)
		}
	})
}

func TestWatchWithoutAChannelFailsRatherThanWaiting(t *testing.T) {
	s, _ := newStore(t)

	w := s.Watch(t.Context(), store.WatchOptions{Interval: testInterval})
	if msg, ok := <-w.Messages(); ok {
		t.Errorf("watch delivered %+v, want the channel closed", msg)
	}
	if err := w.Err(); !errors.Is(err, store.ErrNoChannel) {
		t.Errorf("Err() = %v, want %v", err, store.ErrNoChannel)
	}
}
