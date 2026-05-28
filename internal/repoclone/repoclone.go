// Package repoclone auto-clones a bare GitHub repo the first time a framework
// command references it locally, then runs the project's optional post-clone
// setup script against the new repo.
//
// The clone URL is derived from an owner/repo pair (e.g. `knime/knime-core-ui`
// → `git@github.com:knime/knime-core-ui.git`). Callers that only know the
// target bare path use [EnsureBareFromPath], which defaults the owner to
// [DefaultOwner] and pulls the repo name from the path's basename.
package repoclone

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultOwner is the GitHub org used when a caller does not supply one.
// All current framework2 users target knime org repos; revisit when a
// non-knime project needs auto-clone.
const DefaultOwner = "knime"

// EnsureBare guarantees a bare git repo exists at barePath. When barePath
// is missing, it clones `git@github.com:<owner>/<repo>.git --bare` and then
// runs setupScript (if non-empty) with barePath as its sole argument. When
// barePath already exists, EnsureBare is a no-op: it does not re-run the
// setup script (per the ticket's "freshly-cloned case only" rule).
//
// Empty owner falls back to [DefaultOwner]; empty repo falls back to the
// basename of barePath with a trailing `.git` stripped.
func EnsureBare(barePath, owner, repo, setupScript string) error {
	if _, err := os.Stat(barePath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat bare repo %s: %w", barePath, err)
	}
	if owner == "" {
		owner = DefaultOwner
	}
	if repo == "" {
		repo = strings.TrimSuffix(filepath.Base(barePath), ".git")
	}
	if repo == "" {
		return fmt.Errorf("auto-clone: cannot derive repo name from %q", barePath)
	}
	url := fmt.Sprintf("git@github.com:%s/%s.git", owner, repo)
	if parent := filepath.Dir(barePath); parent != "" {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("create parent dir for %s: %w", barePath, err)
		}
	}
	fmt.Fprintf(os.Stderr, "auto-clone: %s -> %s\n", url, barePath)
	cmd := exec.Command("git", "clone", "--bare", url, barePath)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone --bare %s: %w", url, err)
	}
	if setupScript == "" {
		return nil
	}
	script, err := expandHome(setupScript)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "auto-clone: running setup script %s %s\n", script, barePath)
	sc := exec.Command(script, barePath)
	sc.Stdout = os.Stderr
	sc.Stderr = os.Stderr
	if err := sc.Run(); err != nil {
		return fmt.Errorf("setup script %s %s: %w", script, barePath, err)
	}
	return nil
}

// EnsureBareFromPath is [EnsureBare] for callers that only know the target
// bare path (e.g. `ticket dir worktree <bare>`). Owner defaults to
// [DefaultOwner] and the repo is derived from the basename.
func EnsureBareFromPath(barePath, setupScript string) error {
	return EnsureBare(barePath, "", "", setupScript)
}

func expandHome(p string) (string, error) {
	if !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand ~ in %q: %w", p, err)
	}
	return filepath.Join(home, p[2:]), nil
}
