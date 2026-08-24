// Package tui is the view of a database's channels, and the participant's half of the CLI.
//
// Four columns: channels, the selected channel's threads, that thread's messages, the roster. Keys cover what
// a participant does: post, start a thread, mark read, claim, release, delete.
//
// Being open changes nothing. Reading here never advances a cursor, and the roster is asked in observe mode so
// watching does not join. Hook entry points and one-shot reports stay CLI-only.
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/chancez/agora/internal/config"
	"github.com/chancez/agora/internal/store"
	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	// Sidebar widths. Fixed, because a sidebar that resizes with its content makes the message column jump
	// every time a thread is claimed.
	channelsWidth = 16
	threadsWidth  = 24
	membersWidth  = 20
	// What the messages are worth keeping readable at. The sidebars are what get dropped to protect it, in
	// order of how much a reader needs them, because four columns in eighty characters is four unreadable
	// columns.
	minMessagesWidth = 30
	// minSidebarWidth is narrow enough to be a deliberate choice and wide enough to still say something: a
	// badge and a few characters of a name.
	minSidebarWidth = 8
	// wheelLines is how far one notch scrolls. Three is what a terminal sends per notch on most systems, so
	// this matches the feel of scrolling anything else.
	wheelLines = 3
	// promptGutter separates what a prompt is for from what is being typed into it. The same bar the columns
	// are separated by, since it is the same job.
	promptGutter = " | "
	// A size to fall back on, for a terminal that does not report one. Measured by driving a pty with a
	// window size of 0x0: without this the view drew nothing and stayed blank, with nothing on screen to
	// explain why.
	defaultWidth  = 80
	defaultHeight = 24
)

// newViewport scrolls the message pane. Its own keys are off, since this view dispatches every key itself and
// would otherwise scroll twice per press. Mouse reporting stays off too: it costs the terminal's own text
// selection, and copying a message out is worth more than a wheel.
func newViewport() viewport.Model {
	v := viewport.New(0, 0)
	v.KeyMap = viewport.KeyMap{}
	return v
}

var (
	dim      = lipgloss.NewStyle().Faint(true)
	strong   = lipgloss.NewStyle().Bold(true)
	selected = lipgloss.NewStyle().Reverse(true)
)

// pane is which column has the keyboard.
type pane int

const (
	paneChannels pane = iota
	paneThreads
	paneMessages
	paneMembers
)

// panes is how many there are, for the key that cycles them.
const panes = 4

func (p pane) String() string {
	switch p {
	case paneChannels:
		return "channels"
	case paneThreads:
		return "threads"
	case paneMembers:
		return "members"
	default:
		return "messages"
	}
}

// width is the column a pane gets, zero for the messages, which take whatever is left.
// withLayout puts saved widths on, clamped to the window they are being shown in: one saved on a wide monitor
// would otherwise collapse a narrow terminal to a single pane, which reads as the view being broken rather than
// as a width being too large.
func (m Model) withLayout(layout config.Layout) Model {
	m.sidebars[paneChannels] = layout.Channels
	m.sidebars[paneThreads] = layout.Threads
	m.sidebars[paneMembers] = layout.Members
	return m.fitSidebars()
}

// Layout is what the widths are now, for whoever saves them.
func (m Model) Layout() config.Layout {
	return config.Layout{
		Channels: m.sidebars[paneChannels],
		Threads:  m.sidebars[paneThreads],
		Members:  m.sidebars[paneMembers],
	}
}

// fitSidebars clamps every width that has one, in pane order. Run when the window changes size and when a saved
// layout arrives, so a stored width is held to the same rule a dragged one is.
//
// Each is clamped against the layout the defaults produce rather than against the one the stored widths produce.
// That is not a detail: two oversized widths collapse the columns to a single pane, and a clamp computed from
// that collapsed layout leaves both of them still too large, so the view opens as one pane and stays there.
func (m Model) fitSidebars() Model {
	stored := m.sidebars
	m.sidebars = [panes]int{}
	for _, p := range []pane{paneChannels, paneThreads, paneMembers} {
		if stored[p] > 0 {
			m.sidebars[p] = m.clampSidebar(p, stored[p])
		}
	}
	return m
}

// paneWidth is a sidebar's width: what a drag set it to, or the default it starts at. The message column is
// never in here, because it is whatever is left over: that is what makes it the column the others are dropped
// to protect.
func (m Model) paneWidth(p pane) int {
	if w := m.sidebars[p]; w > 0 {
		return w
	}
	return p.width()
}

func (p pane) width() int {
	switch p {
	case paneChannels:
		return channelsWidth
	case paneThreads:
		return threadsWidth
	case paneMembers:
		return membersWidth
	default:
		return 0
	}
}

// snapshotMsg is everything about the selected channel: its threads, its claims, and its roster. Cursors
// and claims change without anybody posting, so a watch alone would show a live log beside a stale sidebar.
type snapshotMsg struct {
	channels []store.Channel
	threads  []store.Thread
	claims   []store.Claim
	members  []store.Member
}

// messagesMsg is the selected thread's history.
type messagesMsg struct {
	channel  string
	thread   string
	messages []store.Message
}

// messageMsg is one new message from the watch, which is the low latency path.
type messageMsg struct {
	message store.Message
}

// previewMsg is what a delete would take, which is what its confirmation is built on: the question has to be
// asked with the numbers in it, and asking must carry no risk of being the deletion.
type previewMsg struct {
	channel  string
	thread   string
	messages int64
	cursors  int64
	claim    string
}

// What the acting keys came back with. Each carries what the store answered rather than a sentence, because
// the sentence belongs in the model with the rest of the layout, where a test can assert it without a screen.

// postedMsg is one message written.
type postedMsg struct {
	message store.Message
}

// ackedMsg is what an acknowledgement covered: one named thread, or every thread it turned out to reach.
type ackedMsg struct {
	thread  string
	threads []string
}

// mutedMsg is one thread muted or unmuted.
type mutedMsg struct {
	thread string
	muted  bool
}

// claimedMsg is the outcome of a claim, which is a holder either way.
type claimedMsg struct {
	result store.ClaimResult
}

// releasedMsg is the outcome of a release.
type releasedMsg struct {
	thread string
	result store.ReleaseResult
}

// deletedMsg is one thread gone.
type deletedMsg struct {
	thread string
}

// previewChannelMsg is what deleting a whole channel would take. Its own message rather than previewMsg with a
// flag, because the two questions name different things: a channel's is threads and members, where a thread's
// is messages and cursors.
type previewChannelMsg struct {
	channel  string
	threads  int64
	messages int64
	members  int64
	// claims is who holds work in there, named because a claim is somebody working right now and their note is
	// what says whether deleting this is a decision or a mistake.
	claims []string
}

// deletedChannelMsg is one channel gone, with everything that was in it.
type deletedChannelMsg struct {
	channel string
}

// leavePreviewMsg is what removing a member would take, which its question is built on.
type leavePreviewMsg struct {
	channel string
	member  string
	cursors int64
	claims  []string
}

// leftMsg is one member off the roster.
type leftMsg struct {
	member string
}

type errMsg struct {
	err error
}

// action is work the model wants the store to do. The model stays pure: it records what it wants, the program
// reads it, issues the command, and clears it.
type action int

