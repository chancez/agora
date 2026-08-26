package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chancez/agora/internal/config"
	"github.com/chancez/agora/internal/store"
	"github.com/google/go-cmp/cmp"
)

func injectEventJSON(t *testing.T, event, cwd, session string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"hook_event_name": event,
		"session_id":      session,
		"cwd":             cwd,
	})
	if err != nil {
		t.Fatalf("marshal the hook event: %v", err)
	}
	return string(raw)
}

// injected runs the hook and returns the context it produced, or "" when it said nothing.
func injected(t *testing.T, c *cli, event string, args ...string) (string, string) {
	t.Helper()
	got := c.stdin(injectEventJSON(t, event, c.dir, "test-session")).run(append([]string{"inject"}, args...)...)
	if got.code != 0 {
		t.Fatalf("agora inject exited %d, want 0 always\nstderr: %s", got.code, got.stderr)
	}
	if got.stdout == "" {
		return "", ""
	}
	out := decode[injectOutput](t, got).HookSpecificOutput
	return out.AdditionalContext, out.HookEventName
}

func TestInjectSaysNothingWithNoDatabase(t *testing.T) {
	c := newCLI(t)

	text, _ := injected(t, c, "SessionStart")
	if text != "" {
		t.Errorf("inject produced %q, want nothing when no channel exists", text)
	}
	// A hook on every session start and every prompt must not be what creates a database.
	if _, err := os.Stat(c.database()); err == nil {
		t.Error("agora inject created the database")
	}
}

func TestInjectOnSessionStartCarriesUnreadAndClaims(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("claim", "parser-panic",
		"--note", "root cause is in the token loop, do not patch the symptom",
		"--paths", "parser.go,internal/lex/**")
	alice.mustRun("post", "parser-panic", "empty input reaches the token loop with no bounds check")
	// A second thread that nobody claims, so the only thing that can name it is the index itself. With
	// only the claimed thread, dropping thread names from the index still passed: the claims block below
	// prints the same name.
	alice.as("carol").mustRun("post", "docs-rewrite", "renaming the config keys")

	text, event := injected(t, bob, "SessionStart")
	if event != "SessionStart" {
		t.Errorf("hookEventName = %q, want it echoed back as SessionStart", event)
	}
	// The message alone is not enough: the experiment behind this showed an agent needs to know what
	// to do about what it read, and the claim is what says so.
	for _, want := range []string{
		"bob",
		"empty input reaches the token loop",
		"parser-panic", "alice",
		"parser.go, internal/lex/**",
		"root cause is in the token loop",
		"docs-rewrite", "renaming the config keys",
		"agora read --thread",
		// mute rather than ack, because this sentence is where the briefing promises that what you dismiss
		// nobody will tell you again, and ack is undone by the next message in the thread.
		"agora mute",
		"agora skill",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("injected context is missing %q:\n%s", want, text)
		}
	}
}

func TestInjectNeverAdvancesTheCursor(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("post", "general", "parser.go panics on empty input")

	if text, _ := injected(t, bob, "SessionStart"); text == "" {
		t.Fatal("inject said nothing, want the unread message")
	}
	// A hook cannot know whether its output reached the model, so consuming the message would lose it
	// with nothing recording that it happened.
	after := decode[store.ReadResult](t, bob.mustRun("read"))
	if len(after.Messages) != 1 || after.Cursors[after.Messages[0].Thread] != 0 {
		t.Errorf("after inject, read = %d messages at cursors %v, want 1 with its thread at 0",
			len(after.Messages), after.Cursors)
	}
}

