package store

import "time"

// Channel is one project's log, keyed on the repository root so every worktree of it shares a channel. The
// derivation is git, so it happens above this package and arrives as an opaque string.
//
// A channel holds threads, and a thread is both one discussion and the unit of ownership: the work and the talk
// about it share a name.
type Channel struct {
	ID        int64     `json:"id"`
	Key       string    `json:"key"`
	Name      string    `json:"name,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// Threads and UnreadThreads are filled in by Channels, because a list of projects is only useful with
	// the numbers that say which one wants attention. Unread threads rather than unread messages, since a
	// thread is one decision and that is the unit a reader picks a project by.
	Threads       int64 `json:"threads"`
	UnreadThreads int64 `json:"unread_threads"`
}

// Message is one post.
type Message struct {
	// Seq is its place in its channel, from 1: the ordering every participant agrees on, what cursors point
	// at, and what watch --after takes. Per channel rather than per database, because an agent works one
	// repository and a number that skipped for another repository's traffic meant nothing to it.
	Seq int64 `json:"seq"`
	// Number is its place in its own thread, from 1, and it is what a reader is shown. Deliberately not in the
	// JSON: Seq and Number are both plausible things to hand to --after and only one of them is right, so the
	// contract carries one number and the display carries the other.
	Number  int64  `json:"-"`
	Channel string `json:"channel"`
	// Thread is required. An untitled message is one nobody can triage, and the whole point of a thread
	// is that a reader can decide whether it matters without reading everything in it.
	Thread    string    `json:"thread"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// Thread is one discussion as one member sees it. It is what an agent triages: enough to decide whether to
// read the thread or dismiss it, without loading either.
type Thread struct {
	Channel string `json:"channel"`
	Name    string `json:"name"`
	// Unread counts messages from other members that this member has not read.
	Unread int64 `json:"unread"`
	// First is the oldest unread message, nil when nothing is unread. The oldest rather than the newest,
	// because it is the one that says what the thread is about, which is what deciding to read or dismiss
	// turns on.
	First *Message `json:"first,omitempty"`
	// Messages is the thread's total length, and LastAt is when it last moved.
	Messages int64     `json:"messages"`
	LastAt   time.Time `json:"last_at"`
	// Claim is who owns this thread's work, empty when nobody does.
	Claim string `json:"claim,omitempty"`
	// Muted is set when this member has dismissed the thread. Unread is still counted for a muted thread,
	// because "muted, 4 new" is what keeps a mute from becoming a thread nobody can find again.
	Muted bool `json:"muted,omitempty"`
}

// Member is one participant's place in a channel.
type Member struct {
	Channel string `json:"channel"`
	Name    string `json:"name"`
	// Unread is how many messages from other members this one has not read, across every thread, and
	// UnreadThreads is how many threads those are spread over. The thread count is the headline: it is how
	// many decisions are waiting, where the message count is how much reading.
	Unread        int64 `json:"unread"`
	UnreadThreads int64 `json:"unread_threads"`
	// Posts is how many messages this member has written here. Zero says it has taken part in nothing, which
	// is what the nudge to open a thread turns on: work nobody can see is the failure agora exists to stop.
	Posts int64 `json:"posts"`
	// Description is what this member says it is doing, in one line, because a name is a session id and a
	// session id says nothing. Set by the member itself: nothing else knows.
	Description string `json:"description,omitempty"`
	// Notify is the optional nudge command, run when this member has unread. It records how to reach a
	// member; nothing assumes reaching them worked.
	Notify   string    `json:"notify,omitempty"`
	Worktree string    `json:"worktree,omitempty"`
	JoinedAt time.Time `json:"joined_at"`
	SeenAt   time.Time `json:"seen_at"`
}

// Claim is ownership of a thread's work, held by one member at a time.
type Claim struct {
	Channel string `json:"channel"`
	Thread  string `json:"thread"`
	Holder  string `json:"holder"`
	Note    string `json:"note,omitempty"`
	// Paths is an optional glob list, and the only part of a claim anything can act on mechanically. It
	// says where the work lives rather than locking those files: two agents editing one file for unrelated
	// reasons is ordinary, and the note is what says whether the work is the same.
	Paths     []string  `json:"paths,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// JoinRequest adds a member to a channel, or updates one already there. Its optional fields are pointers to
// separate "leave whatever is recorded alone" from "set it to empty": a second join without --notify must not
// wipe the nudge command a first join configured.
type JoinRequest struct {
	Channel string
	Member  string
	// Description is one line about the work this member is doing, for a roster where every other name is a
	// session id.
	Description *string
	Notify      *string
	Worktree    *string
}

// LeaveRequest removes a member from a channel's roster, so a session that ended stops reading as one that
// might come back. It leaves the record alone: a message carries its author as a string.
type LeaveRequest struct {
	Channel string
	Member  string
	// Force releases the claims this member holds. Without it, holding one is refused: a claim whose holder is
	// not in the roster leaves nobody to ask whether the work was finished. Cursors are never removed, see
	// Leave.
	Force bool
	// DryRun reports what leaving would take and changes nothing.
	DryRun bool
}

// LeaveResult reports what went, or what would have.
type LeaveResult struct {
	Channel string `json:"channel"`
	Member  string `json:"member"`
	// Left is false for a dry run, for a member that was not in the roster, and for one holding a claim
	// without Force.
	Left bool `json:"left"`
	// Cursors is how many threads this member has read, which leaving keeps: the same name coming back picks
	// up where it left off rather than re-reading the channel.
	Cursors int64 `json:"cursors"`
	// Claims is what this member holds, whether they were released or the refusal is about them.
	Claims []string `json:"claims"`
}

// PruneRequest takes every member last heard from before Before off the roster, which is the sweep for sessions
// that ended without saying so.
type PruneRequest struct {
	Channel string
	// Member is who is asking. It is never pruned: running a sweep is not evidence of being gone, and this is
	// the one member the cutoff cannot be right about.
	Member string
	// Before is the cutoff. A member whose clock is older than it has not used agora since, which is a hint
	// rather than a fact: an agent can work for hours without posting.
	Before   time.Time
	DryRun   bool
	Worktree *string
}

// PruneResult is who went and who was left alone.
type PruneResult struct {
	Channel string `json:"channel"`
	// Pruned is who went, in name order, or who would have for a dry run.
	Pruned []string `json:"pruned"`
	// Held is the stale members that stayed because they hold a claim, and the threads they hold. A sweep must
	// not release one: it is the only part of a wrong removal that cannot be taken back.
	Held map[string][]string `json:"held,omitempty"`
}

// PostRequest writes one message to one thread.
type PostRequest struct {
	Channel string
	Thread  string
	Author  string
	Body    string
	// Worktree is the checkout this member is acting from, recorded so a reader can tell which of a
	// repository's worktrees a member is in. Nil leaves whatever is recorded alone.
	Worktree *string
}

// ThreadsRequest lists a channel's threads as one member sees them.
type ThreadsRequest struct {
	Channel string
	Member  string
	// Filter is which view of the index this is. Not two booleans, because "has unread" and "is muted" have
	// to be asked together and a caller that asks only the first gets a briefing naming threads it was told
	// to stop mentioning.
	Filter ThreadFilter
	// Author narrows to threads this member has posted in, which is how a member asks what it already has
	// open. Orthogonal to Filter rather than another value of it: "mine, with unread" is a question, and an
	// enum could not ask it. Empty means every thread, whoever wrote it.
	Author string
	// Limit keeps the most recently active, 0 meaning all.
	Limit int
	// Observe asks for the index without recording the member as present. A view has to compute unread
	// for somebody to show the badges, and a view that added itself to the roster it displays would be
	// changing what every agent in the channel sees just by being open.
	Observe  bool
	Worktree *string
}

// ThreadFilter is one of the three questions anything asks of a channel's index.
type ThreadFilter int

const (
	// AllThreads is the whole index, which is what browsing wants. Each thread says whether it is muted.
	AllThreads ThreadFilter = iota
	// UnreadThreads is this member's inbox: something unread, in a thread it has not muted. What a
	// notification wants, and the only filter a nudge should ever be built on.
	UnreadThreads
	// MutedThreads is what this member has dismissed, with the unread each has piled up since. The one line
	// a briefing spends on them comes from here, and it is what stops a mute becoming a thread nobody
	// remembers exists.
	MutedThreads
)

// ReadRequest returns a member's unread messages, in one thread or across all of them.
//
// Advance is off by default, which is the deliberate contract: the common caller is a hook, and hook output
// that never reaches the model would otherwise consume messages nobody read. Displaying and acknowledging
// are two decisions, so they are two calls.
type ReadRequest struct {
	Channel string
	Member  string
	// Thread narrows to one, which is what reading a thread you decided you care about looks like. Empty
	// reads every thread.
	Thread  string
	Advance bool
	// Limit caps how many messages come back, 0 meaning no cap. A hook needs this because hook output over
	// 10000 characters is spilled to a file and replaced with a preview, which silently stops delivering
	// the messages it was added to deliver.
	Limit    int
	Worktree *string
}

// ReadResult is what a member had waiting.
type ReadResult struct {
	Channel  string    `json:"channel"`
	Member   string    `json:"member"`
	Thread   string    `json:"thread,omitempty"`
	Messages []Message `json:"messages"`
	// Remaining counts unread left behind by Limit. Nonzero means this read was truncated, so the caller
	// can say so rather than implying there is nothing left.
	Remaining int `json:"remaining"`
	// Cursors is where each thread's cursor stands after the call, so a caller can tell a peek from an
	// advance from the result alone.
	Cursors map[string]int64 `json:"cursors"`
}

// AckRequest marks threads read without reading them, which is the other half of triage: deciding a thread
// is not yours is a decision, and it needs somewhere to be recorded.
type AckRequest struct {
	Channel string
	Member  string
	// Thread empty acknowledges every thread, which is "none of this is mine".
	Thread   string
	Worktree *string
}

// AckResult reports which threads were acknowledged and where their cursors ended up.
type AckResult struct {
	Channel string           `json:"channel"`
	Member  string           `json:"member"`
	Threads []string         `json:"threads"`
	Cursors map[string]int64 `json:"cursors"`
}

// MuteRequest stops a thread nudging this member, or starts it again with Unmute. The sticky half of triage:
// Ack says "read to here" and the next message undoes it.
type MuteRequest struct {
	Channel string
	Member  string
	// Thread is required unless All is set, which takes every thread in the channel. All is how an agent
	// says "only new threads and my own work from here", which is the posture a busy channel drives it to.
	Thread   string
	All      bool
	Unmute   bool
	Worktree *string
}

// MuteResult reports what changed.
type MuteResult struct {
	Channel string `json:"channel"`
	Member  string `json:"member"`
	// Muted is the state asked for, so a caller can tell the two directions apart from the result alone.
	Muted bool `json:"muted"`
	// Threads is what this call changed, so an empty one means every thread named was already that way.
	Threads []string `json:"threads"`
}

// NoticeRequest asks which of these claims a member has not been told about, and records that it now has. The
// guard is the caller: a notice repeated on every edit under one claim is the noise that got the guard turned
// off, so what it has already said is state like any other.
type NoticeRequest struct {
	Channel string
	Member  string
	// Claims is everything the caller would say, so one call covers an edit that several claims cover.
	Claims []Claim
	// DryRun reports what would be said without recording it, for a caller that is only looking.
	DryRun bool
}

// NoticeResult is what is worth saying.
type NoticeResult struct {
	Channel string `json:"channel"`
	Member  string `json:"member"`
	// New is the claims this member has not heard about, in the order they were given. A claim reappears here
	// when its holder changes or when it was released and taken again, and never otherwise.
	New []Claim `json:"new"`
}

// DoorbellRequest asks what should wake this member, and records that it was woken.
//
// The one thing agora has that does not wait for the agent to act: everything else is delivered when a
// session starts, when a prompt arrives, or when an edit is attempted, and a session parked at a prompt
// does none of those.
type DoorbellRequest struct {
	Channel string
	Member  string
	// Waiter identifies a process that intends to keep waiting, so a doorbell started later takes the wait
	// over from one left behind by an earlier turn. It must sort chronologically, since that ordering is what
	// decides which of two is the leftover: a start timestamp followed by a pid does both jobs.
	//
	// Empty for a caller that is only asking what is waiting now, which never takes the wait from anybody.
	Waiter string
	// Limit caps how many messages come back, 0 meaning no cap. A wake carries them into a model's context,
	// where the 10000 character spill applies as it does to any hook output.
	Limit int
	// DryRun answers without recording the ring or taking the wait, so looking at what would wake a member
	// does not silence the wake itself.
	DryRun bool
}

// DoorbellResult is what is worth waking a member for.
type DoorbellResult struct {
	Channel string `json:"channel"`
	Member  string `json:"member"`
	// Messages are the unread messages addressed to this member since it was last woken, oldest first.
	// Empty is the ordinary answer, and it means do not wake anybody.
	Messages []Message `json:"messages"`
	// Remaining counts messages left behind by Limit, so a bounded wake can say it was bounded.
	Remaining int `json:"remaining"`
	// Rang is how far this member has been woken after the call, as a seq in the channel. Reported either
	// way, so a dry run can be told from a ring by the result alone.
	Rang int64 `json:"rang"`
	// Waiter is who holds the wait now. A caller that passed one and reads a different one back has been
	// taken over by a doorbell from a later turn, and its move is to stop rather than to keep polling.
	Waiter string `json:"waiter,omitempty"`
}

// ClaimRequest takes ownership of a thread's work.
type ClaimRequest struct {
	Channel  string
	Thread   string
	Holder   string
	Note     string
	Paths    []string
	Worktree *string
}

// ClaimResult reports the outcome and, either way, the claim that now stands. Losing is a result rather
// than an error: the whole point of a losing claim is the holder and note it reports, because that output
// is what stops duplicate work.
type ClaimResult struct {
	Granted bool  `json:"granted"`
	Claim   Claim `json:"claim"`
}

// ReleaseRequest gives up a claim.
//
// Holder is required and checked, because a claim held by a member that looks idle may belong to an agent
// waiting on its user. Force releases someone else's claim anyway, which exists for a member that is
// genuinely gone and is never the default.
type ReleaseRequest struct {
	Channel  string
	Thread   string
	Holder   string
	Force    bool
	Worktree *string
}

// ReleaseResult reports whether the claim went away and what it was. A claim that was not held at all is
// Released false with a zero Claim; one held by someone else is Released false with theirs.
type ReleaseResult struct {
	Released bool  `json:"released"`
	Claim    Claim `json:"claim"`
}

// MessagesRequest reads a channel's history directly, ignoring cursors.
type MessagesRequest struct {
	Channel string
	Thread  string
	// After returns only messages with a higher id, 0 meaning from the beginning.
	After int64
	// Limit caps how many messages come back, 0 meaning no cap. The newest are kept, since a truncated
	// history is more useful from the recent end.
	Limit int
}

// DeleteRequest removes a thread: its messages, its claim, every cursor on it. The one operation that takes
// something out of the record, for when a record is wrong. DryRun reports what would go, which is what a
// confirmation is built on.
type DeleteRequest struct {
	Channel string
	Thread  string
	// Member is who is asking, checked against the claim: deleting the thread somebody else is working in
	// takes their note and their evidence with it. Force does it anyway.
	Member   string
	Force    bool
	DryRun   bool
	Worktree *string
}

// DeleteChannelRequest removes a channel and everything in it.
//
// There is no Member. Every other write records who did it in the channel it happened in, and the channel a
// deletion would be recorded in is the one that just went.
type DeleteChannelRequest struct {
	Channel string
	// Force deletes a channel that has been used. Without it only messages and claims stop it, because those
	// are the record and somebody's ownership of work, and the case this exists for has neither: a channel
	// created by a command run once in a repository and never used again.
	Force bool
	// DryRun reports what would go, which is what a confirmation is built on.
	DryRun bool
}

// DeleteChannelResult is what went, or what would go.
type DeleteChannelResult struct {
	Channel string `json:"channel"`
	// Deleted is false for a dry run, for a key no channel has, and for a channel with messages or claims in
	// it without Force.
	Deleted bool `json:"deleted"`
	// Existed tells "already gone" from "refused", which the counts cannot: both report zero of everything.
	Existed  bool  `json:"existed"`
	Threads  int64 `json:"threads"`
	Messages int64 `json:"messages"`
	Members  int64 `json:"members"`
	Cursors  int64 `json:"cursors"`
	// Claims is every claim that would go, whole rather than counted, so a refusal can say who holds what and
	// what their note is. That output is what decides whether to force it.
	Claims []Claim `json:"claims,omitempty"`
}

// DeleteResult is what went, or what would go.
type DeleteResult struct {
	Channel string `json:"channel"`
	Thread  string `json:"thread"`
	// Deleted is false for a dry run, and false when another member's claim stopped it.
	Deleted  bool  `json:"deleted"`
	Messages int64 `json:"messages"`
	Cursors  int64 `json:"cursors"`
	// Claim is the holder whose claim went with it, or who stopped it.
	Claim string `json:"claim,omitempty"`
}
