package tui

import (
	"context"
	"time"

	"github.com/chancez/agora/internal/config"
	"github.com/chancez/agora/internal/store"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	// How much of a thread to load. A thread is one piece of work, so this is more history than any of them
	// will have, and the point of a bound is that a long one still opens instantly.
	historyLimit = 500
	// Claims, cursors, and threads all change without anybody posting, so a watch alone would show a live
	// log beside a stale sidebar. Reloading everything costs a handful of sub-millisecond queries.
	refreshInterval = 2 * time.Second
)

// Store is what the view may touch: everything a participant does, nothing a harness does. No Read, since
// reading here must not advance a cursor, and no Join, since being in the roster follows from posting. The
// compiler is what enforces that.
//
// Each method is one keystroke, and the two that cannot be undone go through a y/n built on a dry run.
type Store interface {
	Channels(ctx context.Context, member string) ([]store.Channel, error)
	Threads(ctx context.Context, req store.ThreadsRequest) ([]store.Thread, error)
	Messages(ctx context.Context, req store.MessagesRequest) ([]store.Message, error)
	Members(ctx context.Context, channel string) ([]store.Member, error)
	Claims(ctx context.Context, channel string) ([]store.Claim, error)
	Watch(ctx context.Context, opts store.WatchOptions) *store.Watcher
	Post(ctx context.Context, req store.PostRequest) (store.Message, error)
	Ack(ctx context.Context, req store.AckRequest) (store.AckResult, error)
	Mute(ctx context.Context, req store.MuteRequest) (store.MuteResult, error)
	Claim(ctx context.Context, req store.ClaimRequest) (store.ClaimResult, error)
	Release(ctx context.Context, req store.ReleaseRequest) (store.ReleaseResult, error)
	Leave(ctx context.Context, req store.LeaveRequest) (store.LeaveResult, error)
	Delete(ctx context.Context, req store.DeleteRequest) (store.DeleteResult, error)
	DeleteChannel(ctx context.Context, req store.DeleteChannelRequest) (store.DeleteChannelResult, error)
}

// Run shows the channels until the user quits or ctx ends.
func Run(ctx context.Context, agora Store, cfg config.Config) error {
	// Cell motion, which reports a drag rather than every idle move of the pointer: a divider drag needs the
	// motion while a button is held and nothing else. It costs the terminal's own click-drag selection for the
	// session, which shift-drag gets back.
	return runProgram(ctx, newProgram(ctx, agora, cfg, refreshInterval, nil,
		tea.WithAltScreen(), tea.WithMouseCellMotion()))
}

// newProgram and runProgram are separate so a test can build the program, hold on to it, and drive it over
// pipes. The model's own tests cover the layout; this is the only way to find out whether the wiring from
// store to screen actually runs.
func newProgram(ctx context.Context, agora Store, cfg config.Config, refresh time.Duration, layout layoutStore, opts ...tea.ProgramOption) *tea.Program {
	opts = append(opts, tea.WithContext(ctx))
	if layout == nil {
		layout = fileLayout{path: cfg.Layout.Value}
	}
	model := &program{Model: New(cfg, time.Now), agora: agora, ctx: ctx, refresh: refresh, layout: layout}
	return tea.NewProgram(model, opts...)
}

func runProgram(ctx context.Context, p *tea.Program) error {
	_, err := p.Run()
	if err != nil && ctx.Err() != nil {
		// Ending on a signal is how this command is meant to end.
		return nil
	}
	return err
}

// layoutStore is where pane widths are kept between sessions. An interface so a test can drive a drag without
// writing to the person's own file, and so the model stays a pure function of what it was handed.
type layoutStore interface {
	Load() (config.Layout, error)
	Save(config.Layout) error
}

// fileLayout is the shipped one: a small hand editable file, not a row in the shared database. The path comes
// from the resolved config, so it is the one agora config reports.
type fileLayout struct{ path string }