func TestInjectOnEachPromptStaysQuietUnlessSomethingIsUnread(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("claim", "parser-panic", "--note", "on it", "--paths", "parser.go")
	// bob has something of his own here, which is what stops the nudge in
	// TestInjectAsksAnAgentWithNothingHereToOpenAThread. Without this the turn below is not quiet for a
	// reason this test is about.
	//
	// A claim rather than a post, because a thread bob has posted in is one the briefing now routes his next
	// message to, and that line is the subject of TestInjectNamesTheThreadsYouAlreadyHaveOpen.
	bob.mustRun("claim", "lexer-bounds", "--note", "the lexer trims the last token")

	// A claim alone is a session-start briefing. Repeating it every turn would spend context on
	// something that has not changed since the last turn.
	if text, _ := injected(t, bob, "UserPromptSubmit"); text != "" {
		t.Errorf("inject produced %q on a prompt with nothing unread, want nothing", text)
	}
	if text, _ := injected(t, bob, "SessionStart"); !strings.Contains(text, "parser-panic") {
		t.Errorf("inject on SessionStart is missing the standing claim:\n%s", text)
	}

	alice.mustRun("post", "parser-panic", "found the root cause")
	text, event := injected(t, bob, "UserPromptSubmit")
	if event != "UserPromptSubmit" {
		t.Errorf("hookEventName = %q, want UserPromptSubmit", event)
	}
	// Once it is speaking anyway, the claim comes with it: that is the moment the ownership picture
	// matters.
	for _, want := range []string{"found the root cause", "parser-panic"} {
		if !strings.Contains(text, want) {
			t.Errorf("injected context is missing %q:\n%s", want, text)
		}
	}
}

func TestInjectIgnoresYourOwnMessagesAndClaims(t *testing.T) {
	alice := newCLI(t)
	alice.mustRun("claim", "parser-panic", "--note", "mine", "--paths", "parser.go")
	alice.mustRun("post", "parser-panic", "my own finding")

	// Neither is news to her, and a hook that reported them would teach her to ignore the hook.
	if text, _ := injected(t, alice, "SessionStart"); text != "" {
		t.Errorf("inject produced %q for a member's own work, want nothing", text)
	}
}

func TestInjectIsBounded(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	long := strings.Repeat("this finding goes on and on. ", 200)
	// Twenty unread threads, which is more than the index shows, plus one long enough to blow any budget
	// on its own.
	for i := range 20 {
		alice.mustRun("post", fmt.Sprintf("thread-%02d", i), long)
	}
	// Every dimension at once, because each is bounded separately and the total is bounded again:
	// many claims, long notes, and a glob list with no natural length.
	globs := make([]string, 0, 40)
	for i := range 40 {
		globs = append(globs, fmt.Sprintf("internal/some/deeply/nested/package%d/**/*.go", i))
	}
	for i := range 12 {
		alice.mustRun("claim", fmt.Sprintf("thread-%02d", i), "--note", long, "--paths", strings.Join(globs, ","))
	}

	text, _ := injected(t, bob, "SessionStart")
	// Hook output over 10000 characters is spilled to a file and replaced with a preview, which
	// silently stops delivering the messages the hook exists to deliver.
	if len(text) == 0 {
		t.Fatal("inject said nothing, want a bounded briefing")
	}
	if len(text) > 10000 {
		t.Errorf("injected context is %d characters, want it under the 10000 that gets spilled", len(text))
	}
	if !strings.Contains(text, "more threads with unread") {
		t.Errorf("a truncated index does not say how many threads it left out:\n%s", text)
	}
	// Which end gets cut, which is the whole of whether a truncated briefing is usable: the index is ordered by
	// when each thread last moved, so what a session misses is the stalest thread rather than the one that
	// arrived while it was working. A thread posted a second ago is the one most likely to be about now.
	//
	// The index alone, because the claims block below it names threads by a rule of its own, and this assertion
	// passed against a reversed sort when it read the whole briefing.
	index := text
	if at := strings.Index(index, "Work other agents have claimed"); at > 0 {
		index = index[:at]
	}
	if !strings.Contains(index, "thread-19") {
		t.Errorf("the newest unread thread is not in the index:\n%s", index)
	}
	if strings.Contains(index, "thread-00") {
		t.Errorf("the index kept the stalest thread and cut a newer one:\n%s", index)
	}
}

