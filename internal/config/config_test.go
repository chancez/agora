package config_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/chancez/agora/internal/config"
	"github.com/google/go-cmp/cmp"
)

// env builds a lookup over a fixed map, so a test never depends on the developer's real environment.
// A test that read $AGORA_DB from the process would pass or fail depending on whose shell ran it.
func env(vars map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := vars[name]
		return value, ok
	}
}

// fixedRepo stands in for git, so precedence tests do not need a repository on disk.
func fixedRepo(root, worktree string) func(string) (config.Repo, bool, error) {
	return func(string) (config.Repo, bool, error) {
		return config.Repo{Root: root, Worktree: worktree}, true, nil
	}
}

func noRepo(string) (config.Repo, bool, error) { return config.Repo{}, false, nil }

func TestResolve(t *testing.T) {
	for _, tc := range []struct {
		name   string
		flags  config.Flags
		vars   map[string]string
		locate func(string) (config.Repo, bool, error)
		want   config.Config
	}{
		{
			name:   "a person in a repository with nothing configured",
			vars:   map[string]string{"XDG_CONFIG_HOME": "/cfg", "XDG_DATA_HOME": "/home/u/.local/share", "USER": "chancez"},
			locate: fixedRepo("/repo", "/repo/.worktrees/parser"),
			want: config.Config{
				Database: config.Value{Value: "/home/u/.local/share/agora/agora.db", Source: "$XDG_DATA_HOME"},
				Channel:  config.Value{Value: "/repo", Source: "repository"},
				Member:   config.Value{Value: "chancez", Source: "$USER"},
				Worktree: config.Value{Value: "/repo/.worktrees/parser", Source: "repository"},
				Layout:   config.Value{Value: "/cfg/agora/tui.json", Source: config.SourceXDGConfig},
			},
		},
		{
			name:   "an agent, which is its session and not the person running it",
			vars:   map[string]string{"XDG_CONFIG_HOME": "/cfg", "XDG_DATA_HOME": "/x", "USER": "chancez", "CLAUDE_CODE_SESSION_ID": "3750ffad-5fca-49c2-b378-8922bdf4e7cd"},
			locate: fixedRepo("/repo", "/repo/.worktrees/parser"),
			want: config.Config{
				Database: config.Value{Value: "/x/agora/agora.db", Source: "$XDG_DATA_HOME"},
				Channel:  config.Value{Value: "/repo", Source: "repository"},
				Member:   config.Value{Value: "claude-da2e43d2", Source: "$CLAUDE_CODE_SESSION_ID"},
				Worktree: config.Value{Value: "/repo/.worktrees/parser", Source: "repository"},
				Layout:   config.Value{Value: "/cfg/agora/tui.json", Source: config.SourceXDGConfig},
			},
		},
		{
			name:   "a Codex session, which names its harness rather than the one agora was written against",
			vars:   map[string]string{"XDG_CONFIG_HOME": "/cfg", "XDG_DATA_HOME": "/x", "USER": "chancez", "CODEX_THREAD_ID": "01a03b15-06a4-7aa3-a7e5-46287dec58e9"},
			locate: fixedRepo("/repo", "/repo/.worktrees/parser"),
			want: config.Config{
				Database: config.Value{Value: "/x/agora/agora.db", Source: "$XDG_DATA_HOME"},
				Channel:  config.Value{Value: "/repo", Source: "repository"},
				Member:   config.Value{Value: "codex-e7784ad5", Source: "$CODEX_THREAD_ID"},
				Worktree: config.Value{Value: "/repo/.worktrees/parser", Source: "repository"},
				Layout:   config.Value{Value: "/cfg/agora/tui.json", Source: config.SourceXDGConfig},
			},
		},
		{
			// Codex inherits the whole environment by default, so running codex inside a Claude Code session
			// leaves both set in the tool shell. The Codex id is the one that is true of this process: it is
			// injected per command Codex runs, where the Claude Code one is inherited by any descendant.
			name:   "codex running inside a Claude Code session is the codex thread",
			vars:   map[string]string{"XDG_CONFIG_HOME": "/cfg", "XDG_DATA_HOME": "/x", "USER": "chancez", "CLAUDE_CODE_SESSION_ID": "3750ffad-5fca-49c2-b378-8922bdf4e7cd", "CODEX_THREAD_ID": "01a03b15-06a4-7aa3-a7e5-46287dec58e9"},
			locate: fixedRepo("/repo", "/repo/.worktrees/parser"),
			want: config.Config{
				Database: config.Value{Value: "/x/agora/agora.db", Source: "$XDG_DATA_HOME"},
				Channel:  config.Value{Value: "/repo", Source: "repository"},
				Member:   config.Value{Value: "codex-e7784ad5", Source: "$CODEX_THREAD_ID"},
				Worktree: config.Value{Value: "/repo/.worktrees/parser", Source: "repository"},
				Layout:   config.Value{Value: "/cfg/agora/tui.json", Source: config.SourceXDGConfig},
			},
		},
		{
			name:   "no session and no user at all, in a container or under cron",
			vars:   map[string]string{"XDG_CONFIG_HOME": "/cfg", "XDG_DATA_HOME": "/x"},
			locate: fixedRepo("/repo", "/repo/.worktrees/parser"),
			want: config.Config{
				Database: config.Value{Value: "/x/agora/agora.db", Source: "$XDG_DATA_HOME"},
				Channel:  config.Value{Value: "/repo", Source: "repository"},
				Member:   config.Value{Value: "parser", Source: "worktree name"},
				Worktree: config.Value{Value: "/repo/.worktrees/parser", Source: "repository"},
				Layout:   config.Value{Value: "/cfg/agora/tui.json", Source: config.SourceXDGConfig},
			},
		},
		{
			name:   "flags win over everything",
			flags:  config.Flags{Database: "/tmp/dev/agora.db", Channel: "devtest", Member: "alice"},
			vars:   map[string]string{"XDG_CONFIG_HOME": "/cfg", "AGORA_DB": "/env/agora.db", "AGORA_CHANNEL": "envchan", "AGORA_MEMBER": "envmember"},
			locate: fixedRepo("/repo", "/repo"),
			want: config.Config{
				Database: config.Value{Value: "/tmp/dev/agora.db", Source: "flag"},
				Channel:  config.Value{Value: "devtest", Source: "flag"},
				Member:   config.Value{Value: "alice", Source: "flag"},
				Worktree: config.Value{Value: "/repo", Source: "repository"},
				Layout:   config.Value{Value: "/cfg/agora/tui.json", Source: config.SourceXDGConfig},
			},
		},
		{
			name:   "the environment wins over what is derived",
			vars:   map[string]string{"XDG_CONFIG_HOME": "/cfg", "AGORA_DB": "/env/agora.db", "AGORA_CHANNEL": "envchan", "AGORA_MEMBER": "envmember", "XDG_DATA_HOME": "/ignored", "CLAUDE_CODE_SESSION_ID": "ignored-too", "USER": "ignored-as-well"},
			locate: fixedRepo("/repo", "/repo"),
			want: config.Config{
				Database: config.Value{Value: "/env/agora.db", Source: "$AGORA_DB"},
				Channel:  config.Value{Value: "envchan", Source: "$AGORA_CHANNEL"},
				Member:   config.Value{Value: "envmember", Source: "$AGORA_MEMBER"},
				Worktree: config.Value{Value: "/repo", Source: "repository"},
				Layout:   config.Value{Value: "/cfg/agora/tui.json", Source: config.SourceXDGConfig},
			},
		},
		{
			// An empty variable somebody else's tool set is not a mistake agora should refuse to run
			// over, unlike an empty AGORA_DB, which is a claim of isolation that is not true.
			name:   "an empty session and user fall through rather than failing",
			vars:   map[string]string{"XDG_CONFIG_HOME": "/cfg", "XDG_DATA_HOME": "/x", "CLAUDE_CODE_SESSION_ID": "", "USER": "  "},
			locate: fixedRepo("/repo", "/repo/.worktrees/parser"),
			want: config.Config{
				Database: config.Value{Value: "/x/agora/agora.db", Source: "$XDG_DATA_HOME"},
				Channel:  config.Value{Value: "/repo", Source: "repository"},
				Member:   config.Value{Value: "parser", Source: "worktree name"},
				Worktree: config.Value{Value: "/repo/.worktrees/parser", Source: "repository"},
				Layout:   config.Value{Value: "/cfg/agora/tui.json", Source: config.SourceXDGConfig},
			},
		},
		{
			name:   "outside a repository",
			vars:   map[string]string{"XDG_CONFIG_HOME": "/cfg", "XDG_DATA_HOME": "/x", "USER": "chancez"},
			locate: noRepo,
			want: config.Config{
				Database: config.Value{Value: "/x/agora/agora.db", Source: "$XDG_DATA_HOME"},
				Channel:  config.Value{Value: "/somewhere/else", Source: "working directory"},
				Member:   config.Value{Value: "chancez", Source: "$USER"},
				Worktree: config.Value{Value: "/somewhere/else", Source: "working directory"},
				Layout:   config.Value{Value: "/cfg/agora/tui.json", Source: config.SourceXDGConfig},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := config.Resolver{
				LookupEnv: env(tc.vars),
				Dir:       "/somewhere/else",
				Locate:    tc.locate,
			}
			got, err := resolver.Resolve(tc.flags)
			if err != nil {
				t.Fatalf("Resolve(): %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Resolve(), -want +got:\n%s", diff)
			}
		})
	}
}

