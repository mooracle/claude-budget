// Package hook installs and removes the prepare-commit-msg / post-commit shim
// pair. The shared recognition marker lets upgrades refresh our own hooks while
// refusing to clobber a third-party hook.
package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Marker identifies a claude-budget-owned hook script.
const Marker = "# claude-budget"

// Names is the hook pair, in install order.
var Names = []string{"prepare-commit-msg", "post-commit"}

// IsInstalled reports whether the primary hook is ours.
func IsInstalled(hooksDir string) bool {
	b, err := os.ReadFile(filepath.Join(hooksDir, "prepare-commit-msg"))
	return err == nil && strings.Contains(string(b), Marker)
}

// Install writes both shims (0755). It validates every target before writing any
// — a third-party collision on either aborts the whole install, so the pair can
// never be left half-installed. `scripts` maps hook name → file contents.
func Install(hooksDir string, scripts map[string]string) error {
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return err
	}
	for _, n := range Names {
		p := filepath.Join(hooksDir, n)
		if b, err := os.ReadFile(p); err == nil && !strings.Contains(string(b), Marker) {
			return fmt.Errorf("%s already exists and is not a claude-budget hook — refusing to overwrite", p)
		}
		if _, ok := scripts[n]; !ok {
			return fmt.Errorf("missing embedded script for %q", n)
		}
	}
	for _, n := range Names {
		p := filepath.Join(hooksDir, n)
		if err := os.WriteFile(p, []byte(scripts[n]), 0o755); err != nil {
			return err
		}
		_ = os.Chmod(p, 0o755) // WriteFile mode is masked by umask; force it
	}
	return nil
}

// Uninstall removes only the hooks that carry our marker.
func Uninstall(hooksDir string) error {
	for _, n := range Names {
		p := filepath.Join(hooksDir, n)
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), Marker) {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	return nil
}