func (f fileLayout) Load() (config.Layout, error) { return config.LoadLayout(f.path) }
func (f fileLayout) Save(l config.Layout) error   { return config.SaveLayout(f.path, l) }

// program adapts the pure Model to bubbletea and owns everything that talks to the store. The split is what
// lets the layout be tested without a terminal and the store without a screen.
type program struct {
	Model
	agora Store
	// layout is the file the widths live in, and saved is what is in it, so a release that changed nothing
	// does not rewrite it.
	layout layoutStore
	saved  config.Layout
	ctx    context.Context
	// refresh is how often everything is reloaded. Injectable because it otherwise hides the watch: a
	// reload every couple of seconds brings in new messages too, so a test cannot tell a working watch from
	// a broken one unless the reload is turned down out of the way.
	refresh time.Duration
	watcher *store.Watcher
	// watching is the channel the current watch is on, so moving to another channel restarts it rather than
	// leaving it delivering messages the view is no longer showing.
	watching string
}

type tickMsg time.Time

func (p *program) Init() tea.Cmd {
	// Read before anything is drawn, so the first frame is already the width it was left at. A file that cannot
	// be read is reported and the defaults stand: it is a preference, and losing one is not worth refusing to
	// open the view over.
	if layout, err := p.layout.Load(); err != nil {
		p.Model, _ = p.Model.Update(errMsg{err: err})
	} else {
		p.saved = layout
		p.Model = p.Model.withLayout(layout)
	}
	return tea.Batch(p.load(), p.tick())
}

func (p *program) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.Model, _ = p.Model.Update(msg)
		return p, nil

	case tea.MouseMsg:
		// Forwarded like a key, because it is one as far as this type is concerned. Left out at first, and the
		// model tests could not see it: they call Update directly, so every one of them passed while nothing
		// the pointer did reached the model at all.
		p.Model, _ = p.Model.Update(msg)
		// On release rather than on every motion: a drag is dozens of events, and the width that matters is
		// where the pointer let go.
		if msg.Action == tea.MouseActionRelease {
			return p, p.saveLayout()
		}
		return p, nil

	case tea.KeyMsg:
		beforeChannel, beforeThread := p.Model.Selection()
		var quit bool
		p.Model, quit = p.Model.Update(msg)
		if quit {
			return p, tea.Quit
		}
		if work := p.Model.Pending(); work.action != actionNone {
			p.Model = p.Model.clearPending()
			return p, p.issue(work)
		}
		if channel, thread := p.Model.Selection(); channel != beforeChannel || thread != beforeThread {
			// The selection moved, so what is on screen is stale until this comes back.
			return p, p.load()
		}
		return p, nil

	case snapshotMsg:
		p.Model, _ = p.Model.Update(msg)
		return p, p.loadMessages()

	case messagesMsg:
		p.Model, _ = p.Model.Update(msg)
		return p, p.watchChannel()

	case messageMsg:
		p.Model, _ = p.Model.Update(msg)
		return p, waitForMessage(p.watcher)

	case tickMsg:
		return p, tea.Batch(p.load(), p.tick())

	case previewMsg, leavePreviewMsg, previewChannelMsg:
		p.Model, _ = p.Model.Update(msg)
		return p, nil

	case postedMsg, ackedMsg, mutedMsg, claimedMsg, releasedMsg, deletedMsg, leftMsg, deletedChannelMsg:
		p.Model, _ = p.Model.Update(msg)
		// The sidebars, the counts, and the roster all move on any of these, so everything is stale.
		return p, p.load()

	case errMsg:
		p.Model, _ = p.Model.Update(msg)
		return p, nil
	}
	return p, nil
}

