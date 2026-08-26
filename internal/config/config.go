// Package config resolves where agora's state lives, which channel a command means, and who is asking, and
// reports where every answer came from.
//
// The provenance is the point: a test that writes to the real database posts to a channel real agents act on,
// so isolation has to be checkable rather than hoped for.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Environment variables agora reads. Never require one that only a single tool sets: identity falls
// through to something derived, so agora works for one agent in a plain terminal on the first run.
const (
	EnvDatabase = "AGORA_DB"
	EnvChannel  = "AGORA_CHANNEL"
	EnvMember   = "AGORA_MEMBER"
	// EnvXDGData is where the database lives by default.
	EnvXDGData = "XDG_DATA_HOME"
	// EnvClaudeSession is Claude Code's session id, set in the agent's shell and in its hooks, where it
	// matches the session_id of the hook event. Reading it rather than CLAUDECODE is deliberate:
	// CLAUDECODE is also set in an IDE's integrated terminal, where the person is typing, and the
	// session id is not.
	EnvClaudeSession = "CLAUDE_CODE_SESSION_ID"
	// EnvCodexThread is Codex's thread id, which it injects into the environment of every shell command
	// the model runs, and which is the same id a Codex hook event carries as its session_id.
	EnvCodexThread = "CODEX_THREAD_ID"
	// EnvAgent names the harness, and only decides the prefix on a name derived from a session id. It is
	// for a hook, which is handed a session_id by an event and need not have the harness's own session
	// variable in its environment: Claude Code sets one there and Codex promises nothing. A hook that
	// resolves a different name than the agent's own commands do splits one session into two members,
	// which is worse than either name, so the Codex wiring in docs/setup.md sets this.
	EnvAgent = "AGORA_AGENT"
	// EnvUser names the person at the keyboard.
	EnvUser = "USER"
)

// The harnesses agora recognises, used to prefix a name derived from a session id so a roster says at a
// glance which members are agents, which harness each is, and which is the person.
const (
	claudeAgent = "claude"
	codexAgent  = "codex"
)

// Sources a value can come from, as reported by agora config.
const (
	SourceFlag         = "flag"
	SourceXDG          = "$" + EnvXDGData
	SourceDefault      = "default"
	SourceRepository   = "repository"
	SourceWorkingDir   = "working directory"
	SourceWorktreeName = "worktree name"
	SourceAgentSession = "$" + EnvClaudeSession
	SourceCodexThread  = "$" + EnvCodexThread
	SourceUserName     = "$" + EnvUser
	SourceHookEvent    = "hook event"
)

// Value is a resolved setting and where it came from.
type Value struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

// Config is everything a command needs before it touches the store.
type Config struct {
	// Database is the sqlite file. Anything reporting the real $XDG_DATA_HOME path is not isolated.
	Database Value `json:"database"`
	// Channel defaults to the repository, so worktrees of one repo share a channel with no
	// configuration.
	Channel Value `json:"channel"`
	// Member is who this invocation is.
	Member Value `json:"member"`
	// Worktree is the checkout this invocation is in, recorded so a reader can tell which of a
	// repository's worktrees a member is working in.
	Worktree Value `json:"worktree"`
	// Layout is the file the view keeps its pane widths in. Resolved here rather than where it is read, so it
	// honours the same environment every other path does.
	Layout Value `json:"layout"`
}

// Flags are the command line overrides, empty when not given.
type Flags struct {
	Database string
	Channel  string
	Member   string
	// SessionID is an agent's session as named by a hook event, used for identity when a command has one. It
	// ranks below --as and $AGORA_MEMBER and above the environment's own session id: the event names the
	// session it is about, where the variable names whichever session the process was spawned from, and only
	// one of those is still true for an event about a session ending.
	SessionID string
}

// Resolver resolves a Config. The zero value reads the real process environment, the real working
// directory, and the real git.
type Resolver struct {
	// LookupEnv defaults to os.LookupEnv.
	LookupEnv func(string) (string, bool)
	// Dir is the working directory, defaulting to os.Getwd.
	Dir string
	// Locate finds the repository a directory belongs to, defaulting to LocateRepo.
	Locate func(dir string) (Repo, bool, error)
}

// EmptyEnvError reports a variable set to the empty string, which reads as unset and falls through to the
// default: `AGORA_DB=` looks like isolation and is not. In cm a whole test suite read the developer's real
// config for weeks that way.
type EmptyEnvError struct {
	Name string
}