// TestTwoAgentsInOneWorktreeAreTwoMembers is what the session id buys over the worktree name it
// replaced. Sharing a checkout is not rare: a subagent, or two agents on one machine both told to look
// at the same tree. Sharing an identity there means sharing a cursor, so one of them marks the other's
// unread messages read, and the roster shows one member where there are two.
func TestTwoAgentsInOneWorktreeAreTwoMembers(t *testing.T) {
	// Real ids, from two nested claude -p invocations under one parent session.
	first := resolveIn(t, map[string]string{"AGORA_DB": "/tmp/agora-test/agora.db", "USER": "chancez",
		"CLAUDE_CODE_SESSION_ID": "f7cc78ba-72ae-4e7e-a4e7-64c8de112c0f"})
	second := resolveIn(t, map[string]string{"AGORA_DB": "/tmp/agora-test/agora.db", "USER": "chancez",
		"CLAUDE_CODE_SESSION_ID": "64e2143c-f638-4452-983b-fc4eae6453ef"})

	want := config.Value{Value: "claude-1adbb3b8", Source: "$CLAUDE_CODE_SESSION_ID"}
	if diff := cmp.Diff(want, first.Member); diff != "" {
		t.Errorf("the first agent's member, -want +got:\n%s", diff)
	}
	if first.Member.Value == second.Member.Value {
		t.Errorf("both agents resolved to %q, want two members", first.Member.Value)
	}
	// Same channel and same worktree: what differs is who, which is the whole point.
	if first.Channel != second.Channel || first.Worktree != second.Worktree {
		t.Errorf("the two agents disagree about where they are:\n%+v\n%+v", first, second)
	}
}