// TestInjectKeepsOneClaimFromCrowdingOutTheRest is what the per-claim bounds are for, as opposed to the
// overall one. A glob list has no natural length, so without a bound the first claim's paths can spend
// the whole budget and the claims after it are cut, which is worse than truncating them: an agent told
// about one claim believes it heard about all of them.
func TestInjectKeepsOneClaimFromCrowdingOutTheRest(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	globs := make([]string, 0, 200)
	for i := range 200 {
		globs = append(globs, fmt.Sprintf("internal/some/deeply/nested/package%03d/**/*.go", i))
	}
	threads := []string{"first-thread", "second-thread", "third-thread"}
	for _, thread := range threads {
		alice.mustRun("claim", thread, "--paths", strings.Join(globs, ","))
	}

	text, _ := injected(t, bob, "SessionStart")
	for _, thread := range threads {
		if !strings.Contains(text, thread) {
			t.Errorf("injected context does not mention %q, so one claim crowded out the others:\n%s", thread, text)
		}
	}
}

func TestInjectWithNoEventBriefsTheCurrentDirectory(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("post", "general", "parser.go panics on empty input")

	// Runnable by hand, which is how a person checks what a session would be told.
	got := bob.run("inject")
	if got.code != 0 {
		t.Fatalf("agora inject exited %d\nstderr: %s", got.code, got.stderr)
	}
	if !strings.Contains(got.stdout, "parser.go panics") {
		t.Errorf("inject with no event on stdin printed:\n%s", got.stdout)
	}
}

func TestInjectLetsTheSessionStartWhenItCannotDecide(t *testing.T) {
	c := newCLI(t)
	c.as("bob").mustRun("post", "general", "something")

	got := c.stdin("{not json").run("inject")
	// A hook that fails loudly on every turn gets removed, and then nothing is delivered at all.
	if got.code != 0 || got.stdout != "" {
		t.Errorf("exit %d, stdout %q, want 0 and nothing", got.code, got.stdout)
	}
	if got.stderr == "" {
		t.Error("stderr is empty, want the failure findable")
	}
}

// TestInjectAsksAnAgentWithNothingHereToOpenAThread is the nudge for the failure that was watched happening: a
// session read the one thread waiting, judged it correctly, then started a feature of its own and posted
// nothing. It fires on a prompt, where the work has just been named, and only while this member has taken part
// in nothing.
func TestInjectAsksAnAgentWithNothingHereToOpenAThread(t *testing.T) {
	const want = "Nothing in this channel is from you yet"

	t.Run("on a prompt in a channel with nothing to say", func(t *testing.T) {
		// The case a briefing cannot cover. Nobody has posted, so there is nothing unread and no claim, and
		// without this the hook is silent for the whole session. join rather than config because inject says
		// nothing at all until a database exists, since a hook on every session start must not be what
		// creates one.
		alice := newCLI(t)
		alice.mustRun("join")

		text, _ := injected(t, alice, "UserPromptSubmit")
		if !strings.Contains(text, want) {
			t.Errorf("inject did not ask an agent with nothing here to open a thread:\n%s", text)
		}
	})

	t.Run("once it has posted", func(t *testing.T) {
		alice := newCLI(t)
		alice.mustRun("post", "parser-panic", "the token loop indexes without a bounds check")

		// What replaces it is the routing line, not silence: a member with a thread of its own has somewhere
		// for its next message to go, which is a different question from whether it has said anything at all.
		text, _ := injected(t, alice, "UserPromptSubmit")
		if strings.Contains(text, want) {
			t.Errorf("inject asked again after alice posted:\n%s", text)
		}
		if !strings.Contains(text, "parser-panic") {
			t.Errorf("inject stopped naming the thread alice has open:\n%s", text)
		}
	})

	t.Run("once it holds a claim", func(t *testing.T) {
		// A claim with no message in its thread is still work anybody can see, which is what the nudge is
		// for.
		alice := newCLI(t)
		alice.mustRun("claim", "parser-panic", "--note", "root cause is in the token loop")

		if text, _ := injected(t, alice, "UserPromptSubmit"); text != "" {
			t.Errorf("inject asked a claim holder to announce itself:\n%s", text)
		}
	})

	t.Run("not on a session start or a tool call", func(t *testing.T) {
		// SessionStart arrives before the agent has been given anything to announce, and PostToolUse would
		// repeat this beside every tool result of the turn.
		alice := newCLI(t)
		alice.mustRun("join")

		for _, event := range []string{"SessionStart", "PostToolUse"} {
			if text, _ := injected(t, alice, event); text != "" {
				t.Errorf("inject spoke on %s:\n%s", event, text)
			}
		}
	})

	t.Run("beside what was unread", func(t *testing.T) {
		alice := newCLI(t)
		bob := alice.as("bob")
		alice.mustRun("post", "docs-rewrite", "renaming the config keys")

		text, _ := injected(t, bob, "UserPromptSubmit")
		for _, want := range []string{want, "docs-rewrite", "agora read --thread"} {
			if !strings.Contains(text, want) {
				t.Errorf("injected context is missing %q:\n%s", want, text)
			}
		}
	})

	t.Run("without dragging standing claims into every prompt", func(t *testing.T) {
		// The claims are a session-start briefing, and this nudge is not a reason to send them again: it
		// repeats until the member takes part, so the claim list would repeat with it.
		alice := newCLI(t)
		bob := alice.as("bob")
		alice.mustRun("claim", "parser-panic", "--note", "on it", "--paths", "parser.go")

		text, _ := injected(t, bob, "UserPromptSubmit")
		if !strings.Contains(text, want) {
			t.Errorf("injected context is missing %q:\n%s", want, text)
		}
		if strings.Contains(text, "parser-panic") {
			t.Errorf("the nudge repeated the standing claims:\n%s", text)
		}
	})
}

