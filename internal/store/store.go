// Package store is the only package in agora that knows SQL. Everything above it takes and returns the domain
// types here, never a *sql.Rows and never a transaction handle.
//
// That boundary is what keeps adding a server a refactor rather than a rewrite. See docs/design.md.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Validation failures, reported as sentinels so a caller can tell them from a database error.
var (
	ErrNoChannel = errors.New("channel is required")
	ErrNoMember  = errors.New("member is required")
	ErrNoAuthor  = errors.New("author is required")
	ErrNoBody    = errors.New("body is required")
	ErrNoThread  = errors.New("thread is required")
	ErrNoHolder  = errors.New("holder is required")
)

// Store is the handle every other package uses. One type, one method per operation.
type Store struct {
	db   *sql.DB
	path string
	now  func() time.Time
}

// Option configures a Store at open time.
type Option func(*Store)

// WithClock replaces the clock, so a test can assert on exact timestamps. Inside a testing/synctest
// bubble the default already reports fake time.
func WithClock(now func() time.Time) Option {
	return func(s *Store) { s.now = now }
}

// Open opens or creates the database at path and applies the schema.
func Open(path string, opts ...Option) (*Store, error) {
	// The driver splits its DSN at the first '?', so a path containing one would silently open a
	// different file than the caller asked for. Refusing is better than guessing, and percent
	// encoding the path instead would mean handing sqlite a file: URI, whose own parsing rules are a
	// second thing to get wrong.
	if strings.ContainsRune(path, '?') {
		return nil, fmt.Errorf("database path must not contain '?': %q", path)
	}
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}

	dsn := path + "?" + strings.Join([]string{
		// WAL so a reader is never blocked by a writer and a watcher never blocks a poster.
		"_pragma=journal_mode(wal)",
		// Every participant is a separate process racing the same file, so a write that arrives
		// mid-transaction has to wait rather than fail.
		"_pragma=busy_timeout(5000)",
		"_pragma=foreign_keys(on)",
		// A deferred transaction upgrading from read to write can lose to SQLITE_BUSY despite busy_timeout,
		// since sqlite has no safe point to retry the read from. Insurance rather than a measured fix: every
		// transaction here opens with the channel upsert, so it already holds the write lock.
		"_txlock=immediate",
	}, "&")

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	s := &Store{db: db, path: path, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Path is the database file this Store is using. agora config reports it, so that isolating a test
// against a throwaway database is checkable instead of hoped for.
func (s *Store) Path() string { return s.path }

// ensureDir creates the database directory if it is missing, and never chmods one that exists. MkdirAll leaves
// an existing mode alone, and the tempting unconditional Chmod would lock down $HOME for a database configured
// at $HOME/agora.db.
func ensureDir(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	// MkdirAll's mode is masked by the umask, and the channel is a record of what people are
	// working on.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod %s: %w", dir, err)
	}
	return nil
}

// baselineVersion is where the schema starts. Seven migrations built up to it, one at a time, and they were
// collapsed into the statements below once every database that existed had already run all seven.
//
// It is 7 rather than 1 on purpose, and the number is load bearing twice. A database already at 7 needs nothing,
// which is what made the collapse safe to do at all. And a binary from before the collapse still recognises a
// database created after it, where renumbering to 1 would have had that binary try to apply steps 2 through 7
// over a schema that already had them.
const baselineVersion = 7

