package tui

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chancez/agora/internal/config"
	"github.com/chancez/agora/internal/store"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/go-cmp/cmp"
	"github.com/muesli/termenv"
)

// diffWork compares the whole request a keystroke produced. A request with the right action and the wrong
// thread is exactly the bug worth catching, so no test here picks at one field of it.
func diffWork(got, want pendingWork) string {
	return cmp.Diff(want, got, cmp.AllowUnexported(pendingWork{}))
}

// typed is text arriving from a terminal, which is how a run of runes arrives: as one event rather than one
// per rune, since that is what a paste is.
func typed(text string) tea.KeyMsg {
	if text == " " {
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(text)}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)}
}

// typing is a whole line, one keystroke per rune, which is what typing it is.
func typing(text string) []any {
	var keys []any
	for _, r := range text {
		keys = append(keys, typed(string(r)))
	}
	return keys
}

// keystroke is one key by name, for a test that iterates over a list of them.
func keystroke(name string) tea.KeyMsg {
	if len([]rune(name)) == 1 {
		return typed(name)
	}
	return press(name)
}

// press is a key that is not text. Named the way bubbletea names them, so a test reads like the keyboard.
func press(name string) tea.KeyMsg {
	named := map[string]tea.KeyType{
		" ":     tea.KeySpace,
		"enter": tea.KeyEnter, "esc": tea.KeyEsc, "tab": tea.KeyTab, "shift+tab": tea.KeyShiftTab,
		"backspace": tea.KeyBackspace, "delete": tea.KeyDelete,
		"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
		"home": tea.KeyHome, "end": tea.KeyEnd,
		"ctrl+c": tea.KeyCtrlC, "ctrl+u": tea.KeyCtrlU, "ctrl+w": tea.KeyCtrlW,
		"ctrl+a": tea.KeyCtrlA, "ctrl+e": tea.KeyCtrlE,
	}
	t, ok := named[name]
	if !ok {
		panic("press: no such key " + name)
	}
	return tea.KeyMsg{Type: t}
}

// TestMain strips styling. lipgloss decides on colour from the environment, so without this the frame a test
// asserts on depends on whose terminal ran it.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

var now = time.Date(2026, 8, 23, 14, 30, 0, 0, time.UTC)

func testConfig() config.Config {
	return config.Config{
		Database: config.Value{Value: "/tmp/agoradev.x9k/agora.db", Source: "$AGORA_DB"},
		Channel:  config.Value{Value: "/Users/c/projects/agora", Source: "repository"},
		Member:   config.Value{Value: "parser", Source: "worktree name"},
	}
}

func testModel(width, height int, msgs ...any) Model {
	m := New(testConfig(), func() time.Time { return now })
	m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	for _, msg := range msgs {
		m, _ = m.Update(msg)
	}
	return m
}

// message takes both numbers, because that is how they arrive: seq counts within the channel and number within
// the thread, so a thread's second message is #2 whatever the channel was doing meanwhile.
func message(seq, number int64, channel, thread, author, body string, minutesAgo int) store.Message {
	return store.Message{
		Seq: seq, Number: number, Channel: channel, Thread: thread, Author: author, Body: body,
		CreatedAt: now.Add(-time.Duration(minutesAgo) * time.Minute),
	}
}

// fullState is three channels, three threads in the selected one, and a claimed thread with messages in it:
// the shape the view exists to show.
func fullState() (snapshotMsg, messagesMsg) {
	const channel = "/Users/c/projects/agora"
	snapshot := snapshotMsg{
		channels: []store.Channel{
			{Key: channel, Threads: 3, UnreadThreads: 2},
			{Key: "/Users/c/projects/cm", Threads: 4, UnreadThreads: 1},
			{Key: "/Users/c/dotfiles", Threads: 1},
		},
		threads: []store.Thread{
			{Channel: channel, Name: "parser-panic", Unread: 2, Messages: 2, LastAt: now.Add(-2 * time.Minute), Claim: "alice"},
			{Channel: channel, Name: "docs-rewrite", Unread: 1, Messages: 1, LastAt: now.Add(-9 * time.Minute)},
			{Channel: channel, Name: "release-1-2", Messages: 3, LastAt: now.Add(-90 * time.Minute)},
		},
		claims: []store.Claim{{
			Channel: channel, Thread: "parser-panic", Holder: "alice",
			Note:      "root cause is in the token loop, do not patch the symptom",
			Paths:     []string{"parser.go", "internal/lex/**"},
			CreatedAt: now.Add(-4 * time.Minute),
		}},
		members: []store.Member{
			{Channel: channel, Name: "alice", SeenAt: now.Add(-30 * time.Second),
				Description: "the lexer's bounds checks, all three call sites",
				Worktree:    "/Users/c/projects/agora/.worktrees/lexer"},
			{Channel: channel, Name: "parser", Unread: 3, UnreadThreads: 2, SeenAt: now.Add(-3 * time.Second),
				Worktree: "/Users/c/projects/agora/.worktrees/pr-chancez-parser-panic"},
		},
	}
	messages := messagesMsg{
		channel: channel,
		thread:  "parser-panic",
		messages: []store.Message{
			message(12, 1, channel, "parser-panic", "alice", "empty input reaches the token loop with no bounds check", 9),
			message(14, 2, channel, "parser-panic", "bob", "confirmed, the lexer indexes it the same way", 2),
		},
	}
	return snapshot, messages
}

// TestView asserts the whole frame rather than picking at lines, because the layout is the thing: a height
// that drifts by one line or a column that stops aligning is exactly what a field-by-field check reads past.
func TestView(t *testing.T) {
	snapshot, messages := fullState()
	got := testModel(96, 18, snapshot, messages).View()
	if got != wantFrame {
		t.Errorf("View():\n%s\n\nwant:\n%s", boxed(got), boxed(wantFrame))
	}
}

// boxed makes trailing space and blank padding visible in a failure, which is where frame diffs hide.
func boxed(frame string) string {
	var b strings.Builder
	for i, line := range strings.Split(frame, "\n") {
		fmt.Fprintf(&b, "%2d |%s|\n", i+1, line)
	}
	return b.String()
}

func TestTheSelectedChannelStartsAsTheOneYouAreIn(t *testing.T) {
	snapshot, _ := fullState()
	m := testModel(96, 18, snapshot)

	channel, thread := m.Selection()
	if channel != testConfig().Channel.Value {
		t.Errorf("selected channel = %q, want the one from config", channel)
	}
	// And its most recently active thread, because a view showing a channel and no thread has an empty pane
	// for no reason.
	if thread != "parser-panic" {
		t.Errorf("selected thread = %q, want the most recently active", thread)
	}
}

func TestMovingBetweenPanesAndSelections(t *testing.T) {
	snapshot, messages := fullState()
	base := testModel(96, 18, snapshot, messages)

	for _, tc := range []struct {
		name        string
		keys        []string
		wantChannel string
		wantThread  string
	}{
		{
			name:        "down the thread list",
			keys:        []string{"l", "j"},
			wantChannel: "/Users/c/projects/agora",
			wantThread:  "docs-rewrite",
		},
		{
			name:        "and back up",
			keys:        []string{"l", "j", "k"},
			wantChannel: "/Users/c/projects/agora",
			wantThread:  "parser-panic",
		},
		{
			name: "into another channel, which clears the thread until it loads",
			keys: []string{"j"},
			// Selecting a different project cannot keep a thread from the last one.
			wantChannel: "/Users/c/projects/cm",
			wantThread:  "",
		},
		{
			name:        "the end of the thread list",
			keys:        []string{"l", "G"},
			wantChannel: "/Users/c/projects/agora",
			wantThread:  "release-1-2",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := base
			for _, key := range tc.keys {
				m, _ = m.Update(keystroke(key))
			}
			channel, thread := m.Selection()
			if channel != tc.wantChannel || thread != tc.wantThread {
				t.Errorf("selection = %q / %q, want %q / %q", channel, thread, tc.wantChannel, tc.wantThread)
			}
		})
	}
}

// TestTheSelectionFollowsNamesNotRows is why the selection is stored as a name. Threads are ordered by when
// they last moved, so anybody posting reorders the sidebar, and a view holding an index would silently jump
// to a different thread underneath the reader.
func TestTheSelectionFollowsNamesNotRows(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)
	m, _ = m.Update(typed("l"))
	m, _ = m.Update(typed("j"))
	if _, thread := m.Selection(); thread != "docs-rewrite" {
		t.Fatalf("selected %q, want docs-rewrite", thread)
	}

	// release-1-2 becomes the most recently active, so every row moves.
	reordered := snapshot
	reordered.threads = []store.Thread{snapshot.threads[2], snapshot.threads[0], snapshot.threads[1]}
	m, _ = m.Update(reordered)
	if _, thread := m.Selection(); thread != "docs-rewrite" {
		t.Errorf("selected %q after a reorder, want docs-rewrite still", thread)
	}
}