const (
	actionNone action = iota
	actionPreviewDelete
	actionDelete
	actionPost
	actionAck
	actionMute
	actionClaim
	actionRelease
	actionPreviewLeave
	actionLeave
	actionPreviewDeleteChannel
	actionDeleteChannel
)

// pendingWork is one request the model wants issued, with everything the store needs for it. It is a value
// rather than an action plus a look at the current selection, because the selection moves: a reload between
// the keystroke and the call would otherwise aim the work at whatever ended up selected.
type pendingWork struct {
	action action
	thread string
	// channel is which channel the work is about, for the one action whose target is not the selected channel:
	// answering a question can take long enough for the selection to have moved to another.
	channel string
	// member is who the work is about, for the one action that is about a member rather than a thread.
	member string
	// body is a post's message, or a claim's note.
	body string
	// paths is a claim's globs, the only part of a claim that anything can enforce.
	paths []string
	// force acts on a claim held by somebody else. Only ever set by answering a question that named them.
	force bool
	// unmute is which way the one toggle goes. Read from the selected thread at the keystroke rather than in
	// the store, so the key cannot flip a thread the view was not showing as muted.
	unmute bool
}

// dragState is a divider being moved with the mouse.
type dragState struct {
	// resizing is the sidebar whose width changes, which is not always the pane left of the divider: the one
	// between the messages column and the roster moves the roster, since the messages column has no width of
	// its own to change.
	resizing pane
	// sign is which way the pointer moves that width. Dragging the right edge of a sidebar widens it; dragging
	// the left edge of the roster, on the other side of the messages column, narrows it.
	sign       int
	startX     int
	startWidth int
}

// question is a pending y/n and the work to do on y. Carrying the work rather than re-deriving it is what
// keeps an answer aimed at what was asked about: the selection can move while the question is on screen.
type question struct {
	channel string
	thread  string
	member  string
	text    string
	work    pendingWork
}

// composeStep is which line of an answer is being typed. A post to an existing thread is one line; a new
// thread is a name and then a message; a claim is a note and then its paths.
type composeStep int

const (
	stepBody composeStep = iota
	stepThread
	stepNote
	stepPaths
)

// compose is something being typed. thread is where it will go, fixed when the prompt opened rather than read
// at submit time, for the same reason a question carries its work.
type compose struct {
	thread string
	step   composeStep
	// note is a claim's note, held while its paths are typed.
	note string
	line textinput.Model
}

// newCompose starts a prompt at one step, aimed at one thread.
//
// A static cursor, because a blink is a timer and therefore a command, and Update here returns none so the
// frame stays assertable without a terminal.
func (m Model) newCompose(thread string, step composeStep) *compose {
	line := textinput.New()
	line.Prompt = ""
	line.Cursor.SetMode(cursor.CursorStatic)
	line.Focus()
	return &compose{thread: thread, step: step, line: line}
}

// sizeInput gives the input the room the prompt leaves it, before every edit.
//
// Before, not in View: textinput works its scroll out while the value changes, so a Width set at render time
// was zero for every edit and the prompt showed the head of a long line. Per edit because the step decides the
// label, and the label decides the room.
func (m Model) sizeInput(c compose) compose {
	label, hint := promptParts(c)
	c.line.Width = m.width - lipgloss.Width(label+promptGutter) - lipgloss.Width(hint) - 3
	if c.line.Width < 8 {
		c.line.Width = 8
	}
	return c
}

// Model is the whole view. Update and View are pure, so the layout can be asserted without a terminal.
type Model struct {
	cfg config.Config

	channels []store.Channel
	threads  []store.Thread
	messages []store.Message
	claims   []store.Claim
	members  []store.Member

	// channel, thread, and member are the selection. They are names rather than indices so a reload that
	// reorders the sidebars keeps the selection on the same thing rather than on the same row.
	channel string
	thread  string
	member  string
	focus   pane

	width, height int
	// messages scroll through a viewport, which owns where the window sits and clamps it. follow is still
	// this model's: it means "keep the window on the newest", which is what you want while watching and not
	// what you want while reading back, and no component knows which of those somebody is doing.
	viewport viewport.Model
	follow   bool
	unseen   int

	// confirm holds a pending question, and while it is set the movement keys are ignored: navigating away
	// mid-question and then pressing y would act on whatever the cursor had moved to.
	confirm *question
	// sidebars is the width a drag has given each pane, zero meaning the default. Per session on purpose:
	// where you dragged a divider is a fact about this window, not about the channel.
	sidebars [panes]int
	// drag is the divider being moved, nil when nothing is. It holds the width the pane started at rather than
	// applying each motion to the last one, so a drag that leaves the clamp and comes back lands where the
	// pointer is instead of where the clamping stopped it.
	drag *dragState
	// compose holds a line being typed, and owns the keyboard the same way for the same reason.
	compose *compose
	pending pendingWork
	// showKeys is the full key list, which is on a key rather than in the footer: a dozen keys listed along
	// the bottom would push the status off the line, and the status is the only place a failed reload is
	// reported.
	showKeys bool
	// notice is the last outcome, in the footer until the next keystroke. A claim that was lost has to say who
	// holds it, which is the whole reason the CLI prints on a losing claim rather than only exiting non-zero.
	notice string

	// keys and help are one keymap and its renderer, so what a key does and what the screen says it does
	// cannot drift apart.
	keys keyMap
	help help.Model

	err    error
	loaded bool
	now    func() time.Time
}

// New builds a model. now is injected so a test can assert on "4m ago" rather than on whatever the clock
// said when it ran.
func New(cfg config.Config, now func() time.Time) Model {
	if now == nil {
		now = time.Now
	}
	return Model{
		cfg:     cfg,
		channel: cfg.Channel.Value,
		follow:  true,
		now:     now,
		keys:    defaultKeys(),
		help:    newHelp(),
		// Its own keys are off: this view dispatches every key itself, so a viewport also binding j and k
		// would scroll twice per press.
		viewport: newViewport(),
		width:    defaultWidth,
		height:   defaultHeight,
		// The leftmost pane, which always has a row to move within. Starting on the messages, the pane most
		// likely to be empty, made every movement key look dead: "I cannot go up or down at all".
		focus: paneChannels,
	}
}

// Selection is which channel and thread the view is showing, which is what tells the loader what to fetch.
func (m Model) Selection() (channel, thread string) { return m.channel, m.thread }

// Pending is work the model wants done, and clearPending is how the program says it has issued it.
func (m Model) Pending() pendingWork { return m.pending }

func (m Model) clearPending() Model {
	m.pending = pendingWork{}
	return m
}

