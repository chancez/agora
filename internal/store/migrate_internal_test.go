package store

import (
	"database/sql"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestANewDatabaseComesUpAtTheBaseline is what the collapse left to check. Seven migrations used to be walked
// one at a time by a test that seeded each older version; there is one statement list now, so what matters is
// that it produces every table and column the queries in this package name.
//
// Written out rather than counted, so dropping a column from the schema fails here rather than in whichever
// query reads it.
func TestANewDatabaseComesUpAtTheBaseline(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "agora.db"))

	if got := versionOf(t, s.db); got != baselineVersion {
		t.Errorf("a new database is at version %d, want the baseline %d", got, baselineVersion)
	}
	want := map[string][]string{
		"channels":  {"id", "key", "name", "created_at"},
		"messages":  {"id", "channel_id", "seq", "author", "thread", "body", "created_at"},
		"members":   {"channel_id", "name", "description", "notify", "worktree", "joined_at", "seen_at"},
		"cursors":   {"channel_id", "member", "thread", "cursor", "muted"},
		"claims":    {"channel_id", "thread", "holder", "note", "paths", "created_at"},
		"notices":   {"channel_id", "member", "thread", "holder", "claimed_at", "told_at"},
		"doorbells": {"channel_id", "member", "seq", "rang_at", "waiter"},
	}
	for table, columns := range want {
		got := columnsOf(t, s.db, table)
		slices.Sort(got)
		sorted := slices.Clone(columns)
		slices.Sort(sorted)
		if !slices.Equal(got, sorted) {
			t.Errorf("%s has columns %v, want %v", table, got, sorted)
		}
	}
	// And the unique index the per-channel sequence rests on, which is the backstop for two posts racing for
	// one number.
	if !slices.Contains(indexesOf(t, s.db), "messages_channel_seq_idx") {
		t.Errorf("indexes = %v, want the unique one on (channel_id, seq)", indexesOf(t, s.db))
	}
}

// TestOpeningTwiceChangesNothing is the failure the first version of this had: it compared versions and then
// fell through to CREATE TABLE over tables that already existed, which nothing caught because every test
// started from an empty file.
func TestOpeningTwiceChangesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agora.db")
	first := openAt(t, path)
	before := schemaOf(t, first.db)

	second := openAt(t, path)
	if got := versionOf(t, second.db); got != baselineVersion {
		t.Errorf("version after opening twice = %d, want %d", got, baselineVersion)
	}
	if after := schemaOf(t, second.db); after != before {
		t.Errorf("the schema changed on the second open:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestADatabaseFromBeforeTheCollapseIsRefused is the cost of collapsing, made explicit. The steps that would
// bring an older database forward are gone, so this binary has nothing to apply, and applying the baseline over
// it would fail on the first table. It says which version it found and what to do instead.
func TestADatabaseFromBeforeTheCollapseIsRefused(t *testing.T) {
	for version := 1; version < baselineVersion; version++ {
		t.Run("from v"+strconv.Itoa(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agora.db")
			seedVersion(t, path, version)

			s, err := Open(path)
			if err == nil {
				s.Close()
				t.Fatalf("Open() on a version %d database succeeded, want it refused", version)
			}
			for _, want := range []string{path, strconv.Itoa(version), strconv.Itoa(baselineVersion), "collapsed"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not mention %q: %v", want, err)
				}
			}
		})
	}
}

// TestAppendingAMigrationStillWorks is the arithmetic the collapse changed. The version is now the baseline plus
// how many migrations have run, so the loop starts at version minus the baseline, and getting that wrong either
// reapplies a migration or skips one. Nothing else can catch it while the list is empty.
//
// It appends to the package's own list, which is why this is not parallel: there is one list, and the next person
// to add a migration is the one this protects.
func TestAppendingAMigrationStillWorks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agora.db")
	openAt(t, path)

	migrations = append(migrations, `CREATE TABLE later(x INTEGER NOT NULL);`)
	schemaVersion = baselineVersion + len(migrations)
	t.Cleanup(func() {
		migrations = migrations[:len(migrations)-1]
		schemaVersion = baselineVersion + len(migrations)
	})

	s := openAt(t, path)
	if got := versionOf(t, s.db); got != baselineVersion+1 {
		t.Errorf("version after one appended migration = %d, want %d", got, baselineVersion+1)
	}
	if !slices.Contains(queryStrings(t, s.db,
		`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`), "later") {
		t.Error("the appended migration did not run")
	}
	// And it is not applied twice, which a wrong starting index does silently until a migration is not
	// idempotent.
	if _, err := Open(path); err != nil {
		t.Errorf("opening again after the migration: %v", err)
	}
}

func TestOpeningANewerDatabaseIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agora.db")
	seedVersion(t, path, 99)

	// Refusing beats guessing. A binary that does not know the schema cannot know which columns mean what,
	// and writing to it anyway is how a channel gets corrupted by an old agora somebody forgot to update.
	s, err := Open(path)
	if err == nil {
		s.Close()
		t.Fatal("Open() on a newer database succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "newer") {
		t.Errorf("the refusal does not say the database is newer: %v", err)
	}
}

func openAt(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seedVersion writes an empty database claiming a schema version, which is all a version check needs. It cannot
// build the tables of an older version any more: the statements that did are what the collapse removed.
func seedVersion(t *testing.T, path string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA user_version = ` + strconv.Itoa(version)); err != nil {
		t.Fatalf("set version: %v", err)
	}
}

func versionOf(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	return version
}

func columnsOf(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("read the columns of %s: %v", table, err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("read the columns of %s: %v", table, err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the columns of %s: %v", table, err)
	}
	if len(names) == 0 {
		t.Fatalf("%s has no columns, so it does not exist", table)
	}
	return names
}

func indexesOf(t *testing.T, db *sql.DB) []string {
	t.Helper()
	return queryStrings(t, db, `SELECT name FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL ORDER BY name`)
}

// schemaOf is every statement the database was built from, for comparing it against itself after a second open.
func schemaOf(t *testing.T, db *sql.DB) string {
	t.Helper()
	return strings.Join(queryStrings(t, db,
		`SELECT sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type, name`), "\n")
}

func queryStrings(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		out = append(out, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return out
}