func TestASelectedThreadThatDisappearsSettlesSomewhereReal(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)

	// The thread is gone from the next reload, which happens when it was only there as a claim and the claim
	// was released.
	gone := snapshot
	gone.threads = snapshot.threads[1:]
	m, _ = m.Update(gone)
	if _, thread := m.Selection(); thread != "docs-rewrite" {
		t.Errorf("selected %q after the selection vanished, want the first thread", thread)
	}
}

func TestMessagesForAThreadYouHaveLeftAreIgnored(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)
	m, _ = m.Update(typed("l"))
	m, _ = m.Update(typed("j"))

	// In flight when the selection moved. Landing it would show one thread's messages under another's name.
	m, _ = m.Update(messages)
	if strings.Contains(m.View(), "token loop") {
		t.Errorf("a stale load landed in the wrong thread:\n%s", m.View())
	}
	// The same goes for the watch.
	m, _ = m.Update(messageMsg{message: message(20, 3, "/Users/c/projects/agora", "parser-panic", "alice", "still parser", 0)})
	if strings.Contains(m.View(), "still parser") {
		t.Errorf("the watch delivered into the wrong thread:\n%s", m.View())
	}
}

func TestANewMessageWhileFollowingArrivesOnScreen(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)

	m, _ = m.Update(messageMsg{message: message(20, 3, "/Users/c/projects/agora", "parser-panic", "carol", "posted just now", 0)})
	if !strings.Contains(m.View(), "posted just now") {
		t.Errorf("the new message is not on screen:\n%s", m.View())
	}
	if strings.Contains(m.View(), "below") {
		t.Error("the frame reports unseen messages while following")
	}
}

// TestANewMessageWhileReadingBackIsCounted keeps a live log from yanking the view out from under somebody
// reading history, while still saying that it grew.
func TestANewMessageWhileReadingBackIsCounted(t *testing.T) {
	snapshot, messages := fullState()
	long := messages
	for i := range 30 {
		long.messages = append(long.messages,
			message(int64(100+i), int64(3+i), messages.channel, messages.thread, "alice", fmt.Sprintf("finding %d", i), 1))
	}
	m := testModel(96, 18, snapshot, long)
	for _, key := range []string{"l", "l", "g"} {
		m, _ = m.Update(keystroke(key))
	}
	before := m.View()

	m, _ = m.Update(messageMsg{message: message(200, 33, messages.channel, messages.thread, "carol", "landed while reading", 0)})
	if !strings.Contains(m.View(), "1 new message below") {
		t.Errorf("the frame does not say what arrived:\n%s", m.View())
	}
	if strings.Contains(m.View(), "landed while reading") {
		t.Error("the view jumped to the new message while paused")
	}
	if m.View() == before {
		t.Error("the frame did not change at all")
	}

	m, _ = m.Update(typed("G"))
	if !strings.Contains(m.View(), "landed while reading") {
		t.Errorf("G did not return to the newest:\n%s", m.View())
	}
	if strings.Contains(m.View(), "below") {
		t.Error("the unseen count survived returning to the bottom")
	}
}

func TestAClaimShowsAsTheThreadsHeader(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)

	// A claim is about a thread, so it reads as that thread's header rather than as a block somewhere else.
	// Checked in pieces that do not straddle the wrap, since the note is wrapped to the pane width.
	for _, want := range []string{"held by alice", "parser.go, internal/lex/**", "root cause is in the token loop"} {
		if !strings.Contains(m.View(), want) {
			t.Errorf("the frame is missing %q:\n%s", want, m.View())
		}
	}
}

func TestANarrowTerminalShowsOnlyTheFocusedPane(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(60, 18, snapshot, messages)

	// Three columns in sixty characters is three unreadable columns, so the focused one takes the screen.
	if !strings.Contains(m.View(), "CHANNELS") {
		t.Errorf("the focused pane is not drawn at 60 columns:\n%s", m.View())
	}
	if strings.Contains(m.View(), "token loop") {
		t.Errorf("an unfocused pane is drawn at 60 columns:\n%s", m.View())
	}
	for _, key := range []string{"l", "l"} {
		m, _ = m.Update(keystroke(key))
	}
	if !strings.Contains(m.View(), "token loop") {
		t.Errorf("moving focus to the messages did not show them:\n%s", m.View())
	}
}

// TestTheFrameIsRectangular is a layout invariant rather than a nicety: a line that overflows wraps, which
// changes the height of everything below it, so the panes would be cut by an unpredictable amount.
func TestTheFrameIsRectangular(t *testing.T) {
	snapshot, messages := fullState()
	for _, width := range []int{40, 60, 84, 96, 200} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := testModel(width, 16, snapshot, messages)
			lines := strings.Split(m.View(), "\n")
			if len(lines) != 16 {
				t.Errorf("frame is %d lines at width %d, want 16", len(lines), width)
			}
			for i, line := range lines {
				if w := lipgloss.Width(line); w > width {
					t.Errorf("line %d is %d wide at width %d: %q", i+1, w, width, line)
				}
			}
		})
	}
}

func TestAFailedReloadKeepsWhatWasOnScreen(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)
	m, _ = m.Update(errMsg{err: errors.New("database is locked")})

	view := m.View()
	if !strings.Contains(view, "error: database is locked") {
		t.Errorf("the failure is not reported:\n%s", view)
	}
	if !strings.Contains(view, "token loop") {
		t.Errorf("the last snapshot was thrown away:\n%s", view)
	}
	m, _ = m.Update(snapshot)
	if strings.Contains(m.View(), "error:") {
		t.Error("the error survived a successful reload")
	}
}

func TestBeforeTheFirstLoad(t *testing.T) {
	m := testModel(96, 18)
	if !strings.Contains(m.View(), "loading") {
		t.Errorf("the view does not say it is still loading:\n%s", m.View())
	}
	// A terminal that never reports a size, or reports 0x0 as a pty with no controlling terminal does, still
	// gets a frame. Measured under script(1), where returning empty meant a permanently blank screen.
	for _, tc := range []struct {
		name string
		m    Model
	}{
		{name: "no size event at all", m: New(testConfig(), func() time.Time { return now })},
		{name: "a terminal reporting zero", m: testModel(0, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if lines := strings.Split(tc.m.View(), "\n"); len(lines) != defaultHeight {
				t.Errorf("frame is %d lines, want the %d line fallback", len(lines), defaultHeight)
			}
			if !strings.Contains(tc.m.View(), "agora") {
				t.Errorf("no frame rendered:\n%s", tc.m.View())
			}
		})
	}
}

func TestQuitKeys(t *testing.T) {
	for _, key := range []string{"q", "ctrl+c", "esc"} {
		t.Run(key, func(t *testing.T) {
			if _, quit := testModel(96, 18).Update(keystroke(key)); !quit {
				t.Errorf("%q did not quit", key)
			}
		})
	}
	if _, quit := testModel(96, 18).Update(typed("x")); quit {
		t.Error("an unbound key quit")
	}
}