// Update is the bubbletea update, minus the commands, which Run supplies.
func (m Model) Update(msg any) (Model, bool) {
	quit := false
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// A terminal that cannot say how big it is gets the fallback rather than a blank screen.
		m.width, m.height = defaultWidth, defaultHeight
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		m = m.fitSidebars()
	case tea.MouseMsg:
		m = m.handleMouse(msg)
	case tea.KeyMsg:
		m, quit = m.handleKey(msg)
	case snapshotMsg:
		m.loaded = true
		m.err = nil
		m.channels = msg.channels
		m.threads = msg.threads
		m.claims = msg.claims
		m.members = msg.members
		m = m.settleSelection()
	case messagesMsg:
		// Ignored when the selection moved on while this was in flight, or the messages of the thread you
		// just left would land in the pane of the one you are looking at.
		if msg.channel == m.channel && msg.thread == m.thread {
			m.messages = msg.messages
		}
	case messageMsg:
		m = m.appendMessage(msg.message)
	case postedMsg:
		// Selecting where it went, because posting into a new thread and being left looking at the old one
		// reads as the post having gone nowhere.
		if msg.message.Channel == m.channel && msg.message.Thread != m.thread {
			m.thread, m.messages = msg.message.Thread, nil
			m.follow, m.unseen = true, 0
			m.viewport.GotoTop()
		}
		m = m.appendMessage(msg.message)
	case ackedMsg:
		// Named or not: A dismisses several at once, and "marked  read" is what the empty thread used to print.
		if msg.thread != "" {
			m.notice = "marked " + msg.thread + " read"
			break
		}
		m.notice = "marked " + plural(len(msg.threads), "thread") + " read"
	case mutedMsg:
		// Worth reporting for the same reason an ack is: the list looks almost the same afterwards, and this is
		// the one key whose effect outlives the next message in the thread.
		if msg.muted {
			m.notice = "muted " + msg.thread + ", it will not nudge you again"
			break
		}
		m.notice = "unmuted " + msg.thread
	case claimedMsg:
		if msg.result.Granted {
			m.notice = "claimed " + msg.result.Claim.Thread
			break
		}
		// The holder and their note, which is the output that stops duplicate work. A lost claim is an
		// outcome rather than an error, so it does not go where a failed reload goes.
		m.notice = fmt.Sprintf("%s is held by %s", msg.result.Claim.Thread, msg.result.Claim.Holder)
		if msg.result.Claim.Note != "" {
			m.notice += ": " + msg.result.Claim.Note
		}
	case releasedMsg:
		switch {
		case msg.result.Released:
			m.notice = "released " + msg.thread
		case msg.result.Claim.Holder != "":
			m.notice = fmt.Sprintf("%s is held by %s, not you", msg.thread, msg.result.Claim.Holder)
		default:
			m.notice = "nobody holds " + msg.thread
		}
	case previewMsg:
		// Only when it is still about what is selected. A reload could have settled the selection elsewhere
		// while the question was in flight.
		if msg.channel == m.channel && msg.thread == m.thread {
			m.confirm = m.deleteQuestion(msg)
		}
	case deletedMsg:
		m.confirm = nil
		m.notice = "deleted " + msg.thread
		if msg.thread == m.thread {
			m.thread, m.messages = "", nil
		}
	case previewChannelMsg:
		// Only when it is still about the selected channel, the same as a thread's: a reload could have settled
		// the selection elsewhere while the question was in flight.
		if msg.channel == m.channel {
			m.confirm = m.deleteChannelQuestion(msg)
		}
	case deletedChannelMsg:
		m.confirm = nil
		m.notice = "deleted " + msg.channel
		m = m.dropChannel(msg.channel)
	case leavePreviewMsg:
		if msg.channel == m.channel && msg.member == m.member {
			m.confirm = m.leaveQuestion(msg)
		}
	case leftMsg:
		m.confirm = nil
		m.notice = "removed " + msg.member
		if msg.member == m.member {
			m.member = ""
		}
	case errMsg:
		m.confirm = nil
		m.err = msg.err
	}
	// Every branch above can change what the message pane holds or how much room it has, and the viewport has
	// to be told both before View reads it.
	return m.refit(), quit
}

// appendMessage adds one message to the pane if it belongs there. The watch and the periodic reload overlap by
// design, so the same message arrives twice.
func (m Model) appendMessage(msg store.Message) Model {
	if msg.Channel != m.channel || msg.Thread != m.thread {
		return m
	}
	if len(m.messages) > 0 && msg.Seq <= m.messages[len(m.messages)-1].Seq {
		return m
	}
	m.messages = append(m.messages, msg)
	if !m.follow {
		m.unseen++
	}
	return m
}

// settleSelection keeps the selection pointing at something that exists. A thread can be dismissed or a
// channel can appear between reloads, and a view that silently showed the wrong thread's messages would be
// worse than one that moved the cursor.
func (m Model) settleSelection() Model {
	if len(m.channels) > 0 && indexOfChannel(m.channels, m.channel) < 0 {
		m.channel = m.channels[0].Key
		m.thread = ""
	}
	if m.thread != "" && indexOfThread(m.threads, m.thread) < 0 {
		m.thread = ""
	}
	if m.thread == "" && len(m.threads) > 0 {
		m.thread = m.threads[0].Name
	}
	if m.member != "" && indexOfMember(m.members, m.member) < 0 {
		m.member = ""
	}
	if m.member == "" && len(m.members) > 0 {
		// Yourself, when you are in the roster: the first thing anybody wants from this column is which
		// worktree somebody else is in, and starting on your own row makes that one keystroke rather than a
		// hunt for where the cursor is.
		m.member = m.members[0].Name
		if indexOfMember(m.members, m.cfg.Member.Value) >= 0 {
			m.member = m.cfg.Member.Value
		}
	}
	return m
}