func (e *EmptyEnvError) Error() string {
	return fmt.Sprintf("$%s is set to the empty string, which is indistinguishable from unset: agora would fall through to its default while looking configured. Give it a value or unset it.", e.Name)
}

// Resolve answers all four questions, or explains why it cannot.
func (r Resolver) Resolve(flags Flags) (Config, error) {
	lookup := r.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	env := strictEnv(lookup)
	// Lenient, for the variables somebody else's tool sets. An empty $USER means the environment is
	// odd, not that a wrong answer is about to be used, and refusing to run every agora command over
	// it would be worse than falling through to the next source.
	inherited := func(name string) (string, bool) {
		value, ok := lookup(name)
		if !ok || strings.TrimSpace(value) == "" {
			return "", false
		}
		return strings.TrimSpace(value), true
	}

	dir := r.Dir
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return Config{}, fmt.Errorf("find the working directory: %w", err)
		}
	}
	locate := r.Locate
	if locate == nil {
		locate = LocateRepo
	}
	// One git invocation for both answers, because agora guard sits in the path of every matching
	// edit and a process spawn is a measured 5.3ms, over ten times the cost of the query it enables.
	repo, inRepo, err := locate(dir)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{}
	if cfg.Layout, err = layoutPath(env, os.UserHomeDir); err != nil {
		return Config{}, err
	}
	if cfg.Database, err = resolveDatabase(flags.Database, env); err != nil {
		return Config{}, err
	}
	if cfg.Channel, err = resolveChannel(flags.Channel, env, repo, inRepo, dir); err != nil {
		return Config{}, err
	}
	cfg.Worktree = resolveWorktree(repo, inRepo, dir)
	if cfg.Member, err = resolveMember(flags, env, inherited, cfg.Worktree.Value); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

type envFunc func(name string) (string, bool, error)

// strictEnv is for the variables agora asks people to set: empty is a mistake worth stopping on, from the cm
// incident where an empty AGORA_DB read as unset and a whole test suite used the real database for weeks while
// appearing isolated.
func strictEnv(lookup func(string) (string, bool)) envFunc {
	return func(name string) (string, bool, error) {
		value, ok := lookup(name)
		if !ok {
			return "", false, nil
		}
		if strings.TrimSpace(value) == "" {
			return "", false, &EmptyEnvError{Name: name}
		}
		return value, true, nil
	}
}

func resolveDatabase(flag string, env envFunc) (Value, error) {
	if flag != "" {
		return Value{Value: flag, Source: SourceFlag}, nil
	}
	if value, ok, err := env(EnvDatabase); err != nil {
		return Value{}, err
	} else if ok {
		return Value{Value: value, Source: "$" + EnvDatabase}, nil
	}
	if value, ok, err := env(EnvXDGData); err != nil {
		return Value{}, err
	} else if ok {
		return Value{Value: filepath.Join(value, "agora", "agora.db"), Source: SourceXDG}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Value{}, fmt.Errorf("find the home directory, and neither $%s nor $%s is set: %w", EnvDatabase, EnvXDGData, err)
	}
	return Value{Value: filepath.Join(home, ".local", "share", "agora", "agora.db"), Source: SourceDefault}, nil
}

func resolveChannel(flag string, env envFunc, repo Repo, inRepo bool, dir string) (Value, error) {
	if flag != "" {
		return Value{Value: flag, Source: SourceFlag}, nil
	}
	if value, ok, err := env(EnvChannel); err != nil {
		return Value{}, err
	} else if ok {
		return Value{Value: value, Source: "$" + EnvChannel}, nil
	}
	if inRepo {
		// The main checkout, shared by every worktree of the repository, which is what makes two
		// agents in separate worktrees land in one channel without configuring anything.
		return Value{Value: repo.Root, Source: SourceRepository}, nil
	}
	// Outside a repository agora still works, keyed on where it was run.
	return Value{Value: dir, Source: SourceWorkingDir}, nil
}

func resolveWorktree(repo Repo, inRepo bool, dir string) Value {
	if inRepo {
		return Value{Value: repo.Worktree, Source: SourceRepository}
	}
	return Value{Value: dir, Source: SourceWorkingDir}
}

