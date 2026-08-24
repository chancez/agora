package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// EnvXDGConfig is where the layout file lives by default.
const EnvXDGConfig = "XDG_CONFIG_HOME"

// SourceXDGConfig reports a path that came from it.
const SourceXDGConfig = "$" + EnvXDGConfig

// Layout is how wide the view's sidebars are, and the only thing agora keeps about a person's window.
//
// A file rather than the database, deliberately. The database is a shared record that every participant reads,
// where a pane width is a fact about one terminal, and it has no key that would survive a session: a member name
// is a session id, so the same person comes back under a different one. This also stays hand editable, which is
// the half a database cannot do: dragging saves itself, and a default can still be set by opening the file.
//
// The message column is absent because it is whatever the sidebars leave, which is what makes it the column the
// others are dropped to protect.
type Layout struct {
	Channels int `json:"channels,omitempty"`
	Threads  int `json:"threads,omitempty"`
	Members  int `json:"members,omitempty"`
}

func layoutPath(env envFunc, home func() (string, error)) (Value, error) {
	if value, ok, err := env(EnvXDGConfig); err != nil {
		return Value{}, err
	} else if ok {
		return Value{Value: filepath.Join(value, "agora", "tui.json"), Source: SourceXDGConfig}, nil
	}
	dir, err := home()
	if err != nil {
		return Value{}, fmt.Errorf("find the home directory, and $%s is not set: %w", EnvXDGConfig, err)
	}
	return Value{Value: filepath.Join(dir, ".config", "agora", "tui.json"), Source: SourceDefault}, nil
}

// LoadLayout reads the layout at path, and reports the zero one when there is nothing to read.
//
// A missing file is not an error: it is what everybody has until the first drag. A file that cannot be parsed is
// one, because a layout silently ignored looks exactly like a drag that did not save, and hand editing is the
// case that produces it.
//
// The path is passed in rather than resolved here, so it comes from the same resolution every other path does
// and a test cannot end up reading the file of whoever is running it.
func LoadLayout(path string) (Layout, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Layout{}, nil
	}
	if err != nil {
		return Layout{}, fmt.Errorf("read %s: %w", path, err)
	}
	var layout Layout
	if err := json.Unmarshal(raw, &layout); err != nil {
		return Layout{}, fmt.Errorf("read %s: %w", path, err)
	}
	return layout, nil
}

// SaveLayout writes the layout at path, creating the directory if it is missing.
//
// Written through a temporary file in the same directory and renamed, which is atomic: the view saves on every
// drag release, and a half written file read at the next startup would be a layout nobody chose.
func SaveLayout(path string, layout Layout) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	// Indented and with a newline, because the point of a file over a database row is that somebody can open it.
	raw, err := json.MarshalIndent(layout, "", "  ")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	raw = append(raw, '\n')

	temp, err := os.CreateTemp(filepath.Dir(path), ".tui.json.*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