func (m Model) handleKey(msg tea.KeyMsg) (Model, bool) {
	// A prompt owns the keyboard, so j is a letter rather than a movement while one is open. It also wants a
	// run of runes whole, since that is what a paste is, which is why the split below is on this side of it.
	if m.compose != nil {
		return m.composeKey(msg)
	}
	// bubbletea reports every rune that arrived in one read as one event, so holding j down long enough for the
	// repeats to land together arrives as "jjj" and matches no binding at all: the view freezes while the key
	// is held. Found by driving a pty, where writing "ll" in one call moved nothing.
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 && !msg.Alt {
		quit := false
		for _, r := range msg.Runes {
			m, quit = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			if quit {
				return m, true
			}
			// A key that opened a prompt owns the rest of the run, which is then text going into it.
			if m.compose != nil || m.confirm != nil {
				return m, false
			}
		}
		return m, false
	}
	// A pending question owns it the same way. Anything but yes is no, because a keystroke meant for the view
	// must never be read as consent to delete.
	if m.confirm != nil {
		switch {
		case key.Matches(msg, m.keys.Confirm):
			m.pending = m.confirm.work
			m.confirm = nil
		case msg.Type == tea.KeyCtrlC:
			return m, true
		default:
			m.confirm = nil
		}
		return m, false
	}

	// The last outcome stays on screen until something else is asked for, so it can be read.
	m.notice = ""

	switch {
	// Before Quit, which esc is also bound to: esc closes the key list rather than quitting, because it is
	// the key you reach for to close something and quitting the whole view is a surprising way to be answered.
	case m.showKeys && key.Matches(msg, m.keys.Cancel):
		m.showKeys = false
	case key.Matches(msg, m.keys.Quit):
		return m, true
	case key.Matches(msg, m.keys.Help):
		m.showKeys = !m.showKeys
	case key.Matches(msg, m.keys.Post):
		// The destination is fixed here rather than read at submit time, for the same reason a question carries
		// its work. With nothing selected this asks for a name first: an empty channel where the post key does
		// nothing is the dead-key report all over again.
		step := stepBody
		if m.thread == "" {
			step = stepThread
		}
		m.compose = m.newCompose(m.thread, step)
	case key.Matches(msg, m.keys.Thread):
		m.compose = m.newCompose("", stepThread)
	case key.Matches(msg, m.keys.Ack):
		// Reading in a view is not acknowledging, so this is the only thing that moves a cursor.
		if m.thread != "" {
			m.pending = pendingWork{action: actionAck, thread: m.thread}
		}
	case key.Matches(msg, m.keys.AckAll):
		m = m.ackAll()
	case key.Matches(msg, m.keys.Mute):
		// One key both ways, because a muted thread is still in this list and a second key to unmute it would
		// be one nobody finds.
		if m.thread != "" {
			m.pending = pendingWork{action: actionMute, thread: m.thread, unmute: m.selectedMuted()}
		}
	case key.Matches(msg, m.keys.Claim):
		if m.thread != "" {
			m.compose = m.newCompose(m.thread, stepNote)
		}
	case key.Matches(msg, m.keys.Release):
		m = m.release()
	case key.Matches(msg, m.keys.Delete):
		// What d removes is whatever the keyboard is on, which is the rule the pane titles already imply.
		// All three go through a question built from a dry run.
		switch {
		// A row in the list rather than the configured channel, which the selection starts on before anything
		// has loaded: d deletes what the keyboard is on, and before the first snapshot it is on nothing.
		case m.focus == paneChannels && indexOfChannel(m.channels, m.channel) >= 0:
			m.pending = pendingWork{action: actionPreviewDeleteChannel, channel: m.channel}
		case m.focus == paneMembers && m.member != "":
			m.pending = pendingWork{action: actionPreviewLeave, member: m.member}
		case m.focus != paneMembers && m.thread != "":
			m.pending = pendingWork{action: actionPreviewDelete, thread: m.thread}
		}
	case key.Matches(msg, m.keys.NextPane):
		m.focus = (m.focus + 1) % panes
	case key.Matches(msg, m.keys.PrevPane):
		// Plus panes before the subtraction, since Go's % keeps the sign of its left operand and a focus of -1
		// is a pane that does not exist.
		m.focus = (m.focus + panes - 1) % panes
	case key.Matches(msg, m.keys.Right):
		if m.focus < paneMembers {
			m.focus++
		}
	case key.Matches(msg, m.keys.Left):
		if m.focus > paneChannels {
			m.focus--
		}
	case key.Matches(msg, m.keys.Down):
		m = m.move(1)
	case key.Matches(msg, m.keys.Up):
		m = m.move(-1)
	case key.Matches(msg, m.keys.Top):
		switch m.focus {
		case paneMessages:
			m.follow = false
			m.viewport.GotoTop()
		default:
			m = m.jump(0)
		}
	case key.Matches(msg, m.keys.Bottom):
		switch m.focus {
		case paneMessages:
			m.follow = true
		case paneThreads:
			m = m.jump(len(m.threads) - 1)
		case paneChannels:
			m = m.jump(len(m.channels) - 1)
		case paneMembers:
			m = m.jump(len(m.members) - 1)
		}
	case key.Matches(msg, m.keys.Follow):
		if m.follow {
			m = m.unfollow()
		} else {
			m.follow = true
		}
	}
	if m.focus == paneMessages && m.follow {
		m.unseen = 0
	}
	return m, false
}