func resolveIn(t *testing.T, vars map[string]string) config.Config {
	t.Helper()
	return resolveWith(t, vars, config.Flags{})
}

func resolveWith(t *testing.T, vars map[string]string, flags config.Flags) config.Config {
	t.Helper()
	resolver := config.Resolver{
		LookupEnv: env(vars),
		Dir:       "/repo/.worktrees/parser",
		Locate:    fixedRepo("/repo", "/repo/.worktrees/parser"),
	}
	got, err := resolver.Resolve(flags)
	if err != nil {
		t.Fatalf("Resolve(): %v", err)
	}
	return got
}

// TestAHookAndTheSessionItIsAboutAreOneMember is the property that makes a second harness work at all. A hook
// resolves the identity from the session_id in the event it was handed; the agent's own commands resolve it from
// the harness's variable in their environment. Those two have to land on the same name, or one session is two
// members: the hook's member accumulates the cursors and the claims, and the agent cannot read or release
// either, while the roster shows two agents where there is one.
//
// The id is the same in both columns on purpose. What differs is only what each process can see.
func TestAHookAndTheSessionItIsAboutAreOneMember(t *testing.T) {
	const id = "01a03b15-06a4-7aa3-a7e5-46287dec58e9"
	for _, tc := range []struct {
		name string
		// shell is what the agent's own `agora post` sees, and hook is what the hook process sees.
		shell map[string]string
		hook  map[string]string
		want  string
	}{
		{
			name:  "Claude Code, which sets its session id for a hook as well as for a shell",
			shell: map[string]string{"CLAUDE_CODE_SESSION_ID": id},
			hook:  map[string]string{"CLAUDE_CODE_SESSION_ID": id},
			want:  "claude-e7784ad5",
		},
		{
			name:  "Codex, if a hook inherits the thread id",
			shell: map[string]string{"CODEX_THREAD_ID": id},
			hook:  map[string]string{"CODEX_THREAD_ID": id},
			want:  "codex-e7784ad5",
		},
		{
			// The case the wiring in docs/setup.md covers, since nothing promises a Codex hook is given the
			// thread id: with $AGORA_AGENT in the hook command the two agree anyway.
			name:  "Codex, where the hook has only what the wiring told it",
			shell: map[string]string{"CODEX_THREAD_ID": id},
			hook:  map[string]string{"AGORA_AGENT": "codex"},
			want:  "codex-e7784ad5",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, vars := range []map[string]string{tc.shell, tc.hook} {
				vars["AGORA_DB"] = "/tmp/agora-test/agora.db"
				vars["XDG_CONFIG_HOME"] = "/cfg"
				vars["USER"] = "chancez"
			}
			// Only the hook is handed a session id, because only the hook is given an event.
			shell := resolveIn(t, tc.shell)
			hook := resolveWith(t, tc.hook, config.Flags{SessionID: id})

			if shell.Member.Value != tc.want || hook.Member.Value != tc.want {
				t.Errorf("the agent resolved %q and its hook resolved %q, want both %q",
					shell.Member.Value, hook.Member.Value, tc.want)
			}
		})
	}
}

