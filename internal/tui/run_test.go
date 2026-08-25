package tui

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chancez/agora/internal/config"
	"github.com/chancez/agora/internal/store"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/go-cmp/cmp"
)

// syncBuffer collects the frames. bubbletea renders from its own goroutine, so a plain bytes.Buffer here is a
// data race that -race would report and a reader would miss.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

const testChannel = "devtest"

func testStore(t *testing.T) (*store.Store, config.Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agora.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, config.Config{
		Database: config.Value{Value: path, Source: "$AGORA_DB"},
		Channel:  config.Value{Value: testChannel, Source: "$AGORA_CHANNEL"},
		Member:   config.Value{Value: "watcher", Source: "$AGORA_MEMBER"},
	}
}

// start runs the view over pipes and returns what it drew. refresh is injectable because a reload brings in
// new messages too, so a test cannot tell a working watch from a broken one unless it is turned out of the way.
func start(t *testing.T, s *store.Store, cfg config.Config, refresh time.Duration) (*tea.Program, io.Writer, *syncBuffer, chan error) {
	t.Helper()
	// No saved layout, and nowhere to save one: a test must not read or write the widths of whoever is running
	// it, the same rule the database follows.
	return startWith(t, s, cfg, refresh, &memoryLayout{})
}

func startWith(t *testing.T, s *store.Store, cfg config.Config, refresh time.Duration, layout layoutStore) (*tea.Program, io.Writer, *syncBuffer, chan error) {
	t.Helper()
	out := &syncBuffer{}
	in, keys := io.Pipe()
	t.Cleanup(func() { keys.Close() })

	// Built here rather than inside the goroutine: a ProgramOption runs while NewProgram is still
	// initialising, so reaching in through one and sending from another goroutine is a race.
	program := newProgram(t.Context(), s, cfg, refresh, layout,
		tea.WithInput(in), tea.WithOutput(out), tea.WithoutSignals(), tea.WithoutCatchPanics())
	done := make(chan error, 1)
	go func() { done <- runProgram(t.Context(), program) }()
	// Wide enough for all four columns and for a message body to fit on one line: a body that wrapped would
	// make every waitFor here a substring that is no longer on any single line.
	program.Send(tea.WindowSizeMsg{Width: 120, Height: 22})
	// keys is the terminal's end of the input, for the one test that needs a keystroke to go through bubbletea's
	// own parser rather than around it.
	return program, keys, out, done
}

// waitFor polls rather than sleeping a fixed time, so it is as fast as the thing it waits for.
func waitFor(t *testing.T, out *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%q never appeared on screen. what was rendered:\n%s", want, out.String())
}

func quit(t *testing.T, program *tea.Program, done chan error) {
	t.Helper()
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run(): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("q did not quit within 5s")
	}
}