func TestHumanAgo(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{d: -time.Second, want: "just now"},
		{d: 3 * time.Second, want: "3s ago"},
		{d: 90 * time.Second, want: "1m ago"},
		{d: 4 * time.Hour, want: "4h ago"},
		{d: 50 * time.Hour, want: "2d ago"},
	} {
		if got := humanAgo(tc.d); got != tc.want {
			t.Errorf("humanAgo(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

const wantFrame = `agora /Users/c/projects/agora                                                        you: parser
db /tmp/agoradev.x9k/agora.db ($AGORA_DB)                           3 threads  3 messages unread
[CHANNELS]      | THREADS                | parser-panic                    | MEMBERS
>2* agora       |>2* parser-panic        | held by alice for 4m            |     alice
 1* cm          | 1* docs-rewrite        | parser.go, internal/lex/**      |>2/3 parser
    dotfiles    |    release-1-2         | root cause is in the token loop,|
                |                        | do not patch the symptom        |
                |                        | #1  alice  14:21:00             |
                |                        |     empty input reaches the     |
                |                        |     token loop with no bounds   |
                |                        |     check                       |
                |                        | #2  bob  14:28:00               |
                |                        |     confirmed, the lexer indexes|
                |                        |     it the same way             |
                |                        |                                 |
------------------------------------------------------------------------------------------------
parser  2 threads / 3 messages unread  seen 3s ago  (you)  ....worktrees/pr-chancez-parser-panic
q quit  j/k move  h/l pane  p post here  n new thread  ? keys  [channels]                       `

// TestMovementKeysDoSomethingOnAnEmptyChannel is the bug as reported: "I cannot go up or down at all". A
// database with channels and nothing posted in them, which is what a fresh install looks like, left the
// keyboard on the messages pane where there was nothing to scroll, so every movement key did nothing and the
// view read as broken.
func TestMovementKeysDoSomethingOnAnEmptyChannel(t *testing.T) {
	empty := snapshotMsg{
		channels: []store.Channel{
			{Key: "/Users/c/projects/agora"},
			{Key: "/Users/c/projects/cm"},
			{Key: "/Users/c/dotfiles"},
		},
	}
	m := testModel(96, 18, empty)

	for _, key := range []string{"j", "down"} {
		before, _ := m.Selection()
		m, _ = m.Update(keystroke(key))
		after, _ := m.Selection()
		if after == before {
			t.Errorf("%q moved nothing on a channel with nothing posted, from %q", key, before)
		}
	}
	for _, key := range []string{"k", "up"} {
		before, _ := m.Selection()
		m, _ = m.Update(keystroke(key))
		if after, _ := m.Selection(); after == before {
			t.Errorf("%q moved nothing, from %q", key, before)
		}
	}
	// And the frame says where the keyboard is, so a pane that has nothing to move within explains itself.
	if !strings.Contains(m.View(), "[channels]") {
		t.Errorf("the footer does not name the focused pane:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "[CHANNELS]") {
		t.Errorf("the focused pane is not marked:\n%s", m.View())
	}
}

// TestArrowKeysMatchTheLetters keeps the two bindings from drifting apart, since the letters are what get
// used and the arrows are what get reached for first.
func TestArrowKeysMatchTheLetters(t *testing.T) {
	snapshot, messages := fullState()
	for _, pair := range []struct{ letter, arrow string }{
		{"j", "down"}, {"k", "up"}, {"l", "right"}, {"h", "left"},
	} {
		t.Run(pair.letter+" and "+pair.arrow, func(t *testing.T) {
			letters := testModel(96, 18, snapshot, messages)
			arrows := testModel(96, 18, snapshot, messages)
			// Twice, so a binding that only works from the starting state still has to agree.
			for range 2 {
				letters, _ = letters.Update(keystroke(pair.letter))
				arrows, _ = arrows.Update(keystroke(pair.arrow))
			}
			if letters.View() != arrows.View() {
				t.Errorf("%q and %q produce different frames:\n%s\n\n%s",
					pair.letter, pair.arrow, boxed(letters.View()), boxed(arrows.View()))
			}
		})
	}
}

// TestDeleteAsksBeforeItDeletes is the whole safety of the d key. The question has to carry the numbers,
// because "delete this thread?" is not a question anybody can answer, and anything other than y has to mean no.
func TestDeleteAsksBeforeItDeletes(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)
	// Onto the threads: d acts on whatever the keyboard is on, and the view opens on the channels, where it
	// deletes the channel instead.
	m, _ = m.Update(typed("l"))

	m, _ = m.Update(typed("d"))
	if diff := diffWork(m.Pending(), pendingWork{action: actionPreviewDelete, thread: "parser-panic"}); diff != "" {
		t.Fatalf("d asked for the wrong thing, -want +got:\n%s", diff)
	}
	// Nothing has been asked of the store beyond the preview, and nothing on screen has changed yet.
	if strings.Contains(m.View(), "delete parser-panic") {
		t.Error("the question was asked before the numbers came back")
	}

	m = m.clearPending()
	m, _ = m.Update(previewMsg{
		channel: "/Users/c/projects/agora", thread: "parser-panic", messages: 2, cursors: 1, claim: "alice",
	})
	view := m.View()
	for _, want := range []string{"delete parser-panic", "2 messages", "1 cursor", "claimed by alice", "[y/n]"} {
		if !strings.Contains(view, want) {
			t.Errorf("the question is missing %q:\n%s", want, view)
		}
	}

	yes, _ := m.Update(typed("y"))
	if diff := diffWork(yes.Pending(), pendingWork{action: actionDelete, thread: "parser-panic", force: true}); diff != "" {
		t.Errorf("y asked for the wrong thing, -want +got:\n%s", diff)
	}
}

func TestAnythingButYesIsNo(t *testing.T) {
	snapshot, messages := fullState()
	base := testModel(96, 18, snapshot, messages)
	base, _ = base.Update(previewMsg{channel: "/Users/c/projects/agora", thread: "parser-panic", messages: 2})

	// A keystroke meant for the view must never be read as consent, so every key except y cancels rather
	// than falling through to its usual meaning.
	for _, key := range []string{"n", "esc", "j", "k", "l", "h", "d", "G", "f", "x"} {
		t.Run(key, func(t *testing.T) {
			m, quit := base.Update(keystroke(key))
			if quit {
				t.Errorf("%q quit during a confirmation", key)
			}
			if work := m.Pending(); work.action != actionNone {
				t.Errorf("%q asked the store for %+v", key, work)
			}
			if strings.Contains(m.View(), "[y/n]") {
				t.Errorf("%q left the question on screen", key)
			}
		})
	}
	// Movement is ignored rather than queued, so the selection cannot drift under the question.
	m, _ := base.Update(typed("j"))
	if _, thread := m.Selection(); thread != "parser-panic" {
		t.Errorf("the selection moved to %q during a confirmation", thread)
	}
}

func TestAConfirmationForAThreadYouHaveLeftIsDropped(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)
	m, _ = m.Update(typed("l"))
	m, _ = m.Update(typed("j"))

	// In flight while the selection moved. Asking about one thread and deleting another is the worst
	// possible outcome for this key.
	m, _ = m.Update(previewMsg{channel: "/Users/c/projects/agora", thread: "parser-panic", messages: 2})
	if strings.Contains(m.View(), "[y/n]") {
		t.Errorf("a stale question was asked:\n%s", m.View())
	}
}

func TestDeleteDoesNothingWithNoThreadSelected(t *testing.T) {
	m := testModel(96, 18, snapshotMsg{channels: []store.Channel{{Key: "/Users/c/projects/agora"}}})
	m, _ = m.Update(typed("l"))

	m, _ = m.Update(typed("d"))
	if work := m.Pending(); work.action != actionNone {
		t.Errorf("d asked for %+v with nothing selected", work)
	}
}

// TestPostingFromTheView is the key the whole thing turns on: p, type it, enter. It also covers the property
// that makes a prompt safe to open at all, since the message here contains a d, a q, and a j: while a prompt is
// open every key that is not an editing key is a letter, so typing cannot navigate, delete, or quit.
func TestPostingFromTheView(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)

	m, _ = m.Update(typed("p"))
	if !strings.Contains(m.View(), "post to parser-panic | ") {
		t.Fatalf("p did not open a prompt naming where it would go:\n%s", boxed(m.View()))
	}
	const body = "done, quit judging the lexer"
	for _, key := range typing(body) {
		var quit bool
		m, quit = m.Update(key)
		if quit {
			t.Fatalf("typing %v quit the view", key)
		}
	}

	// On screen as it is typed, and nothing asked of the store yet.
	if !strings.Contains(m.View(), body) {
		t.Errorf("what was typed is not on screen:\n%s", boxed(m.View()))
	}
	if work := m.Pending(); work.action != actionNone {
		t.Errorf("typing asked the store for %+v", work)
	}
	if _, thread := m.Selection(); thread != "parser-panic" {
		t.Errorf("typing moved the selection to %q", thread)
	}

	m, _ = m.Update(press("enter"))
	if diff := diffWork(m.Pending(), pendingWork{action: actionPost, thread: "parser-panic", body: body}); diff != "" {
		t.Errorf("enter asked for the wrong thing, -want +got:\n%s", diff)
	}
	if strings.Contains(m.View(), "post to parser-panic | ") {
		t.Errorf("the prompt is still open after enter:\n%s", boxed(m.View()))
	}
}

// TestStartingANewThread is the other half of posting: a thread nobody has posted to yet cannot be selected, so
// its name has to be typed before the message.
func TestStartingANewThread(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)

	m, _ = m.Update(typed("n"))
	if !strings.Contains(m.View(), "new thread | ") {
		t.Fatalf("n did not ask for a name:\n%s", boxed(m.View()))
	}
	for _, key := range typing("release-1-3") {
		m, _ = m.Update(key)
	}
	m, _ = m.Update(press("enter"))

	// Naming it is not posting it: the first message is still to come, and the prompt says which thread it is
	// going to rather than leaving that to be remembered.
	if work := m.Pending(); work.action != actionNone {
		t.Errorf("naming a thread asked the store for %+v", work)
	}
	if !strings.Contains(m.View(), "post to release-1-3 | ") {
		t.Fatalf("the second prompt does not name the new thread:\n%s", boxed(m.View()))
	}
	for _, key := range typing("cutting it tomorrow") {
		m, _ = m.Update(key)
	}
	m, _ = m.Update(press("enter"))

	want := pendingWork{action: actionPost, thread: "release-1-3", body: "cutting it tomorrow"}
	if diff := diffWork(m.Pending(), want); diff != "" {
		t.Errorf("the post went to the wrong place, -want +got:\n%s", diff)
	}
}