func resolveMember(flags Flags, env envFunc, inherited func(string) (string, bool), worktree string) (Value, error) {
	if flags.Member != "" {
		return Value{Value: flags.Member, Source: SourceFlag}, nil
	}
	if value, ok, err := env(EnvMember); err != nil {
		return Value{}, err
	} else if ok {
		return Value{Value: value, Source: "$" + EnvMember}, nil
	}
	// Resolved here rather than inside the branch that uses it so that an empty $AGORA_AGENT is refused on
	// every command that could have wanted it, the same as an empty $AGORA_MEMBER.
	agent, err := resolveAgent(env, inherited)
	if err != nil {
		return Value{}, err
	}
	// A hook event names the session it is about, which is not always the session the process was spawned
	// from. The case this exists for is SessionEnd: the identity that is ending is the one in the event.
	if flags.SessionID != "" {
		return Value{Value: sessionName(agent, flags.SessionID), Source: SourceHookEvent}, nil
	}
	// The session, which is the unit identity should follow: it survives compaction, and two agents are two
	// sessions even in one directory. Not a pid, which changes per invocation and would mean a new member per
	// command, and not the worktree name, which is stable but shared by agents in one checkout.
	//
	// Shortened because people read it, in a claim refusal and in the roster, where a bare uuid says nothing.
	// The worktree is recorded separately.
	//
	// Codex first, and only because of what each variable proves when both are set. Codex injects its thread
	// id per command it runs, so seeing it means this process is a Codex tool call; Claude Code's is exported
	// into a shell, so any descendant inherits it, and Codex passes the whole environment through by default.
	// So running codex inside a Claude Code session leaves both set, and the codex one is the true answer. The
	// mirror image, a Claude Code session started from inside a Codex tool call, is the case this gets wrong,
	// and $AGORA_MEMBER is the fix for it.
	if value, ok := inherited(EnvCodexThread); ok {
		return Value{Value: sessionName(codexAgent, value), Source: SourceCodexThread}, nil
	}
	if value, ok := inherited(EnvClaudeSession); ok {
		return Value{Value: sessionName(claudeAgent, value), Source: SourceAgentSession}, nil
	}
	// Nobody's agent, so somebody's terminal.
	if value, ok := inherited(EnvUser); ok {
		return Value{Value: value, Source: SourceUserName}, nil
	}
	// The floor, for an environment with no $USER at all: a container, or cron. agora has to resolve
	// something, since every command needs a member.
	return Value{Value: filepath.Base(worktree), Source: SourceWorktreeName}, nil
}

// resolveAgent names the harness whose session id a hook event carried, since the id alone does not say.
// $AGORA_AGENT is the answer when it is set, then whichever harness variable the hook's own environment has.
func resolveAgent(env envFunc, inherited func(string) (string, bool)) (string, error) {
	if value, ok, err := env(EnvAgent); err != nil {
		return "", err
	} else if ok {
		return strings.TrimSpace(value), nil
	}
	if _, ok := inherited(EnvCodexThread); ok {
		return codexAgent, nil
	}
	// Claude Code last and unconditionally, rather than only when its variable is set: it is what every
	// existing member in every existing channel was named after, and a harness agora cannot identify is not a
	// reason to rename them all.
	return claudeAgent, nil
}

// sessionName shortens a session id to something a person can read in a roster: the harness, and eight hex
// digits of a hash of the id.
//
// A hash rather than a slice of the id itself, because agora cannot know how the next harness builds an id and
// the failure is silent. Slicing worked for Claude Code, whose session id is a v4 uuid and random throughout,
// and broke on the first harness that did anything else: a Codex thread id is a v7 uuid, so its leading digits
// are a millisecond timestamp and the first eight only change about once a minute. Measured while running
// scripts/codex-hook-experiment.sh, where two arms started 90 seconds apart were both codex-01a03b15 and
// therefore shared a member row, a read cursor and a claim, which is the failure a session id was chosen to
// prevent. Any id whose structure lives at either end has that bug waiting in it, and a hash has no ends.
//
// What it costs is the thing a slice was good for: a name no longer contains any part of the id, so a member
// cannot be matched by eye to a session in a transcript or a hook payload. Two digits fewer than a uuid's worth
// of collision resistance, too, though 32 bits against a roster of tens is not the risk that matters here.
func sessionName(agent, id string) string {
	sum := sha256.Sum256([]byte(id))
	return agent + "-" + hex.EncodeToString(sum[:])[:8]
}