// baselineSchema is the whole schema, as one statement list, for a database that does not exist yet.
//
// A database created before the collapse has the same tables with the same columns, defaults and constraints,
// reached by ALTER rather than CREATE, so its column order differs and nothing depends on that: every query here
// names the columns it wants.
const baselineSchema = `
CREATE TABLE channels (
	id         INTEGER PRIMARY KEY,
	key        TEXT NOT NULL UNIQUE,
	name       TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);

-- A message carries two numbers. seq is its place in its channel, which is the ordering every participant agrees
-- on, what cursors point at and what watch --after resumes from; id is the rowid and is never exposed. Its place
-- in its own thread, which is what a reader is shown, is derived per query rather than stored.
CREATE TABLE messages (
	id         INTEGER PRIMARY KEY,
	channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
	seq        INTEGER NOT NULL DEFAULT 0,
	author     TEXT NOT NULL,
	thread     TEXT NOT NULL DEFAULT '',
	body       TEXT NOT NULL,
	created_at INTEGER NOT NULL
);

CREATE INDEX messages_channel_idx ON messages(channel_id, id);
CREATE UNIQUE INDEX messages_channel_seq_idx ON messages(channel_id, seq);

-- description is what a member says it is doing, because a name is a session id and says nothing on its own.
CREATE TABLE members (
	channel_id  INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	notify      TEXT NOT NULL DEFAULT '',
	worktree    TEXT NOT NULL DEFAULT '',
	joined_at   INTEGER NOT NULL,
	seen_at     INTEGER NOT NULL,
	PRIMARY KEY (channel_id, name)
);

-- A cursor is per thread, so a member can read one thread and dismiss another, and muted is the sticky half of
-- that: acknowledging says "read to here" and the next message undoes it, where a mute does not.
CREATE TABLE cursors (
	channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
	member     TEXT NOT NULL,
	thread     TEXT NOT NULL,
	cursor     INTEGER NOT NULL,
	muted      INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (channel_id, member, thread)
);

CREATE TABLE claims (
	channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
	thread     TEXT NOT NULL,
	holder     TEXT NOT NULL,
	note       TEXT NOT NULL DEFAULT '',
	paths      TEXT NOT NULL DEFAULT '[]',
	created_at INTEGER NOT NULL,
	PRIMARY KEY (channel_id, thread)
);

-- What the guard has already told a member, so a notice arrives once per claim rather than on every edit under
-- it. Keyed on the thread with the holder and the claim's age beside it, so a claim that changes hands or is
-- released and taken again is worth saying once more.
CREATE TABLE notices (
	channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
	member     TEXT NOT NULL,
	thread     TEXT NOT NULL,
	holder     TEXT NOT NULL,
	claimed_at INTEGER NOT NULL,
	told_at    INTEGER NOT NULL,
	PRIMARY KEY (channel_id, member, thread)
);

-- What a member has been woken for. seq is a watermark rather than a cursor: a doorbell that marked what it rang
-- about as read would answer a message by hiding it.
CREATE TABLE doorbells (
	channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
	member     TEXT NOT NULL,
	seq        INTEGER NOT NULL DEFAULT 0,
	rang_at    INTEGER NOT NULL DEFAULT 0,
	waiter     TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (channel_id, member)
);
`

// migrations take the database from baselineVersion + N to baselineVersion + N + 1. Appending one is the whole
// procedure, and the version is the baseline plus how many have been applied.
//
// The first version of this compared versions and then fell through to CREATE TABLE over existing tables. Found
// against a real database rather than by a test, because every test started from an empty file.
var migrations = []string{}

var schemaVersion = baselineVersion + len(migrations)

// migrate brings the database up to the current version. Two processes can reach this at once on a first
// run, so the version check and the migrations share one transaction and the loser finds the work done.
func (s *Store) migrate(ctx context.Context) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var version int
		if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
			return fmt.Errorf("read schema version: %w", err)
		}
		switch {
		case version > schemaVersion:
			return fmt.Errorf("database %s has schema version %d, newer than this agora understands (%d)", s.path, version, schemaVersion)
		case version == 0:
			if _, err := tx.ExecContext(ctx, baselineSchema); err != nil {
				return fmt.Errorf("create the schema: %w", err)
			}
			version = baselineVersion
		case version < baselineVersion:
			// Refused rather than guessed at. The steps that would bring it forward were collapsed, so this
			// binary has nothing to apply and applying the baseline over it would fail on the first table.
			return fmt.Errorf("database %s is at schema version %d, older than the %d this agora starts from: the migrations up to %d were collapsed, so use an agora from before that to bring it forward, or delete it if it is a throwaway", s.path, version, baselineVersion, baselineVersion)
		}
		for i := version - baselineVersion; i < len(migrations); i++ {
			if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
				return fmt.Errorf("apply migration %d: %w", baselineVersion+i+1, err)
			}
		}
		// PRAGMA user_version takes no bound parameters.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
			return fmt.Errorf("set schema version: %w", err)
		}
		return nil
	})
}

// tx runs fn in a transaction, rolling back on error or panic.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// channelID returns the id of key, creating the channel if this is its first use. Joining, posting,
// and claiming all create the channel they name, because requiring a separate create step would be a
// step every caller has to remember and no caller can skip.
func (s *Store) channelID(ctx context.Context, tx *sql.Tx, key string) (int64, error) {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO channels(key, created_at) VALUES(?, ?) ON CONFLICT(key) DO NOTHING`,
		key, s.dbTime(),
	); err != nil {
		return 0, fmt.Errorf("create channel %q: %w", key, err)
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM channels WHERE key = ?`, key).Scan(&id); err != nil {
		return 0, fmt.Errorf("look up channel %q: %w", key, err)
	}
	return id, nil
}