// composeKey edits the open prompt. Everything that is not an editing key is a character, including the ones
// that move panes when no prompt is open, so a message can contain the word "did".
func (m Model) composeKey(msg tea.KeyMsg) (Model, bool) {
	c := *m.compose
	// Text first, and never matched against a binding. bubbletea reports the runes that arrived in one read as
	// one event, and a binding matches on that event's whole text, so a word typed quickly can be a key name:
	// typing "the whole thing works end to end" posted "the whole thing works  to". One rune per Update for the
	// same reason, since textinput matches the same way. A single rune cannot collide with a key name.
	if runes := runesOf(msg); len(runes) > 0 && !msg.Alt {
		c = m.sizeInput(c)
		for _, r := range runes {
			c.line, _ = c.line.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
		m.compose = &c
		return m, false
	}

	switch {
	case msg.Type == tea.KeyCtrlC:
		return m, true
	case key.Matches(msg, m.keys.Cancel):
		m.compose = nil
		return m, false
	case key.Matches(msg, m.keys.Submit):
		return m.submit(c), false
	case msg.Alt:
		// An alt combination is not text. textinput does not check, so alt+p would type a p.
		return m, false
	default:
		// An editing key: backspace, the word and line deletes, home and end. The command is dropped, which is
		// what a static cursor makes safe, since the only one textinput returns is the blink timer.
		c.line, _ = c.line.Update(msg)
	}
	m.compose = &c
	return m, false
}

// submit takes one step of a prompt: the next line, or the work itself.
func (m Model) submit(c compose) Model {
	text := strings.TrimSpace(c.line.Value())
	switch c.step {
	case stepThread:
		// An empty answer cancels rather than doing nothing, because a key that appears dead is the worse of
		// the two: nothing has been typed to lose, and the prompt disappearing says it was heard.
		if text == "" {
			m.compose = nil
			return m
		}
		c.thread, c.step = text, stepBody
		c.line.Reset()
		m.compose = &c
	case stepBody:
		if text == "" {
			m.compose = nil
			return m
		}
		m.compose = nil
		m.pending = pendingWork{action: actionPost, thread: c.thread, body: text}
	case stepNote:
		// An empty note is allowed here, unlike an empty message, because a claim carrying no prose is still a
		// claim and `agora claim <thread>` on its own is a valid call.
		c.note, c.step = text, stepPaths
		c.line.Reset()
		m.compose = &c
	case stepPaths:
		m.compose = nil
		m.pending = pendingWork{action: actionClaim, thread: c.thread, body: c.note, paths: splitPaths(text)}
	}
	return m
}

// release gives up the selected thread's claim, asking first when it is not yours. The question is built here
// from what is already on screen rather than from a dry run, since the holder is in the snapshot.
func (m Model) release() Model {
	claim := m.selectedClaim()
	switch {
	case m.thread == "":
		return m
	case claim == nil:
		m.notice = "nobody holds " + m.thread
	case claim.Holder == m.cfg.Member.Value:
		m.pending = pendingWork{action: actionRelease, thread: m.thread}
	default:
		// A claim held by a member that looks idle may belong to an agent waiting on its user, which is why
		// this is a question naming them rather than a keystroke.
		m.confirm = &question{
			channel: m.channel,
			thread:  m.thread,
			text: fmt.Sprintf("release %s, held by %s for %s", m.thread, claim.Holder,
				m.since(claim.CreatedAt)),
			work: pendingWork{action: actionRelease, thread: m.thread, force: true},
		}
	}
	return m
}

// ackAll dismisses every thread in the channel that still nudges this member, asking first. It is the only key
// that acts on more than what is selected, and dismissing is a judgement about relevance rather than a way to
// clear a backlog: what you dismiss nobody tells you again.
//
// The question is built from the snapshot rather than from a dry run, because the count is already on screen.
// Muted threads are not in it, since they are not in the inbox either.
func (m Model) ackAll() Model {
	waiting := make([]string, 0, len(m.threads))
	for _, thread := range m.threads {
		if thread.Unread > 0 && !thread.Muted {
			waiting = append(waiting, thread.Name)
		}
	}
	if len(waiting) == 0 {
		m.notice = "nothing is waiting in this channel"
		return m
	}
	m.confirm = &question{
		channel: m.channel,
		text:    fmt.Sprintf("mark %s read: %s", plural(len(waiting), "thread"), strings.Join(waiting, ", ")),
		// No thread, which is what the store reads as every thread, and the same thing agora ack --all does.
		work: pendingWork{action: actionAck},
	}
	return m
}

// deleteQuestion turns a dry run into the sentence it has to be asked as. The numbers are the question:
// "delete this thread?" is not something anybody can answer. A claim by somebody else is named here rather
// than refused, which is what the CLI needs --force for, because a person looking at the holder can make
// that call.
func (m Model) deleteQuestion(preview previewMsg) *question {
	text := fmt.Sprintf("delete %s: %s, %s", preview.thread,
		plural(int(preview.messages), "message"), plural(int(preview.cursors), "cursor"))
	if preview.claim != "" {
		text += ", claimed by " + preview.claim
	}
	return &question{
		channel: preview.channel,
		thread:  preview.thread,
		text:    text,
		work:    pendingWork{action: actionDelete, thread: preview.thread, force: true},
	}
}

// deleteChannelQuestion is the same shape as a thread's, on the thing one level up: a channel is every
// discussion a repository has had, so the numbers in the question are the point rather than a courtesy.
//
// The CLI refuses a channel that has been used and takes --force. Here the question names what is in it, so
// somebody answering y has already read the volume and the holders, which is what --force is standing in for.
func (m Model) deleteChannelQuestion(preview previewChannelMsg) *question {
	// Ordered by what a narrow terminal can afford to lose, since this is the longest line the view asks
	// anybody: a channel key is a whole path, so the counts are what falls off the end rather than the name,
	// who is working in there, or that it is the repository you are standing in.
	text := "delete channel " + preview.channel
	if preview.channel == m.cfg.Channel.Value {
		text += ", which you are in"
	}
	if len(preview.claims) > 0 {
		text += ", claimed by " + strings.Join(preview.claims, ", ")
	}
	text += fmt.Sprintf(": %s, %s, %s", plural(int(preview.threads), "thread"),
		plural(int(preview.messages), "message"), plural(int(preview.members), "member"))
	return &question{
		channel: preview.channel,
		text:    text,
		work:    pendingWork{action: actionDeleteChannel, channel: preview.channel, force: true},
	}
}

// dropChannel takes a deleted channel out of the list and moves the selection off it, to the row that took its
// place or the one before it.
//
// The selection has to move rather than wait for the reload, because the loader polls the selected channel and
// every command that names a channel creates it: left where it was, the view would recreate what it just
// deleted and show it back, empty.
func (m Model) dropChannel(key string) Model {
	index := indexOfChannel(m.channels, key)
	if index < 0 {
		return m
	}
	m.channels = append(m.channels[:index:index], m.channels[index+1:]...)
	if key != m.channel {
		return m
	}
	m.thread, m.member = "", ""
	m.threads, m.messages, m.claims, m.members = nil, nil, nil, nil
	m.follow, m.unseen = true, 0
	m.viewport.GotoTop()
	m.channel = ""
	if len(m.channels) > 0 {
		m.channel = m.channels[clamp(index, 0, len(m.channels)-1)].Key
	}
	return m
}

// leaveQuestion is what removing a member has to be asked as. It names the claims, since those are what would
// be handed back, and not the cursors, which leaving keeps.
func (m Model) leaveQuestion(preview leavePreviewMsg) *question {
	text := "remove " + preview.member + " from the roster"
	if len(preview.claims) > 0 {
		text += ", releasing " + strings.Join(preview.claims, ", ")
	}
	if preview.member == m.cfg.Member.Value {
		text += ", which is you"
	}
	return &question{
		channel: preview.channel,
		member:  preview.member,
		text:    text,
		work:    pendingWork{action: actionLeave, member: preview.member, force: true},
	}
}

// handleMouse is the whole mouse surface: dragging a divider, and the wheel over the message column. Nothing
// else is interpreted, because a click that selects a row would also have to decide what a click on a prompt or
// a pending question means, and the keyboard already answers those.
//
// Capture is on for the whole session, which takes click-drag selection away from the terminal. Shift-drag gets
// it back in kitty, iTerm2 and xterm, and that trade is the reason resizing is a drag rather than a mode: a
// mode would be a key to remember for something a pointer already knows how to do.
func (m Model) handleMouse(msg tea.MouseMsg) Model {
	switch {
	case msg.Button == tea.MouseButtonWheelUp:
		m.viewport.ScrollUp(wheelLines)
		m.follow = false
		return m
	case msg.Button == tea.MouseButtonWheelDown:
		m.viewport.ScrollDown(wheelLines)
		if m.viewport.AtBottom() {
			// Reaching the end by wheel is the same intent as pressing f: keep up from here.
			m.follow, m.unseen = true, 0
		}
		return m
	case msg.Action == tea.MouseActionRelease:
		m.drag = nil
		return m
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
		m.drag = m.dividerAt(msg.X)
		return m
	case msg.Action == tea.MouseActionMotion && m.drag != nil:
		drag := *m.drag
		m.sidebars[drag.resizing] = m.clampSidebar(drag.resizing,
			drag.startWidth+drag.sign*(msg.X-drag.startX))
		return m
	}
	return m
}

// dividerAt is the drag a press at this column starts, nil for a press anywhere else. The columns are computed
// the same way the frame is drawn, so the divider a pointer is on is the one the eye is on.
func (m Model) dividerAt(x int) *dragState {
	shown := m.columns()
	if len(shown) < 2 {
		return nil
	}
	at := 0
	for i, p := range shown[:len(shown)-1] {
		if p == paneMessages {
			at += m.messagesWidth()
		} else {
			at += m.paneWidth(p)
		}
		// A column either side counts as the divider. It is one column wide, and asking a pointer to land on
		// exactly it is asking too much; nothing else here reads a press, so the tolerance costs nothing.
		if x < at-1 || x > at+1 {
			at++ // the divider column itself
			continue
		}
		// The sidebar this divider moves. Left of it, unless that is the message column, which has no width of
		// its own: then it is the pane on the right, and the pointer moves its width the other way.
		resizing, sign := p, 1
		if p == paneMessages {
			resizing, sign = shown[i+1], -1
		}
		// startX is the divider rather than where the press landed, so a grab a column off does not shift the
		// pane by that column as soon as the pointer moves.
		return &dragState{resizing: resizing, sign: sign, startX: at, startWidth: m.paneWidth(resizing)}
	}
	return nil
}

// clampSidebar keeps a drag inside what the layout can render: wide enough to hold a name, and never so wide
// that the message column drops below the width it is protected at. Without the upper bound a drag would push
// panes out of the layout it is being dragged in.
func (m Model) clampSidebar(p pane, width int) int {
	room := m.width - minMessagesWidth
	for _, shownPane := range m.columns() {
		if shownPane != paneMessages {
			room -= 1 // its divider
			if shownPane != p {
				room -= m.paneWidth(shownPane)
			}
		}
	}
	return min(max(width, minSidebarWidth), max(room, minSidebarWidth))
}

// runesOf is the text a key carries, empty for every key that is not text. Space arrives as its own type and
// still carries its rune, so both paths land here.
func runesOf(msg tea.KeyMsg) []rune {
	if msg.Type != tea.KeyRunes && msg.Type != tea.KeySpace {
		return nil
	}
	return msg.Runes
}

// splitPaths reads a claim's globs the way the CLI's --paths does, so what works there works here.
func splitPaths(text string) []string {
	var paths []string
	for field := range strings.SplitSeq(text, ",") {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			paths = append(paths, trimmed)
		}
	}
	return paths
}