// issue runs one request the model asked for. The whole store side of the view is here, so what the keys can
// reach is one list rather than a search.
func (p *program) issue(work pendingWork) tea.Cmd {
	channel, member, worktree := p.Model.channel, p.cfg.Member.Value, p.cfg.Worktree.Value
	return func() tea.Msg {
		switch work.action {
		case actionPost:
			message, err := p.agora.Post(p.ctx, store.PostRequest{
				Channel:  channel,
				Thread:   work.thread,
				Author:   member,
				Body:     work.body,
				Worktree: &worktree,
			})
			if err != nil {
				return errMsg{err: err}
			}
			return postedMsg{message: message}

		case actionAck:
			result, err := p.agora.Ack(p.ctx, store.AckRequest{
				Channel:  channel,
				Member:   member,
				Thread:   work.thread,
				Worktree: &worktree,
			})
			if err != nil {
				return errMsg{err: err}
			}
			// What it reached, rather than what was asked for: with no thread named it is every thread, and the
			// view has to say how many that was.
			return ackedMsg{thread: work.thread, threads: result.Threads}

		case actionMute:
			result, err := p.agora.Mute(p.ctx, store.MuteRequest{
				Channel:  channel,
				Member:   member,
				Thread:   work.thread,
				Unmute:   work.unmute,
				Worktree: &worktree,
			})
			if err != nil {
				return errMsg{err: err}
			}
			return mutedMsg{thread: work.thread, muted: result.Muted}

		case actionClaim:
			result, err := p.agora.Claim(p.ctx, store.ClaimRequest{
				Channel:  channel,
				Thread:   work.thread,
				Holder:   member,
				Note:     work.body,
				Paths:    work.paths,
				Worktree: &worktree,
			})
			if err != nil {
				return errMsg{err: err}
			}
			// A lost claim is a result, not an error: the holder and their note are the output.
			return claimedMsg{result: result}

		case actionPreviewLeave, actionLeave:
			// A member rather than a thread, and the same shape as a deletion: a dry run builds the question,
			// and answering it forces, because the question named the claims that would be handed back.
			preview := work.action == actionPreviewLeave
			result, err := p.agora.Leave(p.ctx, store.LeaveRequest{
				Channel: channel,
				Member:  work.member,
				Force:   work.force,
				DryRun:  preview,
			})
			if err != nil {
				return errMsg{err: err}
			}
			if preview {
				return leavePreviewMsg{
					channel: channel,
					member:  work.member,
					cursors: result.Cursors,
					claims:  result.Claims,
				}
			}
			return leftMsg{member: work.member}

		case actionPreviewDeleteChannel, actionDeleteChannel:
			// The channel comes from the work rather than from the selection, because this is the one action
			// that can move the selection: what it deletes is what it was selected on.
			preview := work.action == actionPreviewDeleteChannel
			result, err := p.agora.DeleteChannel(p.ctx, store.DeleteChannelRequest{
				Channel: work.channel,
				Force:   work.force,
				DryRun:  preview,
			})
			if err != nil {
				return errMsg{err: err}
			}
			if preview {
				holders := make([]string, 0, len(result.Claims))
				for _, claim := range result.Claims {
					holders = append(holders, claim.Holder)
				}
				return previewChannelMsg{
					channel:  work.channel,
					threads:  result.Threads,
					messages: result.Messages,
					members:  result.Members,
					claims:   holders,
				}
			}
			return deletedChannelMsg{channel: work.channel}

		case actionRelease:
			result, err := p.agora.Release(p.ctx, store.ReleaseRequest{
				Channel:  channel,
				Thread:   work.thread,
				Holder:   member,
				Force:    work.force,
				Worktree: &worktree,
			})
			if err != nil {
				return errMsg{err: err}
			}
			return releasedMsg{thread: work.thread, result: result}

		default:
			// A deletion, or the dry run the question is built from. Force on the real one is implied by the
			// question, which named the holder of any claim, so somebody who answered y was told whose work
			// it was.
			preview := work.action == actionPreviewDelete
			result, err := p.agora.Delete(p.ctx, store.DeleteRequest{
				Channel:  channel,
				Thread:   work.thread,
				Member:   member,
				Force:    work.force,
				DryRun:   preview,
				Worktree: &worktree,
			})
			if err != nil {
				return errMsg{err: err}
			}
			if preview {
				return previewMsg{
					channel:  channel,
					thread:   work.thread,
					messages: result.Messages,
					cursors:  result.Cursors,
					claim:    result.Claim,
				}
			}
			return deletedMsg{thread: work.thread}
		}
	}
}

