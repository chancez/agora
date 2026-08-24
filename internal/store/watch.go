package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// DefaultWatchInterval is how often a watcher checks whether anything was committed.
//
// A data_version check is a measured 0.4ms, so 200ms is about 0.2% of one core per watcher and the
// realistic participant count is ten. That ratio, not latency, is what a server would improve on.
const DefaultWatchInterval = 200 * time.Millisecond

// WatchOptions configures a subscription.
type WatchOptions struct {
	Channel string
	// After delivers messages with a higher seq in this channel, backlog included. Nil starts from the newest
	// at the time Watch is called, which is what a bare agora watch means by "new".
	After *int64
	// Interval between checks, DefaultWatchInterval if zero.
	Interval time.Duration
}

// Watcher is a live subscription to one channel.
//
// This is the seam whose implementation is expected to change: a poll loop satisfies it today and a
// server subscription satisfies it later, and no caller can tell which it got. The one thing a bare
// channel could not express is why it stopped, and "the query failed" arriving as a closed channel
// looks exactly like "nothing to say", which is the failure mode agora exists to prevent.
type Watcher struct {
	messages chan Message

	mu  sync.Mutex
	err error
}

// Messages is closed when the watch ends, whether because the context ended or because it failed.
// Check Err after it closes.
func (w *Watcher) Messages() <-chan Message { return w.messages }

// Err is the failure that ended the watch, or nil if the context ended it.
func (w *Watcher) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *Watcher) fail(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.err = err
}

// Watch subscribes to a channel, delivering each new message until ctx ends.
//
// The mechanism is PRAGMA data_version, which sqlite bumps whenever another connection commits. So a
// watcher touches the message table only when something actually changed, which is a subscribe with
// no message-table polling, no lock, and no daemon.
func (s *Store) Watch(ctx context.Context, opts WatchOptions) *Watcher {
	w := &Watcher{messages: make(chan Message)}
	if opts.Channel == "" {
		w.fail(ErrNoChannel)
		close(w.messages)
		return w
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultWatchInterval
	}
	go func() {
		defer close(w.messages)
		err := s.watch(ctx, w, opts.Channel, opts.After, interval)
		// A context that ended is how a watch is meant to stop, and a query caught mid-flight by it
		// reports its own cancellation. Measured: `agora watch --once --timeout 100ms` printed
		// "read data_version: context deadline exceeded" on 9 of 20 runs, on the documented line
		// `agora watch --once --timeout 2m && agora read --advance`, where an error on every timeout is
		// noise that teaches a reader to ignore the stderr of a command that has something real to say
		// when it fails.
		if err != nil && ctx.Err() == nil {
			w.fail(err)
		}
	}()
	return w
}

func (s *Store) watch(ctx context.Context, w *Watcher, channel string, after *int64, interval time.Duration) error {
	// A dedicated connection, because data_version reports commits from *other* connections. Taken
	// from the pool per query, this can be the very connection that did the write, which reports no
	// change and leaves the watcher waiting forever. That failure looks like a quiet channel, which
	// is indistinguishable from a channel nobody posted to.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("watch %q: %w", channel, err)
	}
	defer conn.Close()

	last := int64(0)
	if after != nil {
		last = *after
	} else if last, err = newestSeq(ctx, conn, channel); err != nil {
		return err
	}
	version, err := dataVersion(ctx, conn)
	if err != nil {
		return err
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		// Before the first tick, so a watcher asked for a backlog does not sit idle with messages
		// already waiting.
		if err := deliver(ctx, conn, w, channel, &last); err != nil {
			return err
		}
		changed, err := waitForCommit(ctx, conn, ticker, &version)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
	}
}

// waitForCommit blocks until another connection commits something, reporting false if the context
// ended first. The message table is not touched here at all: this is one pragma read per tick.
func waitForCommit(ctx context.Context, conn *sql.Conn, ticker *time.Ticker, version *int64) (bool, error) {
	for {
		select {
		case <-ctx.Done():
			return false, nil
		case <-ticker.C:
		}
		current, err := dataVersion(ctx, conn)
		if err != nil {
			return false, err
		}
		if current != *version {
			*version = current
			return true, nil
		}
	}
}

func deliver(ctx context.Context, conn *sql.Conn, w *Watcher, channel string, last *int64) error {
	channelID, ok, err := lookupChannelID(ctx, conn, channel)
	if err != nil || !ok {
		// A channel nobody has posted to yet is not an error. A watcher started before the first
		// post is the ordinary case for an agent that begins work early.
		return err
	}
	messages, err := queryMessages(ctx, conn, channel, channelID, messageFilter{after: *last})
	if err != nil {
		return err
	}
	for _, msg := range messages {
		select {
		case w.messages <- msg:
			// The cursor only moves once the message is handed over, so a watcher that stops
			// mid-batch has not silently skipped the rest.
			*last = msg.Seq
		case <-ctx.Done():
			return nil
		}
	}
	return nil
}

func newestSeq(ctx context.Context, q queryer, channel string) (int64, error) {
	channelID, ok, err := lookupChannelID(ctx, q, channel)
	if err != nil || !ok {
		return 0, err
	}
	var newest int64
	if err := q.QueryRowContext(ctx,
		`SELECT coalesce(max(seq), 0) FROM messages WHERE channel_id = ?`, channelID,
	).Scan(&newest); err != nil {
		return 0, fmt.Errorf("find the newest message in %q: %w", channel, err)
	}
	return newest, nil
}

func dataVersion(ctx context.Context, conn *sql.Conn) (int64, error) {
	var version int64
	if err := conn.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read data_version: %w", err)
	}
	return version, nil
}