// move steps the focused pane's selection, or scrolls the messages.
func (m Model) move(delta int) Model {
	switch m.focus {
	case paneChannels:
		return m.jump(indexOfChannel(m.channels, m.channel) + delta)
	case paneThreads:
		return m.jump(indexOfThread(m.threads, m.thread) + delta)
	case paneMembers:
		return m.jump(indexOfMember(m.members, m.member) + delta)
	default:
		// The viewport clamps, so pressing j at the bottom fifty times does not mean pressing k fifty times
		// to move at all.
		m = m.unfollow()
		if delta > 0 {
			m.viewport.ScrollDown(delta)
		} else {
			m.viewport.ScrollUp(-delta)
		}
		return m
	}
}

// jump selects by index in the focused pane, clamped, and resets what depends on it.
func (m Model) jump(index int) Model {
	switch m.focus {
	case paneChannels:
		if len(m.channels) == 0 {
			return m
		}
		index = clamp(index, 0, len(m.channels)-1)
		if key := m.channels[index].Key; key != m.channel {
			// A different channel has different threads, so nothing below this selection survives it.
			m.channel, m.thread = key, ""
			m.threads, m.messages, m.claims, m.members = nil, nil, nil, nil
			m.follow, m.unseen = true, 0
			m.viewport.GotoTop()
		}
	case paneThreads:
		if len(m.threads) == 0 {
			return m
		}
		index = clamp(index, 0, len(m.threads)-1)
		if name := m.threads[index].Name; name != m.thread {
			m.thread = name
			m.messages = nil
			m.follow, m.unseen = true, 0
			m.viewport.GotoTop()
		}
	case paneMembers:
		// Nothing below a member, so moving here loads nothing and disturbs nothing: it is a column you read
		// while looking at a conversation, to see whose worktree a name belongs to.
		if len(m.members) == 0 {
			return m
		}
		m.member = m.members[clamp(index, 0, len(m.members)-1)].Name
	}
	return m
}

// unfollow stops the window tracking the newest message, leaving it where it already is. It used to jump to
// the bottom first, which is where following had it anyway; what the jump was actually for was that the offset
// was not maintained while following, and the viewport keeps it now.
func (m Model) unfollow() Model {
	m.follow = false
	return m
}

// refit gives the viewport the size and the content the current layout implies, and pins it to the newest
// message while following. Called at the end of Update rather than from View, because the viewport holds the
// scroll position: View has to stay a function of state, and this is what keeps that state true.
func (m Model) refit() Model {
	width := m.messagesWidth()
	m.viewport.Width = width
	m.viewport.Height = max(1, m.paneHeight()-len(m.messageHeaderLines(width)))
	m.viewport.SetContent(strings.Join(m.renderMessages(), "\n"))
	if m.follow {
		m.viewport.GotoBottom()
	}
	return m
}

// View renders the frame.
func (m Model) View() string {
	header := m.headerLines()
	state := m.stateLines()
	footer := m.footerLine()

	height := m.height - len(header) - len(state) - 1
	if height < 1 {
		height = 1
	}
	lines := append([]string{}, header...)
	lines = append(lines, m.paneLines(height)...)
	lines = append(lines, state...)
	lines = append(lines, footer)
	for i, line := range lines {
		lines[i] = truncate(line, m.width)
	}
	return strings.Join(lines, "\n")
}

// paneHeight is how many lines the pane row gets, which a half page and the scroll clamp both need.
func (m Model) paneHeight() int {
	height := m.height - len(m.headerLines()) - len(m.stateLines()) - 1
	if height < 1 {
		return 1
	}
	return height
}

// paneLines lays the columns side by side, or gives the whole width to the focused one when there is not enough
// room to share.
func (m Model) paneLines(height int) []string {
	if m.showKeys {
		return column(m.helpLines(height), height)
	}
	shown := m.columns()
	if len(shown) == 1 {
		return column(m.paneContent(shown[0], m.width, height), height)
	}

	widths := make([]int, len(shown))
	for i, p := range shown {
		widths[i] = m.paneWidth(p)
	}
	columns := make([][]string, len(shown))
	for i, p := range shown {
		if p == paneMessages {
			widths[i] = m.messagesWidth()
		}
		columns[i] = column(m.paneContent(p, widths[i], height), height)
	}

	lines := make([]string, height)
	for row := range lines {
		var b strings.Builder
		for i := range shown {
			if i > 0 {
				b.WriteString(dim.Render("|"))
			}
			if i == len(shown)-1 {
				// The last column is not padded, so no frame carries trailing space it does not need.
				b.WriteString(columns[i][row])
				continue
			}
			b.WriteString(pad(columns[i][row], widths[i]))
		}
		lines[row] = b.String()
	}
	return lines
}

// columns is which panes get one, in order. The message column is what the sidebars are dropped to protect,
// since a channel list beside an unreadable conversation is not a useful trade, and the focused pane always
// gets shown even when it is one that was dropped: a selection you cannot see is worse than a missing column.
func (m Model) columns() []pane {
	var shown []pane
	sidebars := m.paneWidth(paneChannels) + m.paneWidth(paneThreads)
	switch {
	case m.width >= sidebars+m.paneWidth(paneMembers)+3+minMessagesWidth:
		shown = []pane{paneChannels, paneThreads, paneMessages, paneMembers}
	case m.width >= sidebars+2+minMessagesWidth:
		shown = []pane{paneChannels, paneThreads, paneMessages}
	default:
		return []pane{m.focus}
	}
	for _, p := range shown {
		if p == m.focus {
			return shown
		}
	}
	return []pane{m.focus}
}

// messagesWidth is what the message column has to wrap and scroll within, which the layout, the wrapping, and
// the scroll clamp all have to agree on.
func (m Model) messagesWidth() int {
	shown := m.columns()
	if len(shown) == 1 {
		return m.width
	}
	width := m.width - (len(shown) - 1)
	for _, p := range shown {
		if p == paneMessages {
			continue
		}
		width -= m.paneWidth(p)
	}
	return width
}

func (m Model) paneContent(p pane, width, height int) []string {
	switch p {
	case paneChannels:
		return m.channelLines(width)
	case paneThreads:
		return m.threadLines(width)
	case paneMembers:
		return m.memberLines(width)
	default:
		return m.messageLines(width, height)
	}
}

// helpLines is the whole key map, rendered from the same bindings the keys dispatch on, so it cannot describe
// a key that does something else. Grouped in columns rather than one row per key, because it has to fit the
// pane area on a short terminal and a key list that scrolls off is a key list that lies.
func (m Model) helpLines(height int) []string {
	m.help.ShowAll = true
	m.help.Width = m.width
	lines := []string{m.paneTitle("KEYS", m.focus, m.width)}
	for _, line := range strings.Split(m.help.View(m.keys), "\n") {
		lines = append(lines, " "+line)
	}
	// What the keys do depends on which pane has them, and the list cannot say that: d deletes a thread in one
	// pane and a member in another.
	return append(lines, "", " "+dim.Render("in the "+m.focus.String()+" pane: "+m.focus.keyHint()))
}