// TestPostWithNothingSelectedAsksForAName keeps p from being a dead key on an empty channel, which is what a
// fresh install looks like and is the same report the focus bug produced.
func TestPostWithNothingSelectedAsksForAName(t *testing.T) {
	m := testModel(96, 18, snapshotMsg{channels: []store.Channel{{Key: "/Users/c/projects/agora"}}})

	m, _ = m.Update(typed("p"))
	if !strings.Contains(m.View(), "new thread | ") {
		t.Errorf("p on an empty channel did nothing visible:\n%s", boxed(m.View()))
	}
}

func TestLeavingAPromptWithoutPosting(t *testing.T) {
	snapshot, messages := fullState()
	base := testModel(96, 18, snapshot, messages)

	for _, tc := range []struct {
		name string
		keys []any
	}{
		// Esc is the way out, and enter on an empty line is the other one: nothing has been typed to lose, and
		// a key that appears dead is worse than one that closes the prompt.
		{name: "esc part way through", keys: append(typing("never mi"), press("esc"))},
		{name: "enter on an empty line", keys: []any{press("enter")}},
		{name: "everything typed then erased", keys: append(typing("ab"), press("ctrl+u"), press("enter"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := base.Update(typed("p"))
			for _, key := range tc.keys {
				m, _ = m.Update(key)
			}
			if work := m.Pending(); work.action != actionNone {
				t.Errorf("asked the store for %+v", work)
			}
			if strings.Contains(m.View(), "post to parser-panic | ") {
				t.Errorf("the prompt is still open:\n%s", boxed(m.View()))
			}
		})
	}
}

// TestWhatCountsAsTextInAPrompt: the editing keys are the ones bubbletea names, and everything it reports as
// runes is text. An alt combination is not, or alt+p would type a p.
func TestWhatCountsAsTextInAPrompt(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)
	m, _ = m.Update(typed("p"))

	for _, tc := range []struct {
		name string
		key  tea.KeyMsg
		want string
	}{
		{name: "a letter", key: typed("d"), want: "d"},
		{name: "a space", key: press(" "), want: ""},
		{name: "an alt combination is not text", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p"), Alt: true}, want: ""},
		{name: "a named key is not text", key: press("tab"), want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := m.Update(tc.key)
			if line := got.compose.line.Value(); line != tc.want {
				t.Errorf("typing %v put %q in the prompt, want %q", tc.key, line, tc.want)
			}
		})
	}
}

// TestCtrlCQuitsFromAPrompt is the one key that has to work everywhere, since a prompt that swallows it is a
// window you cannot close.
func TestCtrlCQuitsFromAPrompt(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)
	m, _ = m.Update(typed("p"))

	if _, quit := m.Update(press("ctrl+c")); !quit {
		t.Error("ctrl+c did not quit from a prompt")
	}
}

// TestAPostSelectsWhereItWent: posting into a thread you were not looking at and being left looking at the old
// one reads as the post having gone nowhere.
func TestAPostSelectsWhereItWent(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)

	m, _ = m.Update(postedMsg{message: message(20, 1, "/Users/c/projects/agora", "release-1-3", "parser", "cutting it tomorrow", 0)})

	if _, thread := m.Selection(); thread != "release-1-3" {
		t.Errorf("selected thread = %q, want the one just posted to", thread)
	}
	// And the message itself, without waiting for the reload: the watch and the reload will both bring it in
	// again, and the model drops what it already has.
	if !strings.Contains(m.View(), "cutting it tomorrow") {
		t.Errorf("the posted message is not on screen:\n%s", boxed(m.View()))
	}
}

// TestTabCyclesBothWays is the pair. One direction is a cycle you have to go all the way round to undo, which
// is four presses to get back one pane.
func TestTabCyclesBothWays(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)

	forward := []pane{paneThreads, paneMessages, paneMembers, paneChannels}
	for i, want := range forward {
		m, _ = m.Update(press("tab"))
		if m.focus != want {
			t.Fatalf("tab %d put focus on %d, want %d", i+1, m.focus, want)
		}
	}
	// Both ends wrap, since the roster is at one end and the channels at the other.
	back := []pane{paneMembers, paneMessages, paneThreads, paneChannels}
	for i, want := range back {
		m, _ = m.Update(press("shift+tab"))
		if m.focus != want {
			t.Fatalf("shift+tab %d put focus on %d, want %d", i+1, m.focus, want)
		}
	}
}

// TestAckAllAsksFirst is the one key that acts on every thread rather than on what is selected, so it is the one
// key that has to be asked about: dismissing is a judgement about relevance, and what is dismissed nobody
// mentions again.
func TestAckAllAsksFirst(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)

	m, _ = m.Update(typed("A"))
	if work := m.Pending(); work.action != actionNone {
		t.Errorf("A acted without asking: %+v", work)
	}
	// The threads are named in the question, because "mark everything read?" is not answerable.
	view := m.View()
	for _, want := range []string{"mark 2 threads read", "parser-panic", "docs-rewrite"} {
		if !strings.Contains(view, want) {
			t.Errorf("the question is missing %q:\n%s", want, boxed(view))
		}
	}

	m, _ = m.Update(typed("y"))
	// No thread, which is what the store reads as every thread.
	if diff := diffWork(m.Pending(), pendingWork{action: actionAck}); diff != "" {
		t.Errorf("y after A asked for the wrong thing, -want +got:\n%s", diff)
	}
	m = m.clearPending()
	m, _ = m.Update(ackedMsg{threads: []string{"parser-panic", "docs-rewrite"}})
	if !strings.Contains(m.View(), "marked 2 threads read") {
		t.Errorf("the outcome was not reported:\n%s", boxed(m.View()))
	}
}

// TestAckAllWithNothingWaitingSaysSo keeps a dead key from looking broken, and covers the muted case: a muted
// thread is not in the inbox, so clearing the inbox leaves it alone.
func TestAckAllWithNothingWaitingSaysSo(t *testing.T) {
	snapshot, messages := fullState()
	snapshot.threads = slices.Clone(snapshot.threads)
	for i := range snapshot.threads {
		snapshot.threads[i].Muted = true
	}
	m := testModel(96, 18, snapshot, messages)

	m, _ = m.Update(typed("A"))
	if work := m.Pending(); work.action != actionNone {
		t.Errorf("A acted on a channel of muted threads: %+v", work)
	}
	if !strings.Contains(m.View(), "nothing is waiting") {
		t.Errorf("A said nothing with every thread muted:\n%s", boxed(m.View()))
	}
}

// mouse is one event, named the way a hand would describe it.
func mouse(action tea.MouseAction, button tea.MouseButton, x, y int) tea.MouseMsg {
	return tea.MouseMsg{Action: action, Button: button, X: x, Y: y}
}

// TestDraggingADividerResizesThePaneBesideIt is the whole mouse feature: a pointer already knows how to move a
// boundary, where a resize mode is a key to remember for it.
func TestDraggingADividerResizesThePaneBesideIt(t *testing.T) {
	snapshot, messages := fullState()
	// Wider than the frame the other tests use, so the drag is what decides the width rather than the clamp:
	// at 96 columns all four panes plus the message column's minimum leave three spare.
	m := testModel(140, 18, snapshot, messages)
	// The first divider sits just past the channels column, which is where the frame draws it.
	at := channelsWidth

	m, _ = m.Update(mouse(tea.MouseActionPress, tea.MouseButtonLeft, at, 4))
	m, _ = m.Update(mouse(tea.MouseActionMotion, tea.MouseButtonLeft, at+6, 4))
	if got := m.paneWidth(paneChannels); got != channelsWidth+6 {
		t.Errorf("channels width = %d, want %d after dragging six columns right", got, channelsWidth+6)
	}
	// Absolute rather than incremental: the width follows the pointer, so a motion back to where it started
	// puts it back where it was.
	m, _ = m.Update(mouse(tea.MouseActionMotion, tea.MouseButtonLeft, at, 4))
	if got := m.paneWidth(paneChannels); got != channelsWidth {
		t.Errorf("channels width = %d, want it back at %d", got, channelsWidth)
	}
	// And the release ends it, so moving the pointer afterwards moves nothing.
	m, _ = m.Update(mouse(tea.MouseActionRelease, tea.MouseButtonLeft, at, 4))
	m, _ = m.Update(mouse(tea.MouseActionMotion, tea.MouseButtonLeft, at+10, 4))
	if got := m.paneWidth(paneChannels); got != channelsWidth {
		t.Errorf("channels width = %d after release, want %d", got, channelsWidth)
	}
}

