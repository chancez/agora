package tui

import (
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
)

// keyMap is every binding, once: the footer, the `?` list, and what Update dispatches on all read it, so a key
// cannot be bound to one thing and described as another. As three lists they had already drifted, and n was
// described nowhere.
type keyMap struct {
	Up       key.Binding
	Down     key.Binding
	Left     key.Binding
	Right    key.Binding
	NextPane key.Binding
	PrevPane key.Binding
	Top      key.Binding
	Bottom   key.Binding
	Follow   key.Binding
	Post     key.Binding
	Thread   key.Binding
	Ack      key.Binding
	AckAll   key.Binding
	Mute     key.Binding
	Claim    key.Binding
	Release  key.Binding
	Delete   key.Binding
	Help     key.Binding
	Quit     key.Binding
	Cancel   key.Binding
	Confirm  key.Binding
	Submit   key.Binding
}

// Footer entries standing for a pair of bindings: rendered, never matched. They carry keys because help skips
// a binding with none, which is how they first vanished from the footer.
var (
	moveHelp = key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "move"))
	paneHelp = key.NewBinding(key.WithKeys("h", "l"), key.WithHelp("h/l", "pane"))
	// The mouse, which has no binding to describe itself: a divider drag and the wheel are the only two things
	// the pointer does, and the key list is the only place either is written down.
	dragHelp  = key.NewBinding(key.WithKeys("drag"), key.WithHelp("drag |", "resize"))
	wheelHelp = key.NewBinding(key.WithKeys("wheel"), key.WithHelp("wheel", "scroll"))
)

// ShortHelp is the footer. Movement, the pane keys, posting, and the way to the rest: enough to get started
// and short enough to leave room for the status beside it.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Quit, moveHelp, paneHelp, k.Post, k.Thread, k.Help}
}

// FullHelp is what ? shows, in columns: getting around, then acting, then the two that end something.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Down, k.Up, k.Right, k.Left},
		{k.NextPane, k.PrevPane, k.Top, k.Bottom, k.Follow},
		{dragHelp, wheelHelp},
		{k.Post, k.Thread, k.Ack, k.AckAll, k.Mute},
		{k.Claim, k.Release, k.Delete},
		{k.Help, k.Quit},
	}
}

// keyHint is what a pane's keys do to what is selected in it, which the key list cannot say: d deletes a
// thread in one pane and a member in another.
func (p pane) keyHint() string {
	switch p {
	case paneChannels:
		return "d deletes the selected channel"
	case paneMembers:
		return "d removes the selected member"
	case paneMessages:
		return "j/k scroll, f follows the newest"
	default:
		return "d deletes the selected thread"
	}
}

func defaultKeys() keyMap {
	return keyMap{
		Up:       key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k", "up")),
		Down:     key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j", "down")),
		Left:     key.NewBinding(key.WithKeys("h", "left"), key.WithHelp("h", "pane left")),
		Right:    key.NewBinding(key.WithKeys("l", "right", "enter"), key.WithHelp("l", "pane right")),
		NextPane: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next pane")),
		PrevPane: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "previous pane")),
		Top:      key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "top")),
		Bottom:   key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "bottom")),
		Follow:   key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "follow newest")),
		// p and n get confused for each other, so their descriptions are written against each other.
		Post:   key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "post here")),
		Thread: key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new thread")),
		Ack:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "mark read")),
		// Shifted, and asked about first, because it is the only key here that acts on every thread at once.
		AckAll: key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "mark all read")),
		// a and m are the two halves of dismissing, so they are described against each other: a is about what
		// is in the thread so far, m is about the thread.
		Mute:    key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "mute or unmute")),
		Claim:   key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "claim")),
		Release: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "release")),
		Delete:  key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete this")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "keys")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c", "esc"), key.WithHelp("q", "quit")),
		// Prompt and question keys, in neither help list: they are on screen beside what they answer.
		Cancel:  key.NewBinding(key.WithKeys("esc")),
		Confirm: key.NewBinding(key.WithKeys("y")),
		Submit:  key.NewBinding(key.WithKeys("enter")),
	}
}

// newHelp renders in plain ASCII. bubbles defaults to a unicode bullet and ellipsis.
func newHelp() help.Model {
	h := help.New()
	h.ShortSeparator = "  "
	h.FullSeparator = "   "
	h.Ellipsis = "..."
	h.Styles.ShortKey = strong
	h.Styles.ShortDesc = dim
	h.Styles.ShortSeparator = dim
	h.Styles.Ellipsis = dim
	h.Styles.FullKey = strong
	h.Styles.FullDesc = dim
	h.Styles.FullSeparator = dim
	return h
}