// TestAHookWithNoHarnessNamedFallsBackToClaudeCode pins the fallback rather than leaving it to be discovered.
// Every member in every existing channel was named this way, so a hook whose environment says nothing keeps
// naming them the same. It is also why the Codex wiring sets $AGORA_AGENT: without it, this is the name a Codex
// hook would use while the agent's own commands used codex-, and one session would be two members.
func TestAHookWithNoHarnessNamedFallsBackToClaudeCode(t *testing.T) {
	got := resolveWith(t, map[string]string{"AGORA_DB": "/tmp/agora-test/agora.db", "XDG_CONFIG_HOME": "/cfg"},
		config.Flags{SessionID: "3750ffad-5fca-49c2-b378-8922bdf4e7cd"})
	want := config.Value{Value: "claude-da2e43d2", Source: config.SourceHookEvent}
	if diff := cmp.Diff(want, got.Member); diff != "" {
		t.Errorf("Resolve() from a hook event alone, -want +got:\n%s", diff)
	}
}

// TestTwoCodexSessionsStartedTogetherAreTwoMembers is the same property as
// TestTwoAgentsInOneWorktreeAreTwoMembers, for the id format that breaks it. A Codex thread id is a v7 uuid, so
// its leading digits are a millisecond timestamp and the first eight of them only change about once a minute.
//
// The two ids here are real, from two arms of scripts/codex-hook-experiment.sh started 90 seconds apart. Naming
// a member after the first eight digits gave both of them codex-01a03b15, so they shared a cursor and a claim,
// and the roster showed one agent where there were two. That is the failure the session id was chosen to
// prevent, reappearing because the harness's id is structured where the other one is random.
func TestTwoCodexSessionsStartedTogetherAreTwoMembers(t *testing.T) {
	first := resolveIn(t, map[string]string{"AGORA_DB": "/tmp/agora-test/agora.db",
		"CODEX_THREAD_ID": "01a03b15-06a4-7aa3-a7e5-46287dec58e9"})
	second := resolveIn(t, map[string]string{"AGORA_DB": "/tmp/agora-test/agora.db",
		"CODEX_THREAD_ID": "01a03b15-82c1-7342-8755-b0c5a467e4cd"})

	want := config.Value{Value: "codex-e7784ad5", Source: config.SourceCodexThread}
	if diff := cmp.Diff(want, first.Member); diff != "" {
		t.Errorf("the first Codex session's member, -want +got:\n%s", diff)
	}
	if first.Member.Value == second.Member.Value {
		t.Errorf("both Codex sessions resolved to %q, want two members", first.Member.Value)
	}
}