// TestDraggingTheRostersEdgeMovesTheRoster is the divider the messages column is on the left of. The message
// column has no width of its own, so that divider moves the pane on its right, and the pointer moves it the
// other way: dragging left widens the roster.
func TestDraggingTheRostersEdgeMovesTheRoster(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(140, 18, snapshot, messages)
	at := channelsWidth + 1 + threadsWidth + 1 + m.messagesWidth()

	m, _ = m.Update(mouse(tea.MouseActionPress, tea.MouseButtonLeft, at, 4))
	m, _ = m.Update(mouse(tea.MouseActionMotion, tea.MouseButtonLeft, at-5, 4))
	if got := m.paneWidth(paneMembers); got != membersWidth+5 {
		t.Errorf("members width = %d, want %d after dragging its edge five columns left", got, membersWidth+5)
	}
}

// dividers is where the frame draws its column boundaries, read off the pane titles, which is the row a pointer
// aims at. Asserted on the frame rather than on the widths, because a width the layout then ignores is exactly
// the failure to catch: too large and the columns collapse to one pane, too small and it reads as unset.
func dividers(frame string) []int {
	rows := strings.Split(frame, "\n")
	var at []int
	for i, r := range rows[2] {
		if r == '|' {
			at = append(at, i)
		}
	}
	return at
}

// TestADragCannotSqueezeOutTheMessageColumn is the layout rule the drag has to obey: the message column is what
// the sidebars are dropped to protect, so a pointer cannot take its room either, and dragging one boundary must
// not throw a pane out of the layout it is being dragged in.
func TestADragCannotSqueezeOutTheMessageColumn(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)
	at := channelsWidth

	m, _ = m.Update(mouse(tea.MouseActionPress, tea.MouseButtonLeft, at, 4))
	m, _ = m.Update(mouse(tea.MouseActionMotion, tea.MouseButtonLeft, at+400, 4))
	if got := m.messagesWidth(); got < minMessagesWidth {
		t.Errorf("message column = %d wide, want it never under %d", got, minMessagesWidth)
	}
	if got := len(dividers(m.View())); got != 3 {
		t.Errorf("%d dividers after dragging right, want the same 3 panes:\n%s", got, boxed(m.View()))
	}

	// And the other way: a sidebar stays wide enough to say something, and stays that wide on screen rather
	// than snapping back to the width it started at.
	m, _ = m.Update(mouse(tea.MouseActionMotion, tea.MouseButtonLeft, 0, 4))
	if got := dividers(m.View()); len(got) != 3 || got[0] != minSidebarWidth {
		t.Errorf("dividers = %v after dragging to the edge, want the first at %d:\n%s",
			got, minSidebarWidth, boxed(m.View()))
	}
	// The frame still lines up: every line is the width it is supposed to be.
	for i, line := range strings.Split(m.View(), "\n") {
		if lipgloss.Width(line) > 96 {
			t.Errorf("line %d is %d wide, want at most 96:\n%s", i+1, lipgloss.Width(line), boxed(m.View()))
			break
		}
	}
}

// TestAPressAwayFromADividerStartsNothing keeps the pointer from resizing when it was doing something else, and
// covers the wheel, which scrolls the messages column and stops following at the same time.
func TestAPressAwayFromADividerStartsNothing(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)

	m, _ = m.Update(mouse(tea.MouseActionPress, tea.MouseButtonLeft, 3, 4))
	m, _ = m.Update(mouse(tea.MouseActionMotion, tea.MouseButtonLeft, 40, 4))
	if got := m.paneWidth(paneChannels); got != channelsWidth {
		t.Errorf("channels width = %d after a press inside the pane, want %d", got, channelsWidth)
	}

	m, _ = m.Update(mouse(tea.MouseActionPress, tea.MouseButtonWheelUp, 40, 4))
	if m.follow {
		t.Error("the wheel scrolled back and the view still follows the newest")
	}
}

// TestASavedWidthIsClampedToTheWindow is the case a file makes possible and a drag cannot: a width saved on a
// wide monitor, opened in a narrow terminal. Unclamped it collapses the columns to a single pane, which reads as
// the view being broken rather than as a width being too large for the window in front of it.
func TestASavedWidthIsClampedToTheWindow(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages).withLayout(config.Layout{Channels: 400, Threads: 400})

	if got := m.messagesWidth(); got < minMessagesWidth {
		t.Errorf("message column = %d wide, want it never under %d", got, minMessagesWidth)
	}
	if got := len(dividers(m.View())); got != 3 {
		t.Errorf("%d dividers with an oversized saved layout, want the usual 3:\n%s", got, boxed(m.View()))
	}
}

// TestALongThreadNameCannotEatItsBadge is the bug as reported, from a screenshot: with the badge after the name,
// a name as wide as the pane pushed it past the edge and truncation took it, so a column of threads said nothing
// about which of them were unread. That is most of what the column is for.
func TestALongThreadNameCannotEatItsBadge(t *testing.T) {
	snapshot, messages := fullState()
	snapshot.threads = slices.Clone(snapshot.threads)
	snapshot.threads[0].Name = "guard asks too often about one claim"
	m := testModel(96, 18, snapshot, messages)

	if !strings.Contains(m.View(), "2* guard asks") {
		t.Errorf("the badge went missing beside a long name:\n%s", boxed(m.View()))
	}
	// The name is what gives way, and it has to be visibly cut rather than silently shortened.
	if !strings.Contains(m.View(), "...") {
		t.Errorf("the name was not marked as truncated:\n%s", boxed(m.View()))
	}
}

// TestMuteTogglesOnTheSelectedThread is one key both ways, because a muted thread stays in this list and a
// separate unmute key would be one nobody finds. Which way it goes is read from the row, so it cannot flip a
// thread the view was showing as something else.
func TestMuteTogglesOnTheSelectedThread(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)

	m, _ = m.Update(typed("m"))
	if diff := diffWork(m.Pending(), pendingWork{action: actionMute, thread: "parser-panic"}); diff != "" {
		t.Errorf("m asked for the wrong thing, -want +got:\n%s", diff)
	}
	m = m.clearPending()
	m, _ = m.Update(mutedMsg{thread: "parser-panic", muted: true})
	if !strings.Contains(m.View(), "muted parser-panic") {
		t.Errorf("the mute was not reported:\n%s", boxed(m.View()))
	}

	// With the row now saying muted, the same key asks for the other direction, and the list says which
	// threads are quiet rather than showing a count that reads as something waiting.
	muted := snapshot
	muted.threads = slices.Clone(snapshot.threads)
	muted.threads[0].Muted = true
	m, _ = m.Update(muted)
	// A dash rather than a star, in the same column: what has piled up in a muted thread is not waiting on
	// anybody, and the column is what makes the difference readable down the pane.
	if !strings.Contains(m.View(), "2- parser-panic") {
		t.Errorf("a muted thread is not marked in the list:\n%s", boxed(m.View()))
	}
	m, _ = m.Update(typed("m"))
	want := pendingWork{action: actionMute, thread: "parser-panic", unmute: true}
	if diff := diffWork(m.Pending(), want); diff != "" {
		t.Errorf("m on a muted thread asked for the wrong thing, -want +got:\n%s", diff)
	}
}

func TestAckMarksTheSelectedThreadRead(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)

	m, _ = m.Update(typed("a"))
	if diff := diffWork(m.Pending(), pendingWork{action: actionAck, thread: "parser-panic"}); diff != "" {
		t.Errorf("a asked for the wrong thing, -want +got:\n%s", diff)
	}

	// The outcome is reported, because the badge going away is easy to miss and this is the only key that
	// changes what the agent whose cursor it is will see next.
	m = m.clearPending()
	m, _ = m.Update(ackedMsg{thread: "parser-panic"})
	if !strings.Contains(m.View(), "marked parser-panic read") {
		t.Errorf("the ack was not reported:\n%s", boxed(m.View()))
	}
}