// TestRunAgainstARealStore is the wiring the model tests cannot reach: whether the channels, the threads of
// the selected one, and the messages of the selected thread all get loaded and drawn.
func TestRunAgainstARealStore(t *testing.T) {
	s, cfg := testStore(t)
	ctx := t.Context()
	if _, err := s.Post(ctx, store.PostRequest{
		Channel: testChannel, Thread: "docs-rewrite", Author: "carol", Body: "renaming the config keys",
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}
	if _, err := s.Post(ctx, store.PostRequest{
		Channel: testChannel, Thread: "parser-panic", Author: "alice", Body: "empty input reaches the token loop",
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}
	if _, err := s.Claim(ctx, store.ClaimRequest{
		Channel: testChannel, Thread: "parser-panic", Holder: "alice", Paths: []string{"parser.go"},
	}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}

	program, _, out, done := start(t, s, cfg, time.Minute)
	// The channel, both threads, and the newest thread's messages and claim.
	waitFor(t, out, "devtest")
	waitFor(t, out, "parser-panic")
	waitFor(t, out, "docs-rewrite")
	waitFor(t, out, "empty input reaches the token loop")
	waitFor(t, out, "held by alice")

	// Read-only: opening the view must not put the viewer in the roster it is displaying.
	members, err := s.Members(ctx, testChannel)
	if err != nil {
		t.Fatalf("Members(): %v", err)
	}
	for _, member := range members {
		if member.Name == cfg.Member.Value {
			t.Errorf("opening the view joined the channel as %q", member.Name)
		}
	}
	quit(t, program, done)
}

// TestSelectingAnotherThreadLoadsIt is the navigation, end to end: moving the selection has to fetch what it
// selected, since nothing else will.
func TestSelectingAnotherThreadLoadsIt(t *testing.T) {
	s, cfg := testStore(t)
	ctx := t.Context()
	for _, post := range []struct{ thread, body string }{
		{"docs-rewrite", "renaming the config keys"},
		{"parser-panic", "empty input reaches the token loop"},
	} {
		if _, err := s.Post(ctx, store.PostRequest{
			Channel: testChannel, Thread: post.thread, Author: "alice", Body: post.body,
		}); err != nil {
			t.Fatalf("Post(): %v", err)
		}
	}

	program, _, out, done := start(t, s, cfg, time.Minute)
	waitFor(t, out, "empty input reaches the token loop")
	if strings.Contains(out.String(), "renaming the config keys") {
		t.Fatal("the other thread's messages were on screen before it was selected")
	}

	// Into the thread list from the channels, then down one. Focus starts on the leftmost pane.
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	waitFor(t, out, "renaming the config keys")
	quit(t, program, done)
}

// TestTheWatchDeliversIntoTheSelectedThread isolates the watch from the reload, which is the only way to
// know it works: with the real interval a broken watch is invisible because the reload covers for it.
func TestTheWatchDeliversIntoTheSelectedThread(t *testing.T) {
	s, cfg := testStore(t)
	ctx := t.Context()
	if _, err := s.Post(ctx, store.PostRequest{
		Channel: testChannel, Thread: "parser-panic", Author: "alice", Body: "empty input reaches the token loop",
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}

	program, _, out, done := start(t, s, cfg, time.Minute)
	waitFor(t, out, "empty input reaches the token loop")

	// Two of them, because one cannot tell a watch that keeps listening from one that delivers a single
	// message and stops.
	for _, body := range []string{"landed while watching", "and one more after that"} {
		if _, err := s.Post(ctx, store.PostRequest{
			Channel: testChannel, Thread: "parser-panic", Author: "bob", Body: body,
		}); err != nil {
			t.Fatalf("Post(): %v", err)
		}
		waitFor(t, out, body)
	}
	quit(t, program, done)
}

// TestTheReloadNoticesAClaim is the other half. Taking a claim posts no message, so a watch alone would show
// a live log beside a sidebar that never changes.
func TestTheReloadNoticesAClaim(t *testing.T) {
	s, cfg := testStore(t)
	ctx := t.Context()
	if _, err := s.Post(ctx, store.PostRequest{
		Channel: testChannel, Thread: "parser-panic", Author: "alice", Body: "empty input reaches the token loop",
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}

	program, _, out, done := start(t, s, cfg, 20*time.Millisecond)
	waitFor(t, out, "empty input reaches the token loop")
	if strings.Contains(out.String(), "held by") {
		t.Fatal("a claim was on screen before one was taken")
	}

	if _, err := s.Claim(ctx, store.ClaimRequest{
		Channel: testChannel, Thread: "parser-panic", Holder: "carol",
		Note: "taken while the view was open",
	}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}
	waitFor(t, out, "held by carol")
	waitFor(t, out, "taken while the view was open")
	quit(t, program, done)
}

func TestRunEndsWhenTheContextDoes(t *testing.T) {
	s, cfg := testStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	in, keys := io.Pipe()
	defer keys.Close()

	program := newProgram(ctx, s, cfg, time.Minute, &memoryLayout{},
		tea.WithInput(in), tea.WithOutput(&syncBuffer{}), tea.WithoutSignals())
	done := make(chan error, 1)
	go func() { done <- runProgram(ctx, program) }()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		// A signal is how this command is meant to end, so it is not a failure to report.
		if err != nil {
			t.Errorf("run() after the context ended = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the view did not exit within 5s of the context ending")
	}
}

// TestDeleteFromTheViewEndToEnd drives the d key against a real store: the question, then the deletion, then
// the sidebars reflecting it.
func TestDeleteFromTheViewEndToEnd(t *testing.T) {
	s, cfg := testStore(t)
	ctx := t.Context()
	for _, post := range []struct{ thread, body string }{
		{"keep", "this one matters"},
		{"noise", "never mind this"},
	} {
		if _, err := s.Post(ctx, store.PostRequest{
			Channel: testChannel, Thread: post.thread, Author: "alice", Body: post.body,
		}); err != nil {
			t.Fatalf("Post(): %v", err)
		}
	}

	program, _, out, done := start(t, s, cfg, time.Minute)
	waitFor(t, out, "noise")
	// Onto the threads, because d acts on whatever the keyboard is on and it opens on the channels.
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	waitFor(t, out, "delete noise")
	waitFor(t, out, "[y/n]")

	// Still there while the question stands: asking is not doing.
	threads, err := s.Threads(ctx, store.ThreadsRequest{Channel: testChannel, Member: "watcher", Observe: true})
	if err != nil {
		t.Fatalf("Threads(): %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("threads while confirming = %+v, want both", threads)
	}

	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	deadline := time.Now().Add(10 * time.Second)
	for {
		threads, err = s.Threads(ctx, store.ThreadsRequest{Channel: testChannel, Member: "watcher", Observe: true})
		if err != nil {
			t.Fatalf("Threads(): %v", err)
		}
		if len(threads) == 1 && threads[0].Name == "keep" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("threads after y = %+v, want only keep. what was rendered:\n%s", threads, out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	quit(t, program, done)
}

// TestDeleteChannelFromTheViewEndToEnd drives d on the channels pane against a real store, and covers the one
// thing no model test can see: the view polls the channel it has selected, and every command that names a
// channel creates it, so a deletion that left the selection where it was would recreate what it just deleted.
func TestDeleteChannelFromTheViewEndToEnd(t *testing.T) {
	s, cfg := testStore(t)
	ctx := t.Context()
	if _, err := s.Post(ctx, store.PostRequest{
		Channel: testChannel, Thread: "keep", Author: "alice", Body: "this one matters",
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}
	// A channel one command created and nothing ever used, which is what this key is for.
	if _, err := s.Threads(ctx, store.ThreadsRequest{Channel: "visited-once", Member: "watcher"}); err != nil {
		t.Fatalf("Threads(): %v", err)
	}

	program, _, out, done := start(t, s, cfg, time.Minute)
	waitFor(t, out, "visited-once")
	// Down to it in the channels pane, which is where the view opens.
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	waitFor(t, out, "delete channel visited-once")

	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	deadline := time.Now().Add(10 * time.Second)
	for {
		channels, err := s.Channels(ctx, "watcher")
		if err != nil {
			t.Fatalf("Channels(): %v", err)
		}
		if len(channels) == 1 && channels[0].Key == testChannel {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("channels after y = %+v, want only %s. what was rendered:\n%s", channels, testChannel, out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// And it stays gone. The deletion is followed by a reload, which is the call that would bring it back.
	time.Sleep(300 * time.Millisecond)
	channels, err := s.Channels(ctx, "watcher")
	if err != nil {
		t.Fatalf("Channels(): %v", err)
	}
	if len(channels) != 1 {
		t.Errorf("channels after the reload = %+v, want the deleted one to have stayed deleted", channels)
	}
	quit(t, program, done)
}

func TestCancellingADeleteLeavesItAlone(t *testing.T) {
	s, cfg := testStore(t)
	if _, err := s.Post(t.Context(), store.PostRequest{
		Channel: testChannel, Thread: "noise", Author: "alice", Body: "never mind this",
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}

	program, _, out, done := start(t, s, cfg, time.Minute)
	waitFor(t, out, "noise")
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	waitFor(t, out, "delete noise")
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})

	// Give it as long as a deletion would have taken, then check it did not happen.
	time.Sleep(300 * time.Millisecond)
	threads, err := s.Threads(t.Context(), store.ThreadsRequest{Channel: testChannel, Member: "watcher", Observe: true})
	if err != nil {
		t.Fatalf("Threads(): %v", err)
	}
	if len(threads) != 1 {
		t.Errorf("threads after n = %+v, want it left alone", threads)
	}
	quit(t, program, done)
}

// TestPostingFromTheViewEndToEnd drives the p key against a real store, which is the only way to see whether a
// keystroke reaches the database: the model tests stop at the request. The body has spaces in it, and a space
// is a key with its own name in bubbletea, so a prompt reading names rather than runes would drop them.
func TestPostingFromTheViewEndToEnd(t *testing.T) {
	s, cfg := testStore(t)
	ctx := t.Context()
	if _, err := s.Post(ctx, store.PostRequest{
		Channel: testChannel, Thread: "parser-panic", Author: "alice", Body: "empty input reaches the token loop",
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}

	program, _, out, done := start(t, s, cfg, time.Minute)
	waitFor(t, out, "parser-panic")

	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	waitFor(t, out, "post to parser-panic | ")
	const body = "confirmed, the lexer indexes it the same way"
	for _, r := range body {
		if r == ' ' {
			program.Send(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
			continue
		}
		program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	waitFor(t, out, body)
	program.Send(tea.KeyMsg{Type: tea.KeyEnter})

	// In the database, authored by whoever the view is, and on screen without waiting for the reload.
	deadline := time.Now().Add(10 * time.Second)
	for {
		messages, err := s.Messages(ctx, store.MessagesRequest{Channel: testChannel, Thread: "parser-panic"})
		if err != nil {
			t.Fatalf("Messages(): %v", err)
		}
		if len(messages) == 2 {
			if messages[1].Body != body || messages[1].Author != cfg.Member.Value {
				t.Fatalf("posted message = %+v, want %q by %q", messages[1], body, cfg.Member.Value)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the post never landed: %+v. what was rendered:\n%s", messages, out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// And posting is what puts you in the roster, where opening the view alone does not.
	members, err := s.Members(ctx, testChannel)
	if err != nil {
		t.Fatalf("Members(): %v", err)
	}
	found := false
	for _, member := range members {
		found = found || member.Name == cfg.Member.Value
	}
	if !found {
		t.Errorf("posting did not join the channel: %+v", members)
	}
	quit(t, program, done)
}

// TestClaimingFromTheViewEndToEnd covers the two step prompt and the paths reaching the store, since the paths
// are the only part of a claim that agora guard can act on.
func TestClaimingFromTheViewEndToEnd(t *testing.T) {
	s, cfg := testStore(t)
	ctx := t.Context()
	if _, err := s.Post(ctx, store.PostRequest{
		Channel: testChannel, Thread: "parser-panic", Author: "alice", Body: "empty input reaches the token loop",
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}

	program, _, out, done := start(t, s, cfg, time.Minute)
	waitFor(t, out, "parser-panic")

	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	waitFor(t, out, "claim parser-panic, note | ")
	for _, r := range "mine" {
		program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	program.Send(tea.KeyMsg{Type: tea.KeyEnter})
	waitFor(t, out, "claim parser-panic, paths | ")
	for _, r := range "parser.go" {
		program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	program.Send(tea.KeyMsg{Type: tea.KeyEnter})

	deadline := time.Now().Add(10 * time.Second)
	for {
		claims, err := s.Claims(ctx, testChannel)
		if err != nil {
			t.Fatalf("Claims(): %v", err)
		}
		if len(claims) == 1 {
			want := store.Claim{
				Channel: testChannel, Thread: "parser-panic", Holder: cfg.Member.Value,
				Note: "mine", Paths: []string{"parser.go"}, CreatedAt: claims[0].CreatedAt,
			}
			if diff := cmp.Diff(want, claims[0]); diff != "" {
				t.Fatalf("the claim, -want +got:\n%s", diff)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the claim never landed: %+v. what was rendered:\n%s", claims, out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	quit(t, program, done)
}

// TestAckFromTheViewEndToEnd is the only key that moves a cursor, which is the thing being open must never do.
func TestAckFromTheViewEndToEnd(t *testing.T) {
	s, cfg := testStore(t)
	ctx := t.Context()
	if _, err := s.Post(ctx, store.PostRequest{
		Channel: testChannel, Thread: "parser-panic", Author: "alice", Body: "empty input reaches the token loop",
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}

	program, _, out, done := start(t, s, cfg, time.Minute)
	waitFor(t, out, "parser-panic")

	// Unread until the key says otherwise: the messages have been on screen this whole time.
	threads, err := s.Threads(ctx, store.ThreadsRequest{Channel: testChannel, Member: cfg.Member.Value, Observe: true})
	if err != nil {
		t.Fatalf("Threads(): %v", err)
	}
	if len(threads) != 1 || threads[0].Unread != 1 {
		t.Fatalf("threads before the ack = %+v, want one unread", threads)
	}

	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	waitFor(t, out, "marked parser-panic read")

	threads, err = s.Threads(ctx, store.ThreadsRequest{Channel: testChannel, Member: cfg.Member.Value, Observe: true})
	if err != nil {
		t.Fatalf("Threads(): %v", err)
	}
	if len(threads) != 1 || threads[0].Unread != 0 {
		t.Errorf("threads after the ack = %+v, want nothing unread", threads)
	}
	quit(t, program, done)
}

// memoryLayout is a saved layout with no file behind it. Locked because the program saves from its own
// goroutine while the test reads, which -race is there to notice.
type memoryLayout struct {
	mu      sync.Mutex
	layout  config.Layout
	loadErr error
	saveErr error
	saves   int
}

func (m *memoryLayout) Load() (config.Layout, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.layout, m.loadErr
}

func (m *memoryLayout) Save(layout config.Layout) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	m.layout, m.saves = layout, m.saves+1
	return nil
}

func (m *memoryLayout) state() (config.Layout, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.layout, m.saves
}

// TestTheViewOpensAtTheWidthsItWasLeftAt is the point of saving them at all. The first frame is already the
// width it was left at, rather than the default with a flash of the saved one after.
func TestTheViewOpensAtTheWidthsItWasLeftAt(t *testing.T) {
	s, cfg := testStore(t)
	if _, err := s.Post(t.Context(), store.PostRequest{
		Channel: testChannel, Thread: "parser-panic", Author: "alice", Body: "empty input reaches the token loop",
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}

	saved := config.Layout{Channels: 24, Members: 30}
	program, _, out, done := startWith(t, s, cfg, time.Minute, &memoryLayout{layout: saved})
	// The channels title padded to 24 rather than to the 16 it defaults to.
	waitFor(t, out, "[CHANNELS]              |")
	// And the roster, which is the one this test used to miss. Init applies the layout before bubbletea reports a
	// size, so the clamp runs at the 80x24 fallback: a saved channels width of 24 still fits there and came back
	// right, while the roster is not a column at all at 80 and came back at 8. Asserted through the divider left
	// of it, since it is the last column and has nothing padded after it: the message title padded to the 39 the
	// roster leaves rather than to the 49 a defaulted one would.
	waitFor(t, out, " parser-panic"+strings.Repeat(" ", 26)+"| MEMBERS")
	quit(t, program, done)
}

// TestADragSavesTheWidths is the other half, and it is saved on release rather than on every motion: a drag is
// dozens of events and the width that matters is where the pointer let go.
func TestADragSavesTheWidths(t *testing.T) {
	s, cfg := testStore(t)
	if _, err := s.Post(t.Context(), store.PostRequest{
		Channel: testChannel, Thread: "parser-panic", Author: "alice", Body: "empty input reaches the token loop",
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}
	saved := &memoryLayout{}

	program, keys, out, done := startWith(t, s, cfg, time.Minute, saved)
	waitFor(t, out, "[CHANNELS]      |")
	// Two motions, so the count can tell saving on release from saving on every event: one save either way if
	// the drag only moves once, because a width that did not change is not written.
	for _, sequence := range []string{
		"\x1b[<0;17;5M",  // press
		"\x1b[<32;21;5M", // through
		"\x1b[<32;23;5M", // and on
		"\x1b[<0;23;5m",  // release
	} {
		if _, err := io.WriteString(keys, sequence); err != nil {
			t.Fatalf("write %q: %v", sequence, err)
		}
	}
	waitFor(t, out, "[CHANNELS]            |")

	deadline := time.Now().Add(5 * time.Second)
	for {
		layout, saves := saved.state()
		if layout.Channels == 22 {
			if saves != 1 {
				t.Errorf("saved %d times for one drag, want once, on the release", saves)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the drag was never saved: %+v after %d saves", layout, saves)
		}
		time.Sleep(10 * time.Millisecond)
	}
	quit(t, program, done)
}

// TestOpeningTheViewDoesNotOverwriteTheSavedWidths is the other half of the reported bug, and the half that made
// it stick: a click that dragged nothing used to write the file. Init applied the layout at the 80x24 fallback,
// where the roster has 8 columns of room, so what the model held afterwards was 8 rather than the 30 in the file,
// and the first release wrote that 8 back. One reopen and a click was enough to lose the width for good.
func TestOpeningTheViewDoesNotOverwriteTheSavedWidths(t *testing.T) {
	s, cfg := testStore(t)
	if _, err := s.Post(t.Context(), store.PostRequest{
		Channel: testChannel, Thread: "parser-panic", Author: "alice", Body: "empty input reaches the token loop",
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}
	want := config.Layout{Members: 30}
	saved := &memoryLayout{layout: want}

	program, keys, out, done := startWith(t, s, cfg, time.Minute, saved)
	waitFor(t, out, "[CHANNELS]      |")
	// Press and release on the same column: a click, with no motion between them and so no new width to save.
	for _, sequence := range []string{"\x1b[<0;17;5M", "\x1b[<0;17;5m"} {
		if _, err := io.WriteString(keys, sequence); err != nil {
			t.Fatalf("write %q: %v", sequence, err)
		}
	}
	quit(t, program, done)

	if layout, saves := saved.state(); layout != want || saves != 0 {
		t.Errorf("the file holds %+v after %d saves, want %+v and never written", layout, saves, want)
	}
}

// TestDraggingADividerThroughTheProgram is the wiring the model tests cannot see. They call Update directly, so
// every one of them passed while program.Update dropped mouse events before the model ever saw them: reported as
// "I'm not able to drag the borders".
//
// It writes the terminal's own escape sequences rather than sending a MouseMsg, so bubbletea's parser and its
// coordinate base are inside the test too. SGR mouse reports columns from 1 and adds 32 to the button for motion,
// where a MouseMsg counts from 0, and that off-by-one is the other way this silently does nothing.
func TestDraggingADividerThroughTheProgram(t *testing.T) {
	s, cfg := testStore(t)
	if _, err := s.Post(t.Context(), store.PostRequest{
		Channel: testChannel, Thread: "parser-panic", Author: "alice", Body: "empty input reaches the token loop",
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}

	program, keys, out, done := start(t, s, cfg, time.Minute)
	// The channels pane starts 16 wide, so its title is padded to there and the divider follows it.
	waitFor(t, out, "[CHANNELS]      |")

	// Column 17 in the terminal's counting is the divider at 16 in the model's.
	for _, sequence := range []string{
		"\x1b[<0;17;5M",  // press, left button
		"\x1b[<32;23;5M", // drag six columns right, button still down
		"\x1b[<0;23;5m",  // release
	} {
		if _, err := io.WriteString(keys, sequence); err != nil {
			t.Fatalf("write %q: %v", sequence, err)
		}
	}
	waitFor(t, out, "[CHANNELS]            |")
	quit(t, program, done)
}

// TestRemovingAMemberFromTheViewEndToEnd drives d in the roster against a real store. The claim is the part
// worth covering: the question said it would be handed back, so answering it has to actually hand it back.
func TestRemovingAMemberFromTheViewEndToEnd(t *testing.T) {
	s, cfg := testStore(t)
	ctx := t.Context()
	if _, err := s.Post(ctx, store.PostRequest{
		Channel: testChannel, Thread: "parser-panic", Author: "ghost", Body: "empty input reaches the token loop",
	}); err != nil {
		t.Fatalf("Post(): %v", err)
	}
	if _, err := s.Claim(ctx, store.ClaimRequest{
		Channel: testChannel, Thread: "parser-panic", Holder: "ghost", Note: "left this behind",
	}); err != nil {
		t.Fatalf("Claim(): %v", err)
	}

	program, _, out, done := start(t, s, cfg, time.Minute)
	waitFor(t, out, "ghost")

	// Out to the roster and onto ghost, who is the only member: the watcher is not in it, since watching does
	// not join.
	for range 3 {
		program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	}
	waitFor(t, out, "[MEMBERS]")
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	waitFor(t, out, "remove ghost")
	waitFor(t, out, "releasing parser-panic")

	// Still there while the question stands.
	if members, err := s.Members(ctx, testChannel); err != nil || len(members) != 1 {
		t.Fatalf("the roster while confirming = %+v, err %v, want ghost still there", members, err)
	}

	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	deadline := time.Now().Add(10 * time.Second)
	for {
		members, err := s.Members(ctx, testChannel)
		if err != nil {
			t.Fatalf("Members(): %v", err)
		}
		claims, err := s.Claims(ctx, testChannel)
		if err != nil {
			t.Fatalf("Claims(): %v", err)
		}
		if len(members) == 0 && len(claims) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ghost is still here: members %+v, claims %+v. what was rendered:\n%s",
				members, claims, out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// What they said outlives them.
	messages, err := s.Messages(ctx, store.MessagesRequest{Channel: testChannel, Thread: "parser-panic"})
	if err != nil {
		t.Fatalf("Messages(): %v", err)
	}
	if len(messages) != 1 || messages[0].Author != "ghost" {
		t.Errorf("the record after removing a member = %+v, want ghost's message still there", messages)
	}
	quit(t, program, done)
}