func (m Model) channelLines(width int) []string {
	lines := []string{m.paneTitle("CHANNELS", paneChannels, width)}
	if len(m.channels) == 0 {
		return append(lines, dim.Render(" no channels yet"))
	}
	names := make([]string, 0, len(m.channels))
	badges := make([]string, 0, len(m.channels))
	for _, channel := range m.channels {
		// The last path element, because a channel key is a repository path and the sidebar has no room
		// for one. The full path of the selected channel is in the header.
		names = append(names, lastElement(channel.Key))
		badge := ""
		if channel.UnreadThreads > 0 {
			badge = fmt.Sprintf("%d*", channel.UnreadThreads)
		}
		badges = append(badges, badge)
	}
	badges = padBadges(badges)
	for i, name := range names {
		lines = append(lines, m.row(name, badges[i], width,
			m.channels[i].Key == m.channel, m.focus == paneChannels))
	}
	return lines
}

// selectedMuted is whether the selected thread is one this member has muted, which is what makes m a toggle.
func (m Model) selectedMuted() bool {
	for _, thread := range m.threads {
		if thread.Name == m.thread {
			return thread.Muted
		}
	}
	return false
}

func (m Model) threadLines(width int) []string {
	lines := []string{m.paneTitle("THREADS", paneThreads, width)}
	if !m.loaded {
		return append(lines, dim.Render(" loading..."))
	}
	if len(m.threads) == 0 {
		return append(lines, dim.Render(" nothing posted here yet"))
	}
	badges := make([]string, 0, len(m.threads))
	for _, thread := range m.threads {
		badge := ""
		if thread.Unread > 0 {
			badge = fmt.Sprintf("%d*", thread.Unread)
		}
		// A muted thread is marked even with nothing new, because "why is this one quiet" is the question the
		// marker answers, and its count carries a dash rather than a star: what has piled up in it is not
		// waiting on anybody.
		if thread.Muted {
			badge = "-"
			if thread.Unread > 0 {
				badge = fmt.Sprintf("%d-", thread.Unread)
			}
		}
		badges = append(badges, badge)
	}
	badges = padBadges(badges)
	for i, thread := range m.threads {
		lines = append(lines, m.row(thread.Name, badges[i], width,
			thread.Name == m.thread, m.focus == paneThreads))
	}
	return lines
}

// memberLines is the roster: who is here, and who is behind. The badge is threads over messages, the pair the
// roster is consulted for: how many decisions are waiting, and how much reading they add up to.
func (m Model) memberLines(width int) []string {
	lines := []string{m.paneTitle("MEMBERS", paneMembers, width)}
	if len(m.members) == 0 {
		return append(lines, dim.Render(" nobody here yet"))
	}
	badges := make([]string, 0, len(m.members))
	for _, member := range m.members {
		badge := ""
		if member.UnreadThreads > 0 {
			badge = fmt.Sprintf("%d/%d", member.UnreadThreads, member.Unread)
		}
		badges = append(badges, badge)
	}
	badges = padBadges(badges)
	for i, member := range m.members {
		lines = append(lines, m.row(member.Name, badges[i], width,
			member.Name == m.member, m.focus == paneMembers))
	}
	return lines
}

// messageLines is the selected thread: who owns it, then what was said, scrolled to wherever the viewport is.
func (m Model) messageLines(width, height int) []string {
	return append(m.messageHeaderLines(width), strings.Split(m.viewport.View(), "\n")...)
}

// messageHeaderLines is the pane's title and the claim on the thread, which sit above the scrolling part. The
// claim belongs to the thread, so it reads as the thread's header rather than as a separate block somewhere
// else on the screen.
func (m Model) messageHeaderLines(width int) []string {
	title := m.thread
	if title == "" {
		title = "no thread selected"
	}
	lines := []string{m.paneTitle(title, paneMessages, width)}
	claim := m.selectedClaim()
	if claim == nil {
		return lines
	}
	lines = append(lines, dim.Render(fmt.Sprintf(" held by %s for %s", claim.Holder, m.since(claim.CreatedAt))))
	if len(claim.Paths) > 0 {
		lines = append(lines, dim.Render(" "+strings.Join(claim.Paths, ", ")))
	}
	for _, line := range wrap(claim.Note, width-1) {
		lines = append(lines, dim.Render(" "+line))
	}
	return lines
}

func (m Model) renderMessages() []string {
	if !m.loaded {
		return []string{dim.Render(" loading...")}
	}
	if m.thread == "" {
		return []string{dim.Render(" nothing to show")}
	}
	if len(m.messages) == 0 {
		return []string{dim.Render(" claimed, but nothing posted yet")}
	}
	width := m.messagesWidth()
	var lines []string
	for _, msg := range m.messages {
		// Numbered within the thread, since an id counts every other thread's messages too.
		lines = append(lines, fmt.Sprintf(" #%d  %s  %s", msg.Number, strong.Render(msg.Author),
			dim.Render(msg.CreatedAt.In(m.now().Location()).Format(time.TimeOnly))))
		for _, line := range wrap(msg.Body, width-5) {
			lines = append(lines, "     "+line)
		}
	}
	return lines
}

func (m Model) selectedClaim() *store.Claim {
	for i, claim := range m.claims {
		if claim.Thread == m.thread {
			return &m.claims[i]
		}
	}
	return nil
}

func (m Model) headerLines() []string {
	// The database and where it came from are on screen at all times on purpose. agora's whole purpose is
	// to be read by other agents, so "which channel am I actually looking at" is the first question worth
	// answering, and a view that hid it would be the one place isolation could not be checked.
	return []string{
		m.row2(strong.Render("agora ")+m.channel, "you: "+m.cfg.Member.Value),
		m.row2(dim.Render(fmt.Sprintf("db %s (%s)", m.cfg.Database.Value, m.cfg.Database.Source)),
			dim.Render(m.countsSummary())),
	}
}

func (m Model) countsSummary() string {
	unread := int64(0)
	for _, thread := range m.threads {
		unread += thread.Unread
	}
	return fmt.Sprintf("%s  %s unread", plural(len(m.threads), "thread"), plural(int(unread), "message"))
}

// stateLines is the selected member in full. The roster used to sit here as one line of everybody, which fits
// four names and answers nothing about any of them: the question a name raises while reading a conversation is
// which tree that agent is working in, and a path does not fit in a column.
func (m Model) stateLines() []string {
	line := dim.Render(strings.Repeat("-", m.width))
	member := m.selectedMember()
	if member == nil {
		return []string{line, dim.Render("nobody here yet")}
	}
	left := fmt.Sprintf("%s  %s / %s unread  seen %s", strong.Render(member.Name),
		plural(int(member.UnreadThreads), "thread"), plural(int(member.Unread), "message"),
		m.ago(member.SeenAt))
	if member.Name == m.cfg.Member.Value {
		left += "  (you)"
	}
	// The end of the worktree rather than the start, because that is the part that tells two checkouts of one
	// repository apart, and the start is the same for all of them.
	lines := []string{line, m.row2(left, dim.Render(tail(member.Worktree, m.width-lipgloss.Width(left)-2)))}
	if member.Description == "" {
		return lines
	}
	// A second line only when there is something to put on it. It shifts the panes by a row as the selection
	// moves between members who have said what they are doing and members who have not, which is worth it: the
	// alternative is a row of "-" kept permanently in case somebody fills it in.
	return append(lines, dim.Render("  "+truncate(member.Description, m.width-2)))
}

