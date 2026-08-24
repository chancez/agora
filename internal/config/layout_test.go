package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// lookupFrom is a fake environment. Its own rather than the one in config_test.go, because that file is the
// external test package and these functions are unexported.
func lookupFrom(vars map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := vars[name]
		return value, ok
	}
}

func TestLayoutPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		home string
		want Value
	}{
		{
			name: "from the config directory",
			env:  map[string]string{EnvXDGConfig: "/cfg"},
			want: Value{Value: "/cfg/agora/tui.json", Source: SourceXDGConfig},
		},
		{
			name: "from the home directory",
			home: "/home/c",
			want: Value{Value: "/home/c/.config/agora/tui.json", Source: SourceDefault},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := layoutPath(strictEnv(lookupFrom(tc.env)), func() (string, error) { return tc.home, nil })
			if err != nil {
				t.Fatalf("layoutPath(): %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("layoutPath(), -want +got:\n%s", diff)
			}
		})
	}

	// The same rule every other variable follows: empty is indistinguishable from unset, so it is a mistake
	// worth stopping on rather than a silent fall through to a different file.
	_, err := layoutPath(strictEnv(lookupFrom(map[string]string{EnvXDGConfig: ""})), os.UserHomeDir)
	var empty *EmptyEnvError
	if !errors.As(err, &empty) {
		t.Errorf("layoutPath() with an empty $%s = %v, want an EmptyEnvError", EnvXDGConfig, err)
	}
}

// TestLayoutRoundTrip is the whole contract: what the view saved is what it opens at next time, and the file is
// something a person can open, since being hand editable is the reason this is a file and not a database row.
func TestLayoutRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "tui.json")
	want := Layout{Channels: 22, Threads: 30, Members: 18}

	if err := SaveLayout(path, want); err != nil {
		t.Fatalf("SaveLayout(): %v", err)
	}
	got, err := LoadLayout(path)
	if err != nil {
		t.Fatalf("LoadLayout(): %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("LoadLayout(), -want +got:\n%s", diff)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if !strings.Contains(string(raw), "\n  \"channels\": 22") {
		t.Errorf("the file is not laid out for a reader:\n%s", raw)
	}
	// Nothing is left behind by the write, which goes through a temporary file in the same directory so a
	// half written one is never what the next startup reads.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read the directory: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("%d files in the directory, want only tui.json: %v", len(entries), entries)
	}
}

func TestLoadLayoutWithNothingSaved(t *testing.T) {
	// What everybody has until the first drag, so it is the zero layout and not an error.
	got, err := LoadLayout(filepath.Join(t.TempDir(), "tui.json"))
	if err != nil {
		t.Fatalf("LoadLayout() with no file: %v", err)
	}
	if diff := cmp.Diff(Layout{}, got); diff != "" {
		t.Errorf("LoadLayout() with no file, -want +got:\n%s", diff)
	}
}

// TestLoadLayoutReportsAFileItCannotRead is the hand editing case: a layout silently ignored looks exactly like
// a drag that did not save, and the view says so in the corner rather than starting up pretending.
func TestLoadLayoutReportsAFileItCannotRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tui.json")
	if err := os.WriteFile(path, []byte("{channels: 22"), 0o600); err != nil {
		t.Fatalf("write it: %v", err)
	}

	if _, err := LoadLayout(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("LoadLayout() of a broken file = %v, want an error naming it", err)
	}
}