// TestResolveRejectsAnEmptyVariable is the cm incident as a test. An empty variable reads as unset and
// falls through to the default, so AGORA_DB= looks like isolation and is not. An entire test suite
// there read the developer's real config for weeks while appearing isolated.
func TestResolveRejectsAnEmptyVariable(t *testing.T) {
	for _, name := range []string{"AGORA_DB", "AGORA_CHANNEL", "AGORA_MEMBER", "AGORA_AGENT", "XDG_DATA_HOME"} {
		t.Run(name, func(t *testing.T) {
			resolver := config.Resolver{
				LookupEnv: env(map[string]string{name: ""}),
				Dir:       "/repo",
				Locate:    fixedRepo("/repo", "/repo"),
			}
			got, err := resolver.Resolve(config.Flags{})
			var empty *config.EmptyEnvError
			if !errors.As(err, &empty) {
				t.Fatalf("Resolve() with $%s empty: error = %v, want an EmptyEnvError", name, err)
			}
			if empty.Name != name {
				t.Errorf("EmptyEnvError names %q, want %q", empty.Name, name)
			}
			if diff := cmp.Diff(config.Config{}, got); diff != "" {
				t.Errorf("Resolve(), -want +got:\n%s", diff)
			}
		})
	}
}

func TestResolveWhitespaceIsAlsoEmpty(t *testing.T) {
	resolver := config.Resolver{
		LookupEnv: env(map[string]string{"AGORA_DB": "   "}),
		Dir:       "/repo",
		Locate:    fixedRepo("/repo", "/repo"),
	}
	// A path of spaces is a mistake in a shell quoting a variable, not a request.
	if _, err := resolver.Resolve(config.Flags{}); err == nil {
		t.Error("Resolve() with $AGORA_DB set to spaces succeeded, want an error")
	}
}

// TestWorktreesOfOneRepositoryShareAChannel is the derivation that shipped broken in cm's a2a skill,
// against a real repository. Getting it wrong returns an empty roster, which reads as "nobody else is
// working here" precisely when it matters.
func TestWorktreesOfOneRepositoryShareAChannel(t *testing.T) {
	main := initRepo(t)
	worktree := filepath.Join(main, ".worktrees", "parser")
	git(t, main, "worktree", "add", "-q", "-b", "pr/parser", worktree)
	subdir := filepath.Join(worktree, "internal", "lex")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("create %s: %v", subdir, err)
	}

	// The main checkout is the resolved main checkout, whatever it is on this platform: on darwin
	// /var is a symlink to /private/var, and git reports the resolved path while a path built by
	// joining does not.
	wantRoot := evalSymlinks(t, main)

	for _, tc := range []struct {
		name         string
		dir          string
		wantWorktree string
		wantMember   string
	}{
		{name: "the main checkout", dir: main, wantWorktree: wantRoot, wantMember: filepath.Base(wantRoot)},
		{name: "a linked worktree", dir: worktree, wantWorktree: evalSymlinks(t, worktree), wantMember: "parser"},
		{name: "a subdirectory of a worktree", dir: subdir, wantWorktree: evalSymlinks(t, worktree), wantMember: "parser"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := config.Resolver{
				LookupEnv: env(map[string]string{"AGORA_DB": "/tmp/agora-test/agora.db", "XDG_CONFIG_HOME": "/cfg"}),
				Dir:       tc.dir,
			}
			got, err := resolver.Resolve(config.Flags{})
			if err != nil {
				t.Fatalf("Resolve(): %v", err)
			}
			want := config.Config{
				Database: config.Value{Value: "/tmp/agora-test/agora.db", Source: "$AGORA_DB"},
				Channel:  config.Value{Value: wantRoot, Source: "repository"},
				Member:   config.Value{Value: tc.wantMember, Source: "worktree name"},
				Worktree: config.Value{Value: tc.wantWorktree, Source: "repository"},
				Layout:   config.Value{Value: "/cfg/agora/tui.json", Source: config.SourceXDGConfig},
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("Resolve() in %s, -want +got:\n%s", tc.dir, diff)
			}
		})
	}
}

func TestLocateRepoOutsideARepository(t *testing.T) {
	dir := t.TempDir()
	// t.TempDir is not inside a repository, but a developer's /tmp could be anywhere, so this asserts
	// the shape rather than the value.
	repo, inRepo, err := config.LocateRepo(dir)
	if err != nil {
		t.Fatalf("LocateRepo(): %v", err)
	}
	if inRepo {
		t.Skipf("%s is inside a repository (%+v), so there is nothing to test here", dir, repo)
	}
	if diff := cmp.Diff(config.Repo{}, repo); diff != "" {
		t.Errorf("LocateRepo(), -want +got:\n%s", diff)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Hermetic: the developer's global config can set hooks, templates, and a default branch, and
	// this test is about agora's derivation rather than about their setup.
	cmd.Env = append(cmd.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=agora test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=agora test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func evalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return resolved
}