// TestInjectSpendsOneLineOnMutedThreads is what keeps a mute from being a deletion. A muted thread is out of
// the index, so the only thing standing between it and being forgotten is this line, and the line has to stay
// one line or it is not a mute.
func TestInjectSpendsOneLineOnMutedThreads(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")
	alice.mustRun("post", "docs-rewrite", "renaming the config keys")
	alice.mustRun("post", "parser-panic", "the token loop indexes without a bounds check")
	bob.mustRun("mute", "docs-rewrite")
	alice.mustRun("post", "docs-rewrite", "and the defaults")

	text, _ := injected(t, bob, "SessionStart")
	// Named, counted, and with a way back, but no body: the body is what the mute was about.
	for _, want := range []string{"muted, with new messages", "docs-rewrite (2)", "agora unmute"} {
		if !strings.Contains(text, want) {
			t.Errorf("injected context is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "and the defaults") {
		t.Errorf("the briefing carried a muted thread's messages:\n%s", text)
	}
	// The thread that is not muted is still an index entry, body and all.
	if !strings.Contains(text, "the token loop indexes without a bounds check") {
		t.Errorf("the index lost the unmuted thread:\n%s", text)
	}

	// With nothing new in it, a muted thread is not worth a line either.
	bob.mustRun("read", "--thread", "docs-rewrite", "--advance")
	quiet, _ := injected(t, bob, "SessionStart")
	if strings.Contains(quiet, "muted, with new messages") {
		t.Errorf("a muted thread with nothing new still got a line:\n%s", quiet)
	}
}

// TestInjectBriefsTheSessionTheEventNames is what a second harness needs. Claude Code puts its session id in a
// hook's environment; Codex promises only to put it in the event, so a hook that read the environment would
// brief a member the agent is not. The symptom is not a wrong name in a roster: it is a session that is told
// about its own messages, never told about anybody else's, and cannot release the claims its hooks are shown.
//
// Two callers, one session: the agent's own commands, which see the harness's thread id, and the hook, which
// sees only what the wiring told it and the event it was handed.
func TestInjectBriefsTheSessionTheEventNames(t *testing.T) {
	const id = "01a03b15-06a4-7aa3-a7e5-46287dec58e9"
	alice := newCLI(t)
	agent := alice.withEnv(config.EnvMember, "").withEnv(config.EnvCodexThread, id)
	hook := alice.withEnv(config.EnvMember, "").withEnv(config.EnvAgent, "codex")

	alice.mustRun("post", "parser-panic", "empty input reaches the token loop")
	// Read as the agent, so anything the hook reports afterwards is a briefing for somebody else.
	agent.mustRun("read", "--advance")

	got := hook.stdin(injectEventJSON(t, "SessionStart", hook.dir, id)).run("inject")
	if got.code != 0 {
		t.Fatalf("agora inject exited %d\nstderr: %s", got.code, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("inject briefed a member the session is not:\n%s", got.stdout)
	}

	// And the roster has one member for the session rather than two, which is the state that goes wrong.
	members := decode[[]store.Member](t, alice.mustRun("members"))
	var names []string
	for _, m := range members {
		names = append(names, m.Name)
	}
	if diff := cmp.Diff([]string{"alice", "codex-e7784ad5"}, names); diff != "" {
		t.Errorf("the roster, -want +got:\n%s", diff)
	}
}

// TestIsTerminal is what stops inject and guard waiting for a hook event nobody is going to send. It is
// tested directly because the behaviour it drives is not observable without a real tty: an integration test
// pointed at /dev/null passes whether or not the check exists, since /dev/null reads as EOF straight away.
// That version of this test was written first and proved nothing.
func TestIsTerminal(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devnull.Close()
	if !isTerminal(devnull) {
		t.Errorf("isTerminal(%s) = false, want true for a character device", os.DevNull)
	}

	// A hook's stdin is a pipe, and it must be read rather than skipped.
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()
	if isTerminal(reader) {
		t.Error("isTerminal(pipe) = true, want false")
	}
	if isTerminal(strings.NewReader("{}")) {
		t.Error("isTerminal(a plain reader) = true, want false")
	}
}

// TestInjectAsksForADescriptionOnce is the nudge for a field nothing else can fill in. It has to be quiet in
// the two cases where it would be noise: a member who has said what it is doing, and a member alone in a
// channel, where a description is a note to nobody.
func TestInjectAsksForADescriptionOnce(t *testing.T) {
	const want = "agora join --description"

	t.Run("with somebody else here and nothing said", func(t *testing.T) {
		alice := newCLI(t)
		bob := alice.as("bob")
		alice.mustRun("post", "parser-panic", "the token loop indexes without a bounds check")

		got := bob.mustRun("inject", "--text").stdout
		if !strings.Contains(got, want) {
			t.Errorf("inject did not ask bob to say what he is doing:\n%s", got)
		}
	})

	t.Run("once it has been said", func(t *testing.T) {
		alice := newCLI(t)
		bob := alice.as("bob")
		alice.mustRun("post", "parser-panic", "the token loop indexes without a bounds check")
		bob.mustRun("join", "--description", "reviewing the lexer")

		got := bob.mustRun("inject", "--text").stdout
		if strings.Contains(got, want) {
			t.Errorf("inject asked again after bob described himself:\n%s", got)
		}
		// And what he was actually told is still there.
		if !strings.Contains(got, "parser-panic") {
			t.Errorf("the index went missing:\n%s", got)
		}
	})

	t.Run("alone in the channel", func(t *testing.T) {
		// Nothing unread either, so this also covers the property that a quiet turn stays quiet: the roster is
		// only read once there is something to say.
		c := newCLI(t)
		c.mustRun("join")

		if got := c.mustRun("inject", "--text").stdout; got != "" {
			t.Errorf("inject spoke to an agent alone in a channel:\n%s", got)
		}
	})
}

// TestInjectNamesTheThreadsYouAlreadyHaveOpen is where the thread-per-turn fix lives, and it is in the briefing
// rather than in `agora post` because that is what the measurement said: a signal in a command's output arrives
// after the agent has decided where to write. scripts/thread-experiment.sh has the numbers.
//
// Unread cannot carry this. Your own messages are read the moment you write them, so a thread only you have
// spoken in is invisible to everything else here.
func TestInjectNamesTheThreadsYouAlreadyHaveOpen(t *testing.T) {
	t.Run("on a prompt, with the count and where the next message goes", func(t *testing.T) {
		alice := newCLI(t)
		alice.mustRun("post", "parser-panic", "the token loop indexes without a bounds check")
		alice.mustRun("post", "parser-panic", "and the lexer has it too")

		text, _ := injected(t, alice, "UserPromptSubmit")
		want := "You have parser-panic open (2 messages, last moved 0s ago). Follow-up on work you already" +
			" announced goes in the thread that announced it rather than in a new one, and `agora threads" +
			" --mine` lists them. A piece of work that stands on its own still gets its own thread, named in" +
			" the one it came out of so a reader of either finds the other."
		if !strings.Contains(text, want) {
			t.Errorf("the briefing does not route this turn's message:\n%s", text)
		}
	})

	t.Run("naming the newest of several", func(t *testing.T) {
		// Newest first, because that is the one the current turn is most likely continuing, and one name plus a
		// count rather than a list: this is in context on every prompt of a working session.
		alice := newCLI(t)
		alice.mustRun("post", "parser-panic", "starting on the parser")
		alice.mustRun("post", "tui-widths", "starting on the roster width")

		text, _ := injected(t, alice, "UserPromptSubmit")
		if !strings.Contains(text, "You have 2 threads of your own open, most recently tui-widths (1 message,") {
			t.Errorf("the briefing does not name the newest of several:\n%s", text)
		}
		if strings.Contains(text, "parser-panic") {
			t.Errorf("the briefing listed every thread rather than the newest and a count:\n%s", text)
		}
	})

	t.Run("not for somebody else's thread", func(t *testing.T) {
		// The question is where *your* next message goes. A thread you have never spoken in is not one you can
		// be continuing, and it reaches you as unread anyway.
		alice := newCLI(t)
		alice.as("bob").mustRun("post", "docs-rewrite", "renaming the config keys")

		text, _ := injected(t, alice, "UserPromptSubmit")
		if strings.Contains(text, "Follow-up on work you already announced") {
			t.Errorf("the briefing routed alice's turn into bob's thread:\n%s", text)
		}
	})

	t.Run("not for a thread that has gone cold", func(t *testing.T) {
		// Follow-up work arrives close in time to the announcement, so a thread nobody has touched for longer
		// than the window is not what this turn continues, and a line in every prompt of every session is a
		// cost this file has already refused once.
		//
		// Written through the store with a clock of its own, because the CLI deliberately has no clock to
		// inject: a flag only tests would use is a flag that can be set in production.
		alice := newCLI(t)
		old := time.Now().Add(-2 * injectOpenWindow)
		s, err := store.Open(alice.database(), store.WithClock(func() time.Time { return old }))
		if err != nil {
			t.Fatalf("open the store: %v", err)
		}
		if _, err := s.Post(t.Context(), store.PostRequest{
			Channel: "devtest", Author: "alice", Thread: "parser-panic", Body: "started this two hours ago",
		}); err != nil {
			t.Fatalf("post an old message: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("close the store: %v", err)
		}

		text, _ := injected(t, alice, "UserPromptSubmit")
		if strings.Contains(text, "Follow-up on work you already announced") {
			t.Errorf("the briefing routed this turn into a thread that last moved two hours ago:\n%s", text)
		}
	})

	t.Run("not on a session start or a tool call", func(t *testing.T) {
		// The prompt is where the work is named. A session start has nothing to route yet, and PostToolUse
		// would repeat this beside every tool result of the turn.
		alice := newCLI(t)
		alice.mustRun("post", "parser-panic", "the token loop indexes without a bounds check")

		for _, event := range []string{"SessionStart", "PostToolUse"} {
			if text, _ := injected(t, alice, event); strings.Contains(text, "Follow-up on work") {
				t.Errorf("the briefing routed a %s:\n%s", event, text)
			}
		}
	})
}