// TestClaimingAsksForANoteAndPaths follows the CLI: the note is the part that stops duplicate work and the
// paths are the only part anything can enforce, so both are asked for and neither is required.
func TestClaimingAsksForANoteAndPaths(t *testing.T) {
	snapshot, messages := fullState()
	base := testModel(96, 18, snapshot, messages)

	m, _ := base.Update(typed("c"))
	if !strings.Contains(m.View(), "claim parser-panic, note | ") {
		t.Fatalf("c did not ask for a note:\n%s", boxed(m.View()))
	}
	for _, key := range typing("fixing all three call sites") {
		m, _ = m.Update(key)
	}
	m, _ = m.Update(press("enter"))
	if !strings.Contains(m.View(), "claim parser-panic, paths | ") {
		t.Fatalf("the note was not followed by the paths:\n%s", boxed(m.View()))
	}
	for _, key := range typing("parser.go, internal/lex/**") {
		m, _ = m.Update(key)
	}
	m, _ = m.Update(press("enter"))

	want := pendingWork{
		action: actionClaim,
		thread: "parser-panic",
		body:   "fixing all three call sites",
		// Split on commas, the way --paths does, so what works in the CLI works here.
		paths: []string{"parser.go", "internal/lex/**"},
	}
	if diff := diffWork(m.Pending(), want); diff != "" {
		t.Errorf("the claim is wrong, -want +got:\n%s", diff)
	}

	// A claim with no prose and no paths is still a claim, so an empty answer moves on rather than cancelling.
	bare, _ := base.Update(typed("c"))
	bare, _ = bare.Update(press("enter"))
	bare, _ = bare.Update(press("enter"))
	if diff := diffWork(bare.Pending(), pendingWork{action: actionClaim, thread: "parser-panic"}); diff != "" {
		t.Errorf("a bare claim is wrong, -want +got:\n%s", diff)
	}
}

// TestALostClaimNamesTheHolder is why a losing claim is a result and not an error: that output is what stops
// the duplicate work.
func TestALostClaimNamesTheHolder(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)

	m, _ = m.Update(claimedMsg{result: store.ClaimResult{Claim: store.Claim{
		Thread: "parser-panic", Holder: "alice", Note: "root cause is in the token loop",
	}}})

	view := m.View()
	for _, want := range []string{"parser-panic is held by alice", "root cause is in the token loop"} {
		if !strings.Contains(view, want) {
			t.Errorf("the losing claim does not say %q:\n%s", want, boxed(view))
		}
	}
}

func TestReleasingAClaim(t *testing.T) {
	snapshot, messages := fullState()
	mine := snapshot
	mine.claims = []store.Claim{{
		Channel: "/Users/c/projects/agora", Thread: "parser-panic", Holder: "parser",
		CreatedAt: now.Add(-time.Minute),
	}}

	// Your own goes without a question. There is nobody to surprise, and the CLI does not ask either.
	m := testModel(96, 18, mine, messages)
	m, _ = m.Update(typed("r"))
	if diff := diffWork(m.Pending(), pendingWork{action: actionRelease, thread: "parser-panic"}); diff != "" {
		t.Errorf("r on your own claim, -want +got:\n%s", diff)
	}

	// Somebody else's asks, and names them: a claim held by a member that looks idle may belong to an agent
	// waiting on its user.
	theirs := testModel(96, 18, snapshot, messages)
	theirs, _ = theirs.Update(typed("r"))
	if work := theirs.Pending(); work.action != actionNone {
		t.Fatalf("r took somebody else's claim without asking: %+v", work)
	}
	view := theirs.View()
	for _, want := range []string{"release parser-panic, held by alice for 4m", "[y/n]"} {
		if !strings.Contains(view, want) {
			t.Errorf("the question does not say %q:\n%s", want, boxed(view))
		}
	}
	yes, _ := theirs.Update(typed("y"))
	want := pendingWork{action: actionRelease, thread: "parser-panic", force: true}
	if diff := diffWork(yes.Pending(), want); diff != "" {
		t.Errorf("y did not force the release, -want +got:\n%s", diff)
	}

	// A thread nobody holds says so rather than doing nothing.
	nobody := snapshot
	nobody.claims = nil
	quiet := testModel(96, 18, nobody, messages)
	quiet, _ = quiet.Update(typed("r"))
	if work := quiet.Pending(); work.action != actionNone {
		t.Errorf("r on an unclaimed thread asked for %+v", work)
	}
	if !strings.Contains(quiet.View(), "nobody holds parser-panic") {
		t.Errorf("r on an unclaimed thread said nothing:\n%s", boxed(quiet.View()))
	}
}

// TestTheKeyListIsOnAKey: every acting key has to be discoverable, and the footer cannot carry them all without
// pushing the status off the line.
func TestTheKeyListIsOnAKey(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)

	m, _ = m.Update(typed("?"))
	view := m.View()
	for _, want := range []string{"post", "new thread", "read", "claim", "release", "delete"} {
		if !strings.Contains(view, want) {
			t.Errorf("the key list does not mention %q:\n%s", want, boxed(view))
		}
	}
	// And it goes away again, rather than being a screen you have to quit out of. Esc closes it too, since that
	// is the key reached for to close something, and quitting the view is a surprising way to be answered.
	for _, key := range []any{typed("?"), press("esc")} {
		closed, quit := m.Update(key)
		if quit {
			t.Errorf("%v quit the view from the key list", key)
		}
		if strings.Contains(closed.View(), "[KEYS]") {
			t.Errorf("%v did not close the key list:\n%s", key, boxed(closed.View()))
		}
	}
}

// TestAPastedNewlineDoesNotReflowTheFrame. A prompt is one line of a frame whose height everything else is
// laid out against, so a newline arriving in a paste has to become a space rather than a second line.
func TestAPastedNewlineDoesNotReflowTheFrame(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)
	m, _ = m.Update(typed("p"))

	m, _ = m.Update(typed("two\nlines\tapart"))
	if got := m.compose.line.Value(); got != "two lines apart" {
		t.Errorf("the prompt holds %q, want the newline and tab as spaces", got)
	}
	if lines := len(strings.Split(m.View(), "\n")); lines != 18 {
		t.Errorf("the frame is %d lines, want 18:\n%s", lines, boxed(m.View()))
	}
}

// TestALongLineKeepsShowingWhatIsBeingTyped: a prompt that truncated from the left would stop showing the
// typing as soon as it reached the edge of the terminal, which is where a long message most needs proofreading.
func TestALongLineKeepsShowingWhatIsBeingTyped(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)
	m, _ = m.Update(typed("p"))
	for _, key := range typing("the panic is a symptom, the root cause is in the token loop and the lexer has it too") {
		m, _ = m.Update(key)
	}

	view := m.View()
	if !strings.Contains(view, "the lexer has it too") {
		t.Errorf("the end of what was typed is not on screen:\n%s", boxed(view))
	}
	if strings.Contains(view, "the panic is a symptom") {
		t.Errorf("the prompt is showing the head of the line rather than the tail:\n%s", boxed(view))
	}
}

// TestAPromptGoesWhereItWasOpened is the same rule a question follows, for the same reason. Threads are ordered
// by when they last moved, so anybody posting reorders the sidebar under a half typed message, and a reload can
// settle the selection somewhere else entirely.
func TestAPromptGoesWhereItWasOpened(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)
	m, _ = m.Update(typed("p"))
	for _, key := range typing("mine") {
		m, _ = m.Update(key)
	}

	// parser-panic is gone from the next reload, so the selection settles onto another thread.
	gone := snapshot
	gone.threads = snapshot.threads[1:]
	gone.claims = nil
	m, _ = m.Update(gone)
	if _, thread := m.Selection(); thread == "parser-panic" {
		t.Fatal("the selection did not move, so this proves nothing")
	}

	m, _ = m.Update(press("enter"))
	if diff := diffWork(m.Pending(), pendingWork{action: actionPost, thread: "parser-panic", body: "mine"}); diff != "" {
		t.Errorf("the post moved with the selection, -want +got:\n%s", diff)
	}
}

