package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scripts returns a valid script map whose contents carry the recognition marker
// (as the real shims do), so a reinstall recognizes them as ours.
func scripts() map[string]string {
	return map[string]string{
		"prepare-commit-msg": "#!/bin/sh\n" + Marker + "\nclaude-budget trailer \"$1\" --source \"${2:-}\"\n",
		"post-commit":        "#!/bin/sh\n" + Marker + "\nclaude-budget consume\n",
	}
}

func TestInstallWritesBothWithMarkerAndMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hooks") // exercise MkdirAll
	if err := Install(dir, scripts()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	for _, n := range Names {
		p := filepath.Join(dir, n)
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", n, err)
		}
		if mode := info.Mode().Perm(); mode != 0o755 {
			t.Errorf("%s mode = %o, want 0755", n, mode)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", n, err)
		}
		if !strings.Contains(string(b), Marker) {
			t.Errorf("%s missing marker", n)
		}
	}
	if !IsInstalled(dir) {
		t.Error("IsInstalled should report true after install")
	}
}

func TestIsInstalledFalseWhenAbsent(t *testing.T) {
	if IsInstalled(t.TempDir()) {
		t.Error("IsInstalled should be false with no hooks present")
	}
}

func TestIsInstalledFalseForThirdPartyHook(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prepare-commit-msg"), []byte("#!/bin/sh\nsomeone elses hook\n"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if IsInstalled(dir) {
		t.Error("a third-party hook must not read as installed")
	}
}

func TestInstallReinstallRefreshesContent(t *testing.T) {
	dir := t.TempDir()
	if err := Install(dir, scripts()); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	updated := scripts()
	updated["prepare-commit-msg"] = "#!/bin/sh\n" + Marker + "\n# v2\nclaude-budget trailer \"$1\"\n"
	if err := Install(dir, updated); err != nil {
		t.Fatalf("reinstall over our own hook: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "prepare-commit-msg"))
	if !strings.Contains(string(b), "# v2") {
		t.Errorf("reinstall did not refresh content: %q", b)
	}
}

func TestInstallThirdPartyCollisionAbortsWithoutPartialWrite(t *testing.T) {
	dir := t.TempDir()
	// Collide on the SECOND hook in install order; the first must still not be
	// written, proving validate-all-before-write.
	thirdParty := "#!/bin/sh\necho not ours\n"
	if err := os.WriteFile(filepath.Join(dir, "post-commit"), []byte(thirdParty), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err := Install(dir, scripts())
	if err == nil {
		t.Fatal("expected Install to refuse overwriting a third-party hook")
	}
	if _, err := os.Stat(filepath.Join(dir, "prepare-commit-msg")); !os.IsNotExist(err) {
		t.Error("prepare-commit-msg must not be written when the pair aborts")
	}
	// The third-party hook must be left exactly as it was.
	b, _ := os.ReadFile(filepath.Join(dir, "post-commit"))
	if string(b) != thirdParty {
		t.Errorf("third-party post-commit was modified: %q", b)
	}
}

func TestInstallMissingScriptAborts(t *testing.T) {
	dir := t.TempDir()
	incomplete := map[string]string{"prepare-commit-msg": "#!/bin/sh\n" + Marker + "\n"} // post-commit missing
	if err := Install(dir, incomplete); err == nil {
		t.Fatal("expected Install to abort when an embedded script is missing")
	}
	// validate-all-before-write: nothing written.
	if _, err := os.Stat(filepath.Join(dir, "prepare-commit-msg")); !os.IsNotExist(err) {
		t.Error("no hook should be written when a script is missing")
	}
}

func TestUninstallRemovesOnlyMarked(t *testing.T) {
	dir := t.TempDir()
	if err := Install(dir, scripts()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// Replace one of our hooks with a third-party (unmarked) one.
	thirdParty := "#!/bin/sh\necho not ours\n"
	if err := os.WriteFile(filepath.Join(dir, "post-commit"), []byte(thirdParty), 0o755); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if err := Uninstall(dir); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "prepare-commit-msg")); !os.IsNotExist(err) {
		t.Error("our marked hook should have been removed")
	}
	b, err := os.ReadFile(filepath.Join(dir, "post-commit"))
	if err != nil {
		t.Fatalf("third-party hook was removed: %v", err)
	}
	if string(b) != thirdParty {
		t.Errorf("third-party hook was modified: %q", b)
	}
}

func TestUninstallNoHooksIsNoError(t *testing.T) {
	if err := Uninstall(t.TempDir()); err != nil {
		t.Fatalf("Uninstall with nothing installed: %v", err)
	}
}
