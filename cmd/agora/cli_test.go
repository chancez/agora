package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chancez/agora/internal/config"
	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

// cli runs commands the way a caller does: one process worth of state per invocation, against a
// throwaway database.
//
// Nothing here reads the real environment. agora's purpose is to be read by other agents, so a test
// against the real database posts to a channel real agents act on and a stray claim blocks their edits.
type cli struct {
	t    *testing.T
	dir  string
	vars map[string]string
	in   io.Reader
}

func newCLI(t *testing.T) *cli {
	t.Helper()
	// A path that does not exist yet, rather than an empty variable: an empty AGORA_DB reads as unset
	// and falls through to the real database while looking isolated.
	db := filepath.Join(t.TempDir(), "agora.db")
	cfgDir := t.TempDir()
	return &cli{
		t:   t,
		dir: t.TempDir(),
		vars: map[string]string{
			config.EnvDatabase: db,
			config.EnvChannel:  "devtest",
			config.EnvMember:   "alice",
			// The view's pane widths are a file too, and a test reading the one belonging to whoever is running
			// it is the same leak as reading their database.
			config.EnvXDGConfig: cfgDir,
		},
	}
}

// as returns a second caller against the same database and channel, which is the situation agora exists
// for: two agents who cannot see each other's state.
func (c *cli) as(member string) *cli {
	vars := make(map[string]string, len(c.vars))
	for k, v := range c.vars {
		vars[k] = v
	}
	vars[config.EnvMember] = member
	return &cli{t: c.t, dir: c.dir, vars: vars}
}

func (c *cli) database() string { return c.vars[config.EnvDatabase] }

func (c *cli) stdin(text string) *cli {
	c.in = strings.NewReader(text)
	return c
}

type result struct {
	stdout string
	stderr string
	code   int
}