// lookupChannelID finds a channel without creating it, reporting whether it exists. Read-only
// commands use this so that asking about an unused channel is an empty answer rather than a write.
func (s *Store) lookupChannelID(ctx context.Context, key string) (int64, bool, error) {
	return lookupChannelID(ctx, s.db, key)
}

// lookupChannelID reads through whatever handle it is given, so a caller already inside a transaction
// stays inside it rather than reaching around to a second connection for one lookup.
func lookupChannelID(ctx context.Context, q queryer, key string) (int64, bool, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM channels WHERE key = ?`, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("look up channel %q: %w", key, err)
	}
	return id, true, nil
}

// Channel reports a channel, or false if nothing has used that key yet.
func (s *Store) Channel(ctx context.Context, key string) (Channel, bool, error) {
	if key == "" {
		return Channel{}, false, ErrNoChannel
	}
	var (
		ch      Channel
		created int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, key, name, created_at FROM channels WHERE key = ?`, key,
	).Scan(&ch.ID, &ch.Key, &ch.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, false, nil
	}
	if err != nil {
		return Channel{}, false, fmt.Errorf("look up channel %q: %w", key, err)
	}
	ch.CreatedAt = fromDBTime(created)
	return ch, true, nil
}

// DeleteChannel removes a channel and everything in it: every message, member, cursor, and claim.
//
// Nothing else takes a channel out of the database, and every command that names one creates it, so a
// repository a hook ran in once leaves a channel that was never used. A list of channels that is mostly noise
// is one nobody reads, and the list is how somebody finds the project that wants attention.
//
// A channel with messages or claims in it is refused unless forced, which is stricter than the same rule on a
// thread: a thread is one discussion, and a channel is every discussion a repository has ever had.
func (s *Store) DeleteChannel(ctx context.Context, req DeleteChannelRequest) (DeleteChannelResult, error) {
	if req.Channel == "" {
		return DeleteChannelResult{}, ErrNoChannel
	}
	result := DeleteChannelResult{Channel: req.Channel}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		channelID, ok, err := lookupChannelID(ctx, tx, req.Channel)
		if err != nil || !ok {
			return err
		}
		result.Existed = true

		count := func(what, query string, args ...any) (int64, error) {
			var n int64
			if err := tx.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
				return 0, fmt.Errorf("count %s in %q: %w", what, req.Channel, err)
			}
			return n, nil
		}
		if result.Messages, err = count("messages",
			`SELECT count(*) FROM messages WHERE channel_id = ?`, channelID); err != nil {
			return err
		}
		// Threads come from messages and claims both, for the same reason the thread list does: a claimed
		// thread with nothing posted in it yet is still work somebody owns.
		if result.Threads, err = count("threads", `
SELECT count(*) FROM (SELECT thread FROM messages WHERE channel_id = ?
                      UNION
                      SELECT thread FROM claims WHERE channel_id = ?)`, channelID, channelID); err != nil {
			return err
		}
		if result.Members, err = count("members",
			`SELECT count(*) FROM members WHERE channel_id = ?`, channelID); err != nil {
			return err
		}
		if result.Cursors, err = count("cursors",
			`SELECT count(*) FROM cursors WHERE channel_id = ?`, channelID); err != nil {
			return err
		}
		if result.Claims, err = claimRows(ctx, tx, req.Channel, channelID); err != nil {
			return err
		}

		if req.DryRun {
			return nil
		}
		// A member row alone is not use: reading a channel is what puts you in its roster, so the channel this
		// exists to clean up has one member and nothing else. Messages and claims are what needs forcing.
		if !req.Force && (result.Messages > 0 || len(result.Claims) > 0) {
			return nil
		}
		// One statement: every table that references channels declares ON DELETE CASCADE, and foreign keys are
		// on for every connection this package opens. Listing the tables here instead would leave a table added
		// later behind, with rows pointing at an id sqlite is free to hand to the next channel created, so a
		// fresh channel would come up holding a deleted one's messages.
		if _, err := tx.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, channelID); err != nil {
			return fmt.Errorf("delete channel %q: %w", req.Channel, err)
		}
		result.Deleted = true
		return nil
	})
	if err != nil {
		return DeleteChannelResult{}, err
	}
	return result, nil
}

// dbTime is the current time in the storage representation.
func (s *Store) dbTime() int64 { return s.now().UTC().UnixNano() }

// Times are stored as unix nanoseconds: an exact round trip for any time.Time, and sortable, which a
// variable-width RFC3339 fraction is not. Raw readability is agora dump's job, not the schema's.
func fromDBTime(n int64) time.Time { return time.Unix(0, n).UTC() }
