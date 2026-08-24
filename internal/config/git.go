package config

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Repo is the repository a directory belongs to.
type Repo struct {
	// Root is the main checkout, shared by every worktree of the repository. This is the channel key.
	Root string
	// Worktree is the checkout the directory itself is in.
	Worktree string
}

// LocateRepo finds the repository containing dir, reporting false when there is none.
//
// Root has shipped broken once already, in cm's a2a skill: `git rev-parse --git-common-dir` is relative to the
// working directory, so from a main checkout it returns `.git`, whose dirname is `.`, and
// `--path-format=absolute` does not reliably fix it. Getting it wrong returns an empty roster, which reads as
// "nobody else is here" exactly when it matters.
//
// Symlinks are resolved on both answers. On darwin /tmp is a symlink to /private/tmp and git reports the
// resolved path, so skipping it gives one repository two channel keys.
func LocateRepo(dir string) (Repo, bool, error) {
	cmd := exec.Command("git", "rev-parse", "--git-common-dir", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		// Not a repository, or no git installed at all. Both mean the same thing to agora, which
		// keys on the working directory instead and keeps working.
		return Repo{}, false, nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		// A bare repository has a common dir and no worktree.
		return Repo{}, false, nil
	}

	commonDir := lines[0]
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(dir, commonDir)
	}
	root, err := resolve(filepath.Dir(commonDir))
	if err != nil {
		return Repo{}, false, err
	}
	worktree, err := resolve(lines[1])
	if err != nil {
		return Repo{}, false, err
	}
	return Repo{Root: root, Worktree: worktree}, true, nil
}

func resolve(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	return resolved, nil
}
