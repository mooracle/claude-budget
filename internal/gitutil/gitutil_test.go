package gitutil

// These tests drive real `git` against throwaway repos. They self-skip when git
// is unavailable, and each uses t.Chdir/t.Setenv (so they run non-parallel) to
// give gitutil's cwd-relative `git` calls a hermetic repo with no global/system
// config bleeding in.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitAvailable() bool { return exec.Command("git", "--version").Run() == nil }

// newRepo creates a fresh repo, chdirs into it, scrubs global/system git config,
// and returns the repo dir (symlinks resolved, as git reports paths).
func newRepo(t *testing.T) string {
	t.Helper()
	if !gitAvailable() {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	mustGit(t, dir, "init", "-q", "-b", "main")
	t.Chdir(dir)
	root, err := RepoRoot()
	if err != nil {
		t.Fatalf("RepoRoot after init: %v", err)
	}
	return root
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func commitOnce(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}
	mustGit(t, dir, "add", "f.txt")
	mustGit(t, dir,
		"-c", "user.name=Test", "-c", "user.email=test@example.com",
		"commit", "-q", "-m", "init")
}

func TestCurrentBranch_Unborn(t *testing.T) {
	newRepo(t) // fresh repo, no commits yet
	if got, err := CurrentBranch(); err != nil || got != "main" {
		t.Fatalf("CurrentBranch on unborn branch = (%q, %v), want (\"main\", nil)", got, err)
	}
}

func TestCurrentBranch_AfterCommit(t *testing.T) {
	root := newRepo(t)
	commitOnce(t, root)
	if got, err := CurrentBranch(); err != nil || got != "main" {
		t.Fatalf("CurrentBranch = (%q, %v), want (\"main\", nil)", got, err)
	}
}

func TestCurrentBranch_DetachedHead(t *testing.T) {
	root := newRepo(t)
	commitOnce(t, root)
	mustGit(t, root, "checkout", "-q", "--detach", "HEAD")
	if got, err := CurrentBranch(); err != nil || got != "HEAD" {
		t.Fatalf("CurrentBranch when detached = (%q, %v), want (\"HEAD\", nil)", got, err)
	}
}

func TestRepoRootAndGitDir(t *testing.T) {
	root := newRepo(t)
	gd, err := GitDir()
	if err != nil {
		t.Fatalf("GitDir: %v", err)
	}
	if !filepath.IsAbs(gd) {
		t.Errorf("GitDir = %q, want absolute", gd)
	}
	// The default git dir is <root>/.git.
	if want := filepath.Join(root, ".git"); gd != want {
		t.Errorf("GitDir = %q, want %q", gd, want)
	}
}

func TestHooksDir_DefaultUnderGitDir(t *testing.T) {
	newRepo(t)
	gd, err := GitDir()
	if err != nil {
		t.Fatalf("GitDir: %v", err)
	}
	got, err := HooksDir()
	if err != nil {
		t.Fatalf("HooksDir: %v", err)
	}
	if want := filepath.Join(gd, "hooks"); got != want {
		t.Errorf("HooksDir = %q, want %q", got, want)
	}
}

func TestHooksDir_AbsoluteHooksPathVerbatim(t *testing.T) {
	root := newRepo(t)
	abs := filepath.Join(t.TempDir(), "custom-hooks")
	mustGit(t, root, "config", "core.hooksPath", abs)
	got, err := HooksDir()
	if err != nil {
		t.Fatalf("HooksDir: %v", err)
	}
	if got != abs {
		t.Errorf("HooksDir = %q, want absolute hooksPath %q verbatim", got, abs)
	}
}

func TestHooksDir_RelativeHooksPathJoinedToRoot(t *testing.T) {
	root := newRepo(t)
	mustGit(t, root, "config", "core.hooksPath", ".husky")
	got, err := HooksDir()
	if err != nil {
		t.Fatalf("HooksDir: %v", err)
	}
	if want := filepath.Join(root, ".husky"); got != want {
		t.Errorf("HooksDir = %q, want relative hooksPath resolved to %q", got, want)
	}
}

func TestRebaseInProgress(t *testing.T) {
	newRepo(t)
	if RebaseInProgress() {
		t.Fatal("RebaseInProgress = true on a clean repo, want false")
	}
	gd, err := GitDir()
	if err != nil {
		t.Fatalf("GitDir: %v", err)
	}
	// Both backends must be detected: rebase-merge (interactive / merge-backend
	// rebase) and rebase-apply (git am / non-interactive rebase). consume's guard
	// relies on either marker, so a regression dropping one would silently let
	// consume promote watermarks mid-replay.
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		marker := filepath.Join(gd, dir)
		if err := os.MkdirAll(marker, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if !RebaseInProgress() {
			t.Errorf("RebaseInProgress = false with a %s dir present, want true", dir)
		}
		if err := os.RemoveAll(marker); err != nil {
			t.Fatalf("rm %s: %v", dir, err)
		}
		if RebaseInProgress() {
			t.Errorf("RebaseInProgress = true after removing %s, want false", dir)
		}
	}
}