func (c *cli) run(args ...string) result {
	c.t.Helper()
	var stdout, stderr bytes.Buffer
	in := c.in
	if in == nil {
		in = strings.NewReader("")
	}
	a := &app{
		in:     in,
		out:    &stdout,
		errOut: &stderr,
		resolver: config.Resolver{
			LookupEnv: func(name string) (string, bool) {
				value, ok := c.vars[name]
				return value, ok
			},
			Dir: c.dir,
			// No git: these tests are about the commands, and a repository would make the channel
			// key depend on where the test ran.
			Locate: func(string) (config.Repo, bool, error) { return config.Repo{}, false, nil },
		},
	}
	defer a.close()
	code := execute(c.t.Context(), a, args)
	return result{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// mustRun fails the test if the command did not succeed, since a later assertion on the output of a
// command that never ran is a confusing way to find out.
func (c *cli) mustRun(args ...string) result {
	c.t.Helper()
	got := c.run(args...)
	if got.code != 0 {
		c.t.Fatalf("agora %s exited %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), got.code, got.stdout, got.stderr)
	}
	return got
}

// decode unmarshals one JSON value from stdout.
func decode[T any](t *testing.T, got result) T {
	t.Helper()
	var value T
	if err := json.Unmarshal([]byte(got.stdout), &value); err != nil {
		t.Fatalf("decode stdout as %T: %v\nstdout: %s", value, err, got.stdout)
	}
	return value
}

// recent checks a timestamp came from this test run and then zeroes it, so the rest of a value can be
// asserted whole. The CLI has no clock injection on purpose: a flag only tests would use is a flag that
// can be set in production.
func recent(t *testing.T, name string, ts *time.Time) {
	t.Helper()
	if elapsed := time.Since(*ts); elapsed < 0 || elapsed > time.Minute {
		t.Errorf("%s = %v, want a timestamp from this test run", name, *ts)
	}
	*ts = time.Time{}
}

func TestConfigReportsWhereEveryAnswerCameFrom(t *testing.T) {
	c := newCLI(t)

	got := decode[configReport](t, c.mustRun("config"))
	want := configReport{
		Database:       config.Value{Value: c.database(), Source: "$AGORA_DB"},
		DatabaseExists: false,
		Channel:        config.Value{Value: "devtest", Source: "$AGORA_CHANNEL"},
		Member:         config.Value{Value: "alice", Source: "$AGORA_MEMBER"},
		Worktree:       config.Value{Value: c.dir, Source: "working directory"},
		Layout:         config.Value{Value: filepath.Join(c.vars[config.EnvXDGConfig], "agora", "tui.json"), Source: config.SourceXDGConfig},
	}
	// database_exists false before anything has written is what makes isolation checkable: a report
	// naming a throwaway path that already exists is a report about somebody else's database.
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("agora config, -want +got:\n%s", diff)
	}
	if _, err := os.Stat(c.database()); err == nil {
		t.Error("agora config created the database, want it only reported where one would go")
	}

	c.mustRun("post", "general", "first")
	got = decode[configReport](t, c.mustRun("config"))
	if !got.DatabaseExists {
		t.Error("database_exists is false after a post, want true")
	}
}

// TestAnEmptyVariableIsRejected is the cm incident at the command line. An empty variable reads as unset
// and falls through to the default, so AGORA_DB= looks like isolation and is not.
func TestAnEmptyVariableIsRejected(t *testing.T) {
	c := newCLI(t)
	real := c.database()
	c.vars[config.EnvDatabase] = ""

	got := c.run("config")
	if got.code != 1 {
		t.Errorf("exit code = %d, wanted 1 for an empty $AGORA_DB\nstdout: %s", got.code, got.stdout)
	}
	if !strings.Contains(got.stderr, config.EnvDatabase) {
		t.Errorf("stderr = %q, want it to name $AGORA_DB", got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing printed", got.stdout)
	}
	if _, err := os.Stat(real); err == nil {
		t.Error("a database was created despite the error")
	}
}

func TestJoinPostAndRead(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")

	member := decode[store.Member](t, alice.mustRun("join", "--notify", "cm send alice"))
	recent(t, "joined_at", &member.JoinedAt)
	recent(t, "seen_at", &member.SeenAt)
	wantMember := store.Member{
		Channel:  "devtest",
		Name:     "alice",
		Notify:   "cm send alice",
		Worktree: alice.dir,
	}
	if diff := cmp.Diff(wantMember, member); diff != "" {
		t.Errorf("agora join, -want +got:\n%s", diff)
	}

	posted := decode[store.Message](t, bob.mustRun("post", "parser-panic", "parser.go panics on empty input"))
	recent(t, "created_at", &posted.CreatedAt)
	// No Number: it is display-only and deliberately absent from the JSON, so it decodes as zero here.
	wantMessage := store.Message{
		Seq:     1,
		Channel: "devtest",
		Author:  "bob",
		Thread:  "parser-panic",
		Body:    "parser.go panics on empty input",
	}
	if diff := cmp.Diff(wantMessage, posted); diff != "" {
		t.Errorf("agora post, -want +got:\n%s", diff)
	}

	// Twice, and the second read has to return the same thing: reading is not acknowledging.
	for i := range 2 {
		got := decode[store.ReadResult](t, alice.mustRun("read"))
		for j := range got.Messages {
			recent(t, "created_at", &got.Messages[j].CreatedAt)
		}
		want := store.ReadResult{
			Channel:  "devtest",
			Member:   "alice",
			Messages: []store.Message{wantMessage},
			Cursors:  map[string]int64{"parser-panic": 0},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("agora read, call %d, -want +got:\n%s", i+1, diff)
		}
	}

	advanced := decode[store.ReadResult](t, alice.mustRun("read", "--advance"))
	// Cursors are per thread, so what advanced is named rather than counted.
	if advanced.Cursors["parser-panic"] != 1 {
		t.Errorf("cursors after --advance = %v, want parser-panic at 1", advanced.Cursors)
	}
	empty := decode[store.ReadResult](t, alice.mustRun("read"))
	// An empty list rather than a nil one, because the CLI prints [] and not null.
	want := store.ReadResult{
		Channel:  "devtest",
		Member:   "alice",
		Messages: []store.Message{},
		Cursors:  map[string]int64{"parser-panic": 1},
	}
	if diff := cmp.Diff(want, empty); diff != "" {
		t.Errorf("agora read after --advance, -want +got:\n%s", diff)
	}
}

// TestTheJSONCarriesOneNumber is the contract that keeps --after unambiguous. A message has two numbers, and
// only the channel sequence is a thing to hand back to agora: publishing both would let an agent pass the
// thread number to watch --after, where it is a valid number pointing at the wrong message.
func TestTheJSONCarriesOneNumber(t *testing.T) {
	c := newCLI(t)
	out := c.mustRun("post", "parser-panic", "the token loop").stdout

	var fields map[string]any
	if err := json.Unmarshal([]byte(out), &fields); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if _, ok := fields["seq"]; !ok {
		t.Errorf("no seq in the JSON:\n%s", out)
	}
	for _, absent := range []string{"number", "id"} {
		if _, ok := fields[absent]; ok {
			t.Errorf("%q is in the JSON, which invites passing it to --after:\n%s", absent, out)
		}
	}
}

func TestJoinAgainWithoutNotifyKeepsIt(t *testing.T) {
	c := newCLI(t)
	c.mustRun("join", "--notify", "cm send alice --enter")

	again := decode[store.Member](t, c.mustRun("join"))
	// Omitting the flag means "leave it alone", not "set it to empty". A rejoin that wiped the nudge
	// command would silently stop the doorbell ringing, and nothing would report that it had.
	if again.Notify != "cm send alice --enter" {
		t.Errorf("notify after a rejoin = %q, want it kept", again.Notify)
	}

	cleared := decode[store.Member](t, c.mustRun("join", "--notify", ""))
	if cleared.Notify != "" {
		t.Errorf("notify after --notify '' = %q, want it cleared", cleared.Notify)
	}
}

func TestReadLimitSaysWhatItLeftBehind(t *testing.T) {
	c := newCLI(t)
	// Posted by somebody else, because your own messages are not unread to you.
	bob := c.as("bob")
	for _, body := range []string{"one", "two", "three"} {
		bob.mustRun("post", "general", body)
	}

	got := decode[store.ReadResult](t, c.mustRun("read", "--limit", "1", "--advance"))
	if got.Remaining != 2 || len(got.Messages) != 1 || got.Cursors[got.Messages[0].Thread] != 1 {
		t.Errorf("agora read --limit 1 --advance = %+v, want 1 message, its thread at cursor 1, 2 remaining", got)
	}
	// A hook that dumps unbounded unread is silently truncated to a preview, so the count left behind
	// has to reach the reader.
	text := c.mustRun("read", "--limit", "1", "--text").stdout
	if !strings.Contains(text, "1 more unread") {
		t.Errorf("--text output does not report what was left behind:\n%s", text)
	}
}

func TestReadTextSaysWhetherTheCursorMoved(t *testing.T) {
	c := newCLI(t)
	c.as("bob").mustRun("post", "parser-panic", "parser.go panics on empty input")

	peeked := c.mustRun("read", "--text").stdout
	if !strings.Contains(peeked, "nothing marked read") {
		t.Errorf("--text output does not say nothing was marked read:\n%s", peeked)
	}
	// It also has to name the way out of getting the same thing every turn, which is now two commands:
	// read one thread, or dismiss one you have judged irrelevant.
	for _, want := range []string{"--advance", "agora ack"} {
		if !strings.Contains(peeked, want) {
			t.Errorf("--text output does not mention %q:\n%s", want, peeked)
		}
	}
	advanced := c.mustRun("read", "--text", "--advance").stdout
	if !strings.Contains(advanced, "marked read: parser-panic") {
		t.Errorf("--text output does not name the thread it marked read:\n%s", advanced)
	}
}

func TestPostReadsStdinForABodyOfDash(t *testing.T) {
	c := newCLI(t)
	body := "two symptoms, one cause:\n  - parser.go panics\n  - lexer drops the last token"

	msg := decode[store.Message](t, c.stdin(body+"\n").mustRun("post", "general", "-"))
	if msg.Body != body {
		t.Errorf("body = %q, want %q", msg.Body, body)
	}
	// A multi-line finding has to stay readable in the form a hook injects.
	text := c.mustRun("dump", "--text").stdout
	if !strings.Contains(text, "      - parser.go panics") {
		t.Errorf("dump --text does not indent continuation lines:\n%s", text)
	}
}

func TestClaimNamesTheHolderWhenItLoses(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")

	won := decode[store.ClaimResult](t, alice.mustRun("claim", "parser-panic",
		"--note", "root cause is in the token loop", "--paths", "parser.go,internal/lex/**"))
	recent(t, "created_at", &won.Claim.CreatedAt)
	wantWon := store.ClaimResult{
		Granted: true,
		Claim: store.Claim{
			Channel: "devtest",
			Thread:  "parser-panic",
			Holder:  "alice",
			Note:    "root cause is in the token loop",
			Paths:   []string{"parser.go", "internal/lex/**"},
		},
	}
	if diff := cmp.Diff(wantWon, won); diff != "" {
		t.Errorf("agora claim, -want +got:\n%s", diff)
	}

	lost := bob.run("claim", "parser-panic", "--note", "adding a nil check")
	// A bare non-zero exit is not enough here. This output is what stops duplicate work, so it has to
	// carry the holder and their note, and the status is only so a script notices.
	if lost.code != 1 {
		t.Errorf("exit code = %d, want 1 for a lost claim", lost.code)
	}
	result := decode[store.ClaimResult](t, lost)
	recent(t, "created_at", &result.Claim.CreatedAt)
	if diff := cmp.Diff(store.ClaimResult{Granted: false, Claim: wantWon.Claim}, result); diff != "" {
		t.Errorf("agora claim when lost, -want +got:\n%s", diff)
	}

	text := bob.run("claim", "parser-panic", "--text")
	for _, want := range []string{"alice holds parser-panic", "root cause is in the token loop", "parser.go"} {
		if !strings.Contains(text.stdout, want) {
			t.Errorf("--text output missing %q:\n%s", want, text.stdout)
		}
	}
}

func TestReleaseRefusesAnotherMembersClaim(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("claim", "parser-panic", "--note", "on it")

	refused := bob.run("release", "parser-panic")
	if refused.code != 1 {
		t.Errorf("exit code = %d, want 1 when the claim is not yours", refused.code)
	}
	result := decode[store.ReleaseResult](t, refused)
	if result.Released || result.Claim.Holder != "alice" {
		t.Errorf("agora release = %+v, want it refused and alice named", result)
	}

	forced := decode[store.ReleaseResult](t, bob.mustRun("release", "parser-panic", "--force"))
	if !forced.Released {
		t.Errorf("agora release --force = %+v, want it released", forced)
	}
	// Nothing left to release, and saying so beats implying it worked.
	if again := bob.run("release", "parser-panic"); again.code != 1 {
		t.Errorf("exit code = %d, want 1 when nothing is held", again.code)
	}
}

func TestMembersShowsWhoIsBehind(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("post", "general", "one")
	alice.mustRun("post", "general", "two")
	bob.mustRun("post", "general", "three")

	got := decode[[]memberReport](t, alice.mustRun("members"))
	for i := range got {
		recent(t, "joined_at", &got[i].JoinedAt)
		recent(t, "seen_at", &got[i].SeenAt)
	}
	// Each is behind only on what the other wrote. Their own posts do not count, or a hook would hand
	// an agent its own findings back and the roster would call it behind on work it did.
	// Each is behind only on what the other wrote, and both of those land in one thread.
	want := []memberReport{
		{Member: store.Member{Channel: "devtest", Name: "alice", Unread: 1, UnreadThreads: 1, Posts: 2, Worktree: alice.dir}},
		{Member: store.Member{Channel: "devtest", Name: "bob", Unread: 2, UnreadThreads: 1, Posts: 1, Worktree: bob.dir}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("agora members, -want +got:\n%s", diff)
	}

	bob.mustRun("read", "--advance")
	got = decode[[]memberReport](t, alice.mustRun("members"))
	if got[1].Unread != 0 || got[1].UnreadThreads != 0 {
		t.Errorf("bob after reading = %d unread over %d threads, want none of either",
			got[1].Unread, got[1].UnreadThreads)
	}

	// Everyone was just heard from, so nobody is stale. A threshold that catches a member who acted
	// one second ago would flag every agent that is merely waiting on its user.
	stale := decode[[]memberReport](t, alice.mustRun("members", "--stale"))
	if len(stale) != 0 {
		t.Errorf("agora members --stale = %+v, want nobody", stale)
	}

	// With no grace at all everyone qualifies, which is the direction of the filter: --stale narrows
	// to members not heard from, rather than to members who have been.
	all := decode[[]memberReport](t, alice.mustRun("members", "--stale", "--stale-after", "0s"))
	if len(all) != 2 {
		t.Errorf("agora members --stale --stale-after 0s returned %d members, want both", len(all))
	}
	for _, report := range all {
		if !report.Stale {
			t.Errorf("%s is reported as not stale under --stale, which contradicts being listed", report.Name)
		}
	}
}

func TestAMemberWhoOnlyReadStillShowsAWorktree(t *testing.T) {
	alice := newCLI(t)
	carol := alice.as("carol")
	alice.mustRun("post", "general", "one")
	// The hook path is a read, so a roster that only learned worktrees from join would show a blank
	// column for most members rather than for the odd one.
	carol.mustRun("read")

	got := decode[[]memberReport](t, alice.mustRun("members"))
	for _, report := range got {
		if report.Worktree == "" {
			t.Errorf("%s has no worktree recorded, want %s", report.Name, carol.dir)
		}
	}
}

func TestDumpIgnoresCursors(t *testing.T) {
	alice := newCLI(t)
	alice.mustRun("post", "parser-panic", "one")
	alice.mustRun("post", "general", "two")
	alice.mustRun("post", "parser-panic", "three")
	alice.mustRun("read", "--advance")

	all := decode[[]store.Message](t, alice.mustRun("dump"))
	if len(all) != 3 {
		t.Errorf("agora dump returned %d messages, want 3 regardless of the cursor", len(all))
	}
	thread := decode[[]store.Message](t, alice.mustRun("dump", "--thread", "parser-panic"))
	if len(thread) != 2 {
		t.Errorf("agora dump --thread returned %d messages, want 2", len(thread))
	}
	newest := decode[[]store.Message](t, alice.mustRun("dump", "--limit", "1"))
	if len(newest) != 1 || newest[0].Body != "three" {
		t.Errorf("agora dump --limit 1 = %+v, want the newest message", newest)
	}
}

func TestWatchOnceBlocksUntilAPostArrives(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	// The database has to exist before the watch starts, or the two callers race over creating it.
	alice.mustRun("join")

	done := make(chan result, 1)
	go func() {
		done <- alice.run("watch", "--once", "--interval", "10ms")
	}()

	// Real time rather than synctest, because these are two independent invocations: the store's watch tests
	// assert timing, and this only proves the wiring. Posting repeatedly is deliberate, since a watch starts
	// from the newest message and a single post racing the baseline would be correctly ignored.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case got := <-done:
			if got.code != 0 {
				t.Fatalf("agora watch --once exited %d\nstderr: %s", got.code, got.stderr)
			}
			msg := decode[store.Message](t, got)
			if msg.Body != "something happened" || msg.Author != "bob" {
				t.Errorf("watch delivered %+v, want bob's post", msg)
			}
			// One compact object per line, so a stream can be consumed a line at a time.
			if lines := strings.Count(strings.TrimSpace(got.stdout), "\n"); lines != 0 {
				t.Errorf("stdout spans %d lines, want one object on one line:\n%s", lines+1, got.stdout)
			}
			return
		case <-deadline:
			t.Fatal("agora watch --once did not return within 10s")
		default:
			bob.mustRun("post", "general", "something happened")
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// TestEmptyListsAreJSONArrays checks the raw output rather than the decoded value, because Go decodes
// null and [] into the same nil slice: the difference only exists in the text an agent's json.load sees,
// where iterating a null is a crash.
func TestEmptyListsAreJSONArrays(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "read with nothing unread", args: []string{"read"}, want: `"messages": []`},
		{name: "dump on an empty channel", args: []string{"dump"}, want: "[]"},
		{name: "members with nobody in the channel", args: []string{"members"}, want: "[]"},
		{name: "channels on an empty database", args: []string{"channels"}, want: "[]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A channel per case: reading is enough to put a member in the roster, so a shared one
			// would not be empty by the time members ran.
			c := newCLI(t)
			got := c.mustRun(tc.args...)
			if !strings.Contains(got.stdout, tc.want) {
				t.Errorf("agora %s printed:\n%s\nwant it to contain %s", strings.Join(tc.args, " "), got.stdout, tc.want)
			}
			if strings.Contains(got.stdout, "null") {
				t.Errorf("agora %s printed a null:\n%s", strings.Join(tc.args, " "), got.stdout)
			}
		})
	}
}

func TestWatchOnceThatTimesOutExitsNonZero(t *testing.T) {
	c := newCLI(t)
	c.mustRun("join")

	start := time.Now()
	got := c.run("watch", "--once", "--timeout", "50ms", "--interval", "10ms")
	// `agora watch --once --timeout 2m && agora read --advance` has to not go on to read nothing.
	if got.code != 1 {
		t.Errorf("exit code = %d, want 1 when nothing arrived\nstdout: %s\nstderr: %s", got.code, got.stdout, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing", got.stdout)
	}
	// timeout(1) does not exist on darwin, so this flag is the only bounded wait an agent has.
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("waited %v, want it bounded by --timeout", elapsed)
	}
}

func TestUnknownCommandFails(t *testing.T) {
	c := newCLI(t)
	got := c.run("claimm", "parser-panic")
	if got.code != 1 {
		t.Errorf("exit code = %d, want 1 for an unknown command", got.code)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing on stdout so JSON stays parseable", got.stdout)
	}
}

// TestWatchThatTimesOutSaysNothingOnStderr is a race, so it runs repeatedly. Before the fix the poll loop's own
// query was cancelled by the deadline and reported as a failure on 9 of 20 runs, which teaches a reader to
// ignore stderr from a command that has something real to say when it genuinely fails.
func TestWatchThatTimesOutSaysNothingOnStderr(t *testing.T) {
	c := newCLI(t)
	c.as("bob").mustRun("post", "general", "seed")

	for i := range 20 {
		got := c.run("watch", "--once", "--timeout", "30ms", "--interval", "5ms")
		if got.code != 1 {
			t.Fatalf("run %d: exit code = %d, want 1 when nothing arrived", i, got.code)
		}
		if got.stderr != "" {
			t.Fatalf("run %d: stderr = %q, want nothing: a timeout is how this command ends", i, got.stderr)
		}
	}
}