func (m Model) selectedMember() *store.Member {
	for i, member := range m.members {
		if member.Name == m.member {
			return &m.members[i]
		}
	}
	return nil
}

func (m Model) footerLine() string {
	if c := m.compose; c != nil {
		return m.promptLine(*c)
	}
	if c := m.confirm; c != nil {
		// The keys go next to the question, not at the far edge. Right-aligned they sat sixty columns away
		// from what they answered, and read as decoration rather than as the two keys that do anything.
		//
		// The question is what gives way when the line is too narrow, never the keys. Truncating the whole
		// thing took "[y/n]" off the end of a question naming a channel and its counts, leaving a confirmation
		// with nothing on screen saying how to answer it.
		const keys = "?  [y/n]"
		return m.row2(strong.Render(truncate(c.text, m.width-len(keys))+keys), "")
	}
	// A failure or an outcome takes the whole line. Sharing it with the keys cut a losing claim at
	// "is held by alice: fixing all ...", losing the note, which is the part that stops duplicate work.
	if m.err != nil {
		return m.row2(dim.Render("error: "+m.err.Error()), "")
	}
	if m.notice != "" {
		return m.row2(dim.Render(m.notice), "")
	}
	// The focused pane is named, not just marked: "nothing happens when I press j" is answered by an empty
	// pane. help truncates itself to its width, so a narrow terminal needs no second list.
	marker := fmt.Sprintf("  [%s]", m.focus)
	m.help.ShowAll = false
	m.help.Width = m.width - lipgloss.Width(marker)
	keys := m.help.View(m.keys) + dim.Render(marker)
	right := ""
	switch {
	case m.unseen > 0:
		right = fmt.Sprintf("%s below", plural(m.unseen, "new message"))
	case m.focus == paneMessages && m.follow:
		right = "following"
	case m.focus == paneMessages:
		right = "paused"
	}
	return m.row2(dim.Render(keys), dim.Render(right))
}

// promptLine is the line being typed, with the key that commits it alongside, for the same reason [y/n] is.
//
// A dim label and a gutter rather than a ">": the two ran together, and "post to release-1-3> cutting it" read
// as typing with the thread name still in it.
func (m Model) promptLine(c compose) string {
	label, hint := promptParts(c)
	return m.row2(dim.Render(label+promptGutter)+c.line.View(), dim.Render(hint))
}

// promptParts is what a prompt says: what it is for, and which key commits it.
func promptParts(c compose) (label, hint string) {
	switch c.step {
	case stepThread:
		return "new thread", "enter names it, esc cancels"
	case stepBody:
		return "post to " + c.thread, "enter posts, esc cancels"
	case stepNote:
		return "claim " + c.thread + ", note", "enter continues, esc cancels"
	default:
		return "claim " + c.thread + ", paths", "enter claims it, esc cancels"
	}
}

// paneTitle marks which column has the keyboard. Brackets rather than only bold, because bold against dim is
// too quiet to answer "why is nothing moving", which is the question it exists to answer.
func (m Model) paneTitle(title string, p pane, width int) string {
	if m.focus == p {
		return strong.Render("[" + title + "]")
	}
	return dim.Render(" " + title)
}

// row is one sidebar entry: the selection marker, this row's badge, then the name.
//
// The badge is on the left because it used to be on the right, where a long name pushed it off the end and the
// truncation ate it: a pane of threads showed nothing about which of them were unread, which is most of what a
// sidebar is for. Callers pad every badge in a pane to one width, so the names line up and the column of markers
// can be read straight down.
func (m Model) row(name, badge string, width int, isSelected, focused bool) string {
	marker := " "
	if isSelected {
		marker = ">"
	}
	text := truncate(marker+badge+name, width)
	if isSelected && focused {
		return selected.Render(pad(text, width))
	}
	return text
}

// row2 puts left and right on one line, truncated rather than wrapped, because a header that wraps changes
// the height of everything below it.
func (m Model) row2(left, right string) string {
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	if lw+rw+1 > m.width {
		if lw >= m.width {
			return truncate(left, m.width)
		}
		right = truncate(right, m.width-lw-1)
		rw = lipgloss.Width(right)
	}
	gap := m.width - lw - rw
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// padBadges gives every badge in a pane one width, right aligned, with a space after it, so the names beside
// them start in the same column and the badges themselves form a column that can be read down. An empty badge
// becomes spaces rather than nothing, for the same reason. A pane where nothing has a badge keeps its full
// width for names.
func padBadges(badges []string) []string {
	width := 0
	for _, badge := range badges {
		width = max(width, lipgloss.Width(badge))
	}
	if width == 0 {
		return badges
	}
	padded := make([]string, len(badges))
	for i, badge := range badges {
		padded[i] = fmt.Sprintf("%*s ", width, badge)
	}
	return padded
}

func (m Model) ago(t time.Time) string { return humanAgo(m.now().Sub(t)) }

// since is a bare duration, for "held for 4m" where "held for 4m ago" would read as nonsense.
func (m Model) since(t time.Time) string {
	return strings.TrimSuffix(humanAgo(m.now().Sub(t)), " ago")
}

// column pads a pane's lines out to the row height so the columns stay aligned.
func column(lines []string, height int) []string {
	if len(lines) > height {
		return lines[:height]
	}
	return append(lines, make([]string, height-len(lines))...)
}

func pad(s string, width int) string {
	if gap := width - lipgloss.Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return truncate(s, width)
}

func lastElement(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 && i < len(path)-1 {
		return path[i+1:]
	}
	return path
}

func indexOfChannel(channels []store.Channel, key string) int {
	for i, channel := range channels {
		if channel.Key == key {
			return i
		}
	}
	return -1
}

func indexOfThread(threads []store.Thread, name string) int {
	for i, thread := range threads {
		if thread.Name == name {
			return i
		}
	}
	return -1
}

func indexOfMember(members []store.Member, name string) int {
	for i, member := range members {
		if member.Name == name {
			return i
		}
	}
	return -1
}

// tail keeps the end of a string, for a path, where the end is the part that identifies it. Counted in runes
// rather than in display columns, because what this is given is a path and not something already styled.
func tail(s string, width int) string {
	if width < 1 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	if width <= 3 {
		return string(runes[len(runes)-width:])
	}
	return "..." + string(runes[len(runes)-(width-3):])
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// humanAgo is deliberately coarse. The question a roster answers is "is this member still here", and
// seconds of precision on an hour-old timestamp is noise.
func humanAgo(d time.Duration) string {
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func wrap(text string, width int) []string {
	if text == "" {
		return nil
	}
	if width < 8 {
		width = 8
	}
	var lines []string
	for paragraph := range strings.SplitSeq(text, "\n") {
		wrapped := lipgloss.NewStyle().Width(width).Render(paragraph)
		for _, line := range strings.Split(wrapped, "\n") {
			// lipgloss pads to the full width, which would leave wrapped lines trailing spaces while the
			// lines beside them have none.
			lines = append(lines, strings.TrimRight(line, " "))
		}
	}
	return lines
}

func truncate(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	if width <= 3 {
		return ansi.Truncate(s, width, "")
	}
	return ansi.Truncate(s, width, "...")
}