// TestTheRosterIsAColumnYouCanMoveIn is what moved the members off the bottom line: a name in a conversation
// raises the question of which tree that agent is working in, and a path does not fit on a line shared with four
// other names.
func TestTheRosterIsAColumnYouCanMoveIn(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)

	// It starts on you, because the first thing anybody wants from this column is somebody else's worktree and
	// a hunt for where the cursor is comes first otherwise.
	if m.member != "parser" {
		t.Errorf("the selected member is %q, want you", m.member)
	}
	// And the whole record of whoever is selected is on screen, which is what the column has no room for.
	view := m.View()
	for _, want := range []string{"parser", "2 threads / 3 messages unread", "seen 3s ago", "(you)", "pr-chancez-parser-panic"} {
		if !strings.Contains(view, want) {
			t.Errorf("the member detail line is missing %q:\n%s", want, boxed(view))
		}
	}

	// Out to the roster, past the messages.
	for range 3 {
		m, _ = m.Update(typed("l"))
	}
	if m.focus != paneMembers {
		t.Fatalf("focus is %v after three l, want the members", m.focus)
	}
	if !strings.Contains(m.View(), "[MEMBERS]") {
		t.Errorf("the members pane is not marked as focused:\n%s", boxed(m.View()))
	}

	m, _ = m.Update(typed("k"))
	if m.member != "alice" {
		t.Fatalf("k selected %q, want alice", m.member)
	}
	// What she says she is doing, which is the answer to "who is this name": every other member is eight hex
	// digits of a session id.
	if !strings.Contains(m.View(), "the lexer's bounds checks") {
		t.Errorf("the detail lines do not say what alice is doing:\n%s", boxed(m.View()))
	}
	// Her worktree, not yours, which is the point of moving here.
	if !strings.Contains(m.View(), ".worktrees/lexer") {
		t.Errorf("the detail line did not follow the selection:\n%s", boxed(m.View()))
	}
	// And nothing below a member moved: the conversation you were reading is still the one on screen.
	if _, thread := m.Selection(); thread != "parser-panic" {
		t.Errorf("moving in the roster changed the selected thread to %q", thread)
	}
}

// TestTheRosterIsDroppedBeforeTheMessagesAre is the layout rule: the message column is what the sidebars are
// sacrificed for, and the focused pane is shown even when it is one of the ones that got dropped, because a
// selection you cannot see is worse than a missing column.
func TestTheRosterIsDroppedBeforeTheMessagesAre(t *testing.T) {
	snapshot, messages := fullState()

	wide := testModel(96, 18, snapshot, messages)
	if !strings.Contains(wide.View(), "MEMBERS") {
		t.Errorf("no roster at 96 columns:\n%s", boxed(wide.View()))
	}

	narrow := testModel(80, 18, snapshot, messages)
	if strings.Contains(narrow.View(), "MEMBERS") {
		t.Errorf("the roster is still drawn at 80 columns:\n%s", boxed(narrow.View()))
	}
	// The three that are left still are, rather than the whole layout collapsing.
	for _, want := range []string{"CHANNELS", "THREADS", "token loop"} {
		if !strings.Contains(narrow.View(), want) {
			t.Errorf("%q went missing at 80 columns:\n%s", want, boxed(narrow.View()))
		}
	}

	for range 3 {
		narrow, _ = narrow.Update(typed("l"))
	}
	if !strings.Contains(narrow.View(), "[MEMBERS]") {
		t.Errorf("focusing the roster at 80 columns did not show it:\n%s", boxed(narrow.View()))
	}
	if strings.Contains(narrow.View(), "CHANNELS") {
		t.Errorf("the focused pane is sharing the screen at 80 columns:\n%s", boxed(narrow.View()))
	}
}

// TestRemovingAMemberAsksWithTheClaimsInIt is the same safety as the delete key, on the thing that is harder to
// undo in a different way: what a removed member was holding is what nobody can ask about afterwards.
func TestRemovingAMemberAsksWithTheClaimsInIt(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)
	for range 3 {
		m, _ = m.Update(typed("l"))
	}
	m, _ = m.Update(typed("k")) // alice

	m, _ = m.Update(typed("d"))
	if diff := diffWork(m.Pending(), pendingWork{action: actionPreviewLeave, member: "alice"}); diff != "" {
		t.Fatalf("d in the roster asked for the wrong thing, -want +got:\n%s", diff)
	}
	m = m.clearPending()
	// The cursors are reported by the dry run and deliberately not in the question: leaving keeps them, and a
	// question has to name what actually goes.
	m, _ = m.Update(leavePreviewMsg{
		channel: "/Users/c/projects/agora", member: "alice", cursors: 2, claims: []string{"parser-panic"},
	})
	view := m.View()
	for _, want := range []string{"remove alice from the roster", "releasing parser-panic", "[y/n]"} {
		if !strings.Contains(view, want) {
			t.Errorf("the question is missing %q:\n%s", want, boxed(view))
		}
	}

	yes, _ := m.Update(typed("y"))
	want := pendingWork{action: actionLeave, member: "alice", force: true}
	if diff := diffWork(yes.Pending(), want); diff != "" {
		t.Errorf("y asked for the wrong thing, -want +got:\n%s", diff)
	}

	// The outcome clears the selection, since the row it was on is gone.
	gone, _ := yes.Update(leftMsg{member: "alice"})
	if gone.member != "" {
		t.Errorf("the selection is still on %q after it was removed", gone.member)
	}
	if !strings.Contains(gone.View(), "removed alice") {
		t.Errorf("the removal was not reported:\n%s", boxed(gone.View()))
	}
}

// TestDeleteStillMeansTheThreadInTheThreadPanes: d acts on whatever the keyboard is on, so the pane it was
// pressed in decides, and the panes below the channels must not have changed meaning.
func TestDeleteStillMeansTheThreadInTheThreadPanes(t *testing.T) {
	snapshot, messages := fullState()
	base := testModel(96, 18, snapshot, messages)

	for _, focus := range []string{"l", "ll"} {
		m := base
		for _, key := range focus {
			m, _ = m.Update(typed(string(key)))
		}
		m, _ = m.Update(typed("d"))
		want := pendingWork{action: actionPreviewDelete, thread: "parser-panic"}
		if diff := diffWork(m.Pending(), want); diff != "" {
			t.Errorf("d after %q, -want +got:\n%s", focus, diff)
		}
	}
}

// TestDeletingAChannelAsksWithWhatIsInIt is the d key one level up. The CLI refuses a channel that has been
// used and takes --force; here the numbers and the holders are in the question, which is what somebody
// answering y is being told.
func TestDeletingAChannelAsksWithWhatIsInIt(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)

	m, _ = m.Update(typed("d"))
	want := pendingWork{action: actionPreviewDeleteChannel, channel: "/Users/c/projects/agora"}
	if diff := diffWork(m.Pending(), want); diff != "" {
		t.Fatalf("d on the channels asked for the wrong thing, -want +got:\n%s", diff)
	}
	// Nothing on screen has changed until the numbers come back.
	if strings.Contains(m.View(), "delete channel") {
		t.Error("the question was asked before the numbers came back")
	}

	m = m.clearPending()
	preview := previewChannelMsg{
		channel: "/Users/c/projects/agora", threads: 3, messages: 6, members: 2, claims: []string{"alice"},
	}
	m, _ = m.Update(preview)
	// At 96 columns the counts run off the end, so what has to survive is the name, who is working in there,
	// that it is the repository this session is in, and the keys that answer it.
	view := m.View()
	for _, want := range []string{
		"delete channel /Users/c/projects/agora", "which you are in", "claimed by alice", "3 threads", "[y/n]",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the question is missing %q:\n%s", want, boxed(view))
		}
	}
	// Given the room, all of it.
	wide, _ := testModel(140, 18, snapshot, messages).Update(preview)
	want96 := "delete channel /Users/c/projects/agora, which you are in, claimed by alice: 3 threads, 6 messages, 2 members?  [y/n]"
	if !strings.Contains(wide.View(), want96) {
		t.Errorf("the whole question is not on screen at 140 columns:\n%s", boxed(wide.View()))
	}

	yes, _ := m.Update(typed("y"))
	wantYes := pendingWork{action: actionDeleteChannel, channel: "/Users/c/projects/agora", force: true}
	if diff := diffWork(yes.Pending(), wantYes); diff != "" {
		t.Errorf("y asked for the wrong thing, -want +got:\n%s", diff)
	}
}