// saveLayout writes the widths if they changed. A failure goes where a reload failure goes, since a drag that
// did not stick is worth one line and not worth interrupting anybody.
func (p *program) saveLayout() tea.Cmd {
	layout := p.Model.Layout()
	if layout == p.saved {
		return nil
	}
	p.saved = layout
	return func() tea.Msg {
		if err := p.layout.Save(layout); err != nil {
			return errMsg{err: err}
		}
		return nil
	}
}

func (p *program) View() string { return p.Model.View() }

func (p *program) tick() tea.Cmd {
	return tea.Tick(p.refresh, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// load reads the sidebars and the roster. One failure reports and leaves the last snapshot on screen,
// because a view that blanks on a transient error is less useful than one that says so in the corner.
func (p *program) load() tea.Cmd {
	channel, member := p.Model.channel, p.cfg.Member.Value
	return func() tea.Msg {
		channels, err := p.agora.Channels(p.ctx, member)
		if err != nil {
			return errMsg{err: err}
		}
		if channel == "" {
			return snapshotMsg{channels: channels}
		}
		threads, err := p.agora.Threads(p.ctx, store.ThreadsRequest{
			Channel: channel,
			Member:  member,
			// Observe, so opening a view does not add the viewer to the roster it is displaying.
			Observe: true,
		})
		if err != nil {
			return errMsg{err: err}
		}
		claims, err := p.agora.Claims(p.ctx, channel)
		if err != nil {
			return errMsg{err: err}
		}
		members, err := p.agora.Members(p.ctx, channel)
		if err != nil {
			return errMsg{err: err}
		}
		return snapshotMsg{channels: channels, threads: threads, claims: claims, members: members}
	}
}

// loadMessages fetches the selected thread. Separate from load, because the selection can settle onto a
// different thread than the one that was asked for and the answer has to say which thread it is for.
func (p *program) loadMessages() tea.Cmd {
	channel, thread := p.Model.Selection()
	if channel == "" || thread == "" {
		return nil
	}
	return func() tea.Msg {
		messages, err := p.agora.Messages(p.ctx, store.MessagesRequest{
			Channel: channel,
			Thread:  thread,
			Limit:   historyLimit,
		})
		if err != nil {
			return errMsg{err: err}
		}
		return messagesMsg{channel: channel, thread: thread, messages: messages}
	}
}

// watchChannel keeps one watch on the channel being shown, restarted when the selection moves to another.
// The watch is per channel rather than per thread because that is what the store offers, and the model drops
// what does not belong to the selected thread.
func (p *program) watchChannel() tea.Cmd {
	channel, _ := p.Model.Selection()
	if channel == "" || channel == p.watching {
		return nil
	}
	after := int64(0)
	if n := len(p.Model.messages); n > 0 {
		after = p.Model.messages[n-1].Seq
	}
	p.watching = channel
	p.watcher = p.agora.Watch(p.ctx, store.WatchOptions{Channel: channel, After: &after})
	return waitForMessage(p.watcher)
}

func waitForMessage(watcher *store.Watcher) tea.Cmd {
	if watcher == nil {
		return nil
	}
	return func() tea.Msg {
		message, ok := <-watcher.Messages()
		if !ok {
			// A closed channel is the context ending or the watch failing, and Err is the only thing that
			// tells them apart.
			if err := watcher.Err(); err != nil {
				return errMsg{err: err}
			}
			return nil
		}
		return messageMsg{message: message}
	}
}
