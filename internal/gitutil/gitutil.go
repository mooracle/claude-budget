// Package gitutil wraps the git CLI for the paths claude-budget needs:
// worktree root, git dir, current branch, the effective hooks dir (honoring
// core.hooksPath), and a rebase-in-progress check.
package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

// RepoRoot is the worktree top-level (git rev-parse --show-toplevel).
func RepoRoot() (string, error) { return git("rev-parse", "--show-toplevel") }

// GitDir is the absolute git dir — worktree-specific in a linked worktree, so
// each worktree keeps independent state (git rev-parse --absolute-git-dir).
func GitDir() (string, error) { return git("rev-parse", "--absolute-git-dir") }

// CurrentBranch returns the checked-out branch, or "HEAD" when detached.
// symbolic-ref works on an unborn branch (fresh repo, no commits yet); the
// rev-parse fallback yields "HEAD" only when truly detached.
func CurrentBranch() (string, error) {
	if b, err := git("symbolic-ref", "--short", "HEAD"); err == nil && b != "" {
		return b, nil
	}
	return git("rev-parse", "--abbrev-ref", "HEAD")
}

// HooksDir is where git looks for hook scripts: core.hooksPath if set (absolute
// verbatim, relative resolved against the worktree root), else <gitDir>/hooks.
func HooksDir() (string, error) {
	if hp, err := git("config", "--get", "core.hooksPath"); err == nil && hp != "" {
		if filepath.IsAbs(hp) {
			return hp, nil
		}
		root, err := RepoRoot()
		if err != nil {
			return "", err
		}
		return filepath.Join(root, hp), nil
	}
	gd, err := GitDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gd, "hooks"), nil
}

// RebaseInProgress reports whether a rebase is underway (consume must skip).
func RebaseInProgress() bool {
	gd, err := GitDir()
	if err != nil {
		return false
	}
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if st, err := os.Stat(filepath.Join(gd, d)); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}