// TestDeletingAChannelMovesTheSelectionOffIt is not cosmetic. The loader polls the selected channel and every
// command that names one creates it, so a selection left on a deleted channel recreates it and shows it back,
// empty, which reads as the deletion having failed.
func TestDeletingAChannelMovesTheSelectionOffIt(t *testing.T) {
	snapshot, messages := fullState()
	base := testModel(96, 18, snapshot, messages)

	for _, tc := range []struct {
		name     string
		down     int
		deleted  string
		want     string
		wantLeft []string
	}{
		{
			name: "the first", deleted: "/Users/c/projects/agora", want: "/Users/c/projects/cm",
			wantLeft: []string{"/Users/c/projects/cm", "/Users/c/dotfiles"},
		},
		{
			name: "one in the middle", down: 1, deleted: "/Users/c/projects/cm", want: "/Users/c/dotfiles",
			wantLeft: []string{"/Users/c/projects/agora", "/Users/c/dotfiles"},
		},
		{
			// Nothing below it, so the selection goes back up rather than off the end.
			name: "the last", down: 2, deleted: "/Users/c/dotfiles", want: "/Users/c/projects/cm",
			wantLeft: []string{"/Users/c/projects/agora", "/Users/c/projects/cm"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := base
			for range tc.down {
				m, _ = m.Update(typed("j"))
			}
			m, _ = m.Update(deletedChannelMsg{channel: tc.deleted})

			if channel, thread := m.Selection(); channel != tc.want || thread != "" {
				t.Errorf("selection = %q, %q, want %q and no thread", channel, thread, tc.want)
			}
			var left []string
			for _, channel := range m.channels {
				left = append(left, channel.Key)
			}
			if diff := cmp.Diff(tc.wantLeft, left); diff != "" {
				t.Errorf("channels left, -want +got:\n%s", diff)
			}
			if !strings.Contains(m.View(), "deleted "+tc.deleted) {
				t.Errorf("the deletion was not reported:\n%s", boxed(m.View()))
			}
		})
	}
}

func TestDeletingTheOnlyChannelLeavesNothingSelected(t *testing.T) {
	m := testModel(96, 18, snapshotMsg{channels: []store.Channel{{Key: "/Users/c/projects/agora"}}})

	m, _ = m.Update(deletedChannelMsg{channel: "/Users/c/projects/agora"})
	if channel, _ := m.Selection(); channel != "" {
		t.Errorf("selection = %q, want nothing selected with no channels left", channel)
	}
	if len(m.channels) != 0 {
		t.Errorf("channels = %+v, want none", m.channels)
	}
}

// TestDeletingAChannelYouAreNotOnLeavesTheSelectionAlone: the list is every channel in the database, so one of
// them going is not a reason to move what somebody is reading.
func TestDeletingAChannelYouAreNotOnLeavesTheSelectionAlone(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)

	m, _ = m.Update(deletedChannelMsg{channel: "/Users/c/projects/cm"})
	if channel, thread := m.Selection(); channel != "/Users/c/projects/agora" || thread != "parser-panic" {
		t.Errorf("selection = %q, %q, want it where it was", channel, thread)
	}
	if len(m.channels) != 2 {
		t.Errorf("channels = %+v, want the other two", m.channels)
	}
}

func TestAConfirmationForAChannelYouHaveLeftIsDropped(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)
	m, _ = m.Update(typed("j"))

	// In flight while the selection moved. Asking about one channel and deleting another is the worst outcome
	// this key has.
	m, _ = m.Update(previewChannelMsg{channel: "/Users/c/projects/agora", threads: 3})
	if strings.Contains(m.View(), "[y/n]") {
		t.Errorf("a stale question was asked:\n%s", boxed(m.View()))
	}
}

func TestDeletingAChannelDoesNothingWithNoneSelected(t *testing.T) {
	m := testModel(96, 18, snapshotMsg{})

	m, _ = m.Update(typed("d"))
	if work := m.Pending(); work.action != actionNone {
		t.Errorf("d asked for %+v with no channel selected", work)
	}
}

func TestRemovingAMemberWithNoRosterDoesNothing(t *testing.T) {
	m := testModel(96, 18, snapshotMsg{channels: []store.Channel{{Key: "/Users/c/projects/agora"}}})
	for range 3 {
		m, _ = m.Update(typed("l"))
	}
	m, _ = m.Update(typed("d"))

	if work := m.Pending(); work.action != actionNone {
		t.Errorf("d in an empty roster asked for %+v", work)
	}
	if !strings.Contains(m.View(), "nobody here yet") {
		t.Errorf("an empty roster does not say so:\n%s", boxed(m.View()))
	}
}

func TestTail(t *testing.T) {
	const path = "/Users/c/projects/agora/.worktrees/parser"
	for _, tc := range []struct {
		width int
		want  string
	}{
		// The end is what tells two checkouts of one repository apart; the start is the same for all of them.
		{width: 40, want: "...rs/c/projects/agora/.worktrees/parser"},
		{width: 41, want: path},
		{width: 80, want: path},
		{width: 3, want: "ser"},
		{width: 0, want: ""},
	} {
		if got := tail(path, tc.width); got != tc.want {
			t.Errorf("tail(%d) = %q, want %q", tc.width, got, tc.want)
		}
	}
}

// TestScrollingStopsAtBothEnds is the bug the clamp exists for: pressing j at the bottom fifty times used to
// mean pressing k fifty times before anything moved, because the offset kept counting past the end.
func TestScrollingStopsAtBothEnds(t *testing.T) {
	snapshot, messages := fullState()
	long := messages
	for i := range 30 {
		long.messages = append(long.messages,
			message(int64(100+i), int64(3+i), messages.channel, messages.thread, "alice", fmt.Sprintf("finding %d", i), 1))
	}
	m := testModel(96, 18, snapshot, long)
	for range 2 {
		m, _ = m.Update(typed("l")) // out to the messages
	}

	// After the first j, which only stops it following: the status line says "paused" from then on, and this
	// is about whether the content moves.
	m, _ = m.Update(typed("j"))
	bottom := m.View()
	for range 50 {
		m, _ = m.Update(typed("j"))
	}
	if m.View() != bottom {
		t.Errorf("scrolling down from the bottom moved:\n%s", boxed(m.View()))
	}
	// One k has to move, rather than fifty of them undoing fifty that did nothing.
	m, _ = m.Update(typed("k"))
	if m.View() == bottom {
		t.Errorf("k after scrolling down at the bottom did nothing:\n%s", boxed(m.View()))
	}

	m, _ = m.Update(typed("g"))
	top := m.View()
	for range 5 {
		m, _ = m.Update(typed("k"))
	}
	if m.View() != top {
		t.Errorf("scrolling up from the top moved:\n%s", boxed(m.View()))
	}
	if !strings.Contains(top, "empty input reaches the") {
		t.Errorf("the top of the thread is not the oldest message:\n%s", boxed(top))
	}
}

// TestARunOfRunesIsARunOfKeys. bubbletea reports every rune that arrived in one read as one event, so holding a
// key down long enough for its repeats to land together arrives as "jjj". Dispatched as one key it matches no
// binding and the view freezes while the key is held, which is how a pty driving "ll" moved nothing.
func TestARunOfRunesIsARunOfKeys(t *testing.T) {
	snapshot, messages := fullState()
	base := testModel(96, 18, snapshot, messages)

	together, _ := base.Update(typed("ll"))
	apart := base
	for range 2 {
		apart, _ = apart.Update(typed("l"))
	}
	if together.focus != apart.focus {
		t.Errorf("ll landed on the %v pane, want the %v pane two l presses reach", together.focus, apart.focus)
	}

	// A key in the middle of a run that opens a prompt keeps the rest, since from there it is text.
	prompt, _ := base.Update(typed("lpab"))
	if prompt.compose == nil {
		t.Fatalf("p inside a run did not open a prompt")
	}
	if got := prompt.compose.line.Value(); got != "" {
		t.Errorf("the prompt already holds %q, want the rest of the run to arrive as its own event", got)
	}
	// And quitting inside a run quits.
	if _, quit := base.Update(typed("xq")); !quit {
		t.Error("q inside a run did not quit")
	}
}

// TestTypingAWordThatIsAlsoAKeyName is a bug from a real terminal. bubbletea reports the runes that arrived in
// one read as one event, and a binding is matched against that event's whole text, so typing "end" quickly
// arrives as one event whose String() is "end", which textinput has bound to "go to the line end". Typing "the
// whole thing works end to end" posted "the whole thing works  to".
func TestTypingAWordThatIsAlsoAKeyName(t *testing.T) {
	snapshot, messages := fullState()
	m := testModel(96, 18, snapshot, messages)
	m, _ = m.Update(typed("p"))

	const body = "the whole thing works end to end"
	m, _ = m.Update(typed(body))
	if got := m.compose.line.Value(); got != body {
		t.Errorf("the prompt holds %q, want %q", got, body)
	}

	// The other names a word could collide with, each arriving whole the way a fast typist delivers them.
	for _, word := range []string{"end", "up", "down", "left", "right", "home", "tab", "esc", "enter", "delete"} {
		t.Run(word, func(t *testing.T) {
			fresh, _ := testModel(96, 18, snapshot, messages).Update(typed("p"))
			fresh, _ = fresh.Update(typed(word))
			if got := fresh.compose.line.Value(); got != word {
				t.Errorf("typing %q left %q in the prompt", word, got)
			}
		})
	}
}
