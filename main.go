// Command claude-budget tracks GitHub Claude Code token usage per commit and
// appends git trailers (e.g. "Claude-Cost: 0.42") to commit messages.
//
// It is the "brain" invoked by two thin shell hooks installed by `setup`:
//
//	prepare-commit-msg → claude-budget trailer "$1"   (scan, price, append, stage watermark)
//	post-commit        → claude-budget consume         (promote the staged watermark)
//
// See docs/plans/2026-06-14-claude-budget.md for the full design.
package main

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mooracle/claude-budget/internal/gitutil"
	"github.com/mooracle/claude-budget/internal/hook"
	"github.com/mooracle/claude-budget/internal/pricing"
	"github.com/mooracle/claude-budget/internal/reader"
	"github.com/mooracle/claude-budget/internal/state"
)

// The canonical rate card lives in data/ and is embedded at build time; pricing
// parses it at runtime. Embed lives in the root package because go:embed cannot
// reach across "../" from internal/pricing.
//
//go:embed data/claude-pricing.json
var pricingData []byte

// Hook shims, embedded so `setup` can write them without a data dir at runtime.
//
//go:embed hooks/prepare-commit-msg hooks/post-commit
var hookFS embed.FS

const version = "0.1.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "version", "-v", "--version":
		fmt.Println(version)
	case "setup":
		err = runSetup()
	case "uninstall":
		err = runUninstall()
	case "status":
		err = runStatus()
	case "price":
		err = runPriceDemo()
	case "trailer", "consume":
		fmt.Fprintf(os.Stderr, "claude-budget %s: not yet implemented — see docs/plans/2026-06-14-claude-budget.md\n", os.Args[1])
		os.Exit(1)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "claude-budget:", err)
		os.Exit(1)
	}
}

func runSetup() error {
	hooksDir, err := gitutil.HooksDir()
	if err != nil {
		return fmt.Errorf("resolve hooks dir (are you in a git repo?): %w", err)
	}
	scripts, err := loadScripts()
	if err != nil {
		return err
	}
	if err := hook.Install(hooksDir, scripts); err != nil {
		return err
	}
	fmt.Printf("✓ installed claude-budget hooks in %s\n", hooksDir)
	if root, err := gitutil.RepoRoot(); err == nil {
		cfg := filepath.Join(root, ".claude-budget.toml")
		if _, err := os.Stat(cfg); os.IsNotExist(err) {
			fmt.Printf("  tip: add %s to choose trailers (default: Claude-Cost only)\n", cfg)
		}
	}
	fmt.Println("  run `claude-budget status` any time to see uncommitted usage")
	return nil
}

func runUninstall() error {
	hooksDir, err := gitutil.HooksDir()
	if err != nil {
		return err
	}
	if err := hook.Uninstall(hooksDir); err != nil {
		return err
	}
	fmt.Printf("✓ removed claude-budget hooks from %s\n", hooksDir)
	return nil
}

func runStatus() error {
	root, err := gitutil.RepoRoot()
	if err != nil {
		return fmt.Errorf("not in a git repository")
	}
	gitDir, err := gitutil.GitDir()
	if err != nil {
		return err
	}
	branch, err := gitutil.CurrentBranch()
	if err != nil {
		return err
	}
	rc, err := pricing.Load(pricingData)
	if err != nil {
		return err
	}
	st, err := state.Load(gitDir)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	projects := filepath.Join(home, ".claude", "projects")

	res, err := reader.Scan(projects, root, branch, st.HwmFor(branch), rc)
	if err != nil {
		return err
	}

	fmt.Printf("claude-budget — %s @ branch %q\n\n", root, branch)
	if branch == "HEAD" {
		fmt.Println("  (detached HEAD — branch attribution unavailable)")
	}
	if res.Requests == 0 {
		fmt.Println("  nothing pending since the last commit on this branch.")
	} else {
		fmt.Printf("  pending since last commit:  %s   ·   %d tokens   ·   %d requests\n\n",
			money(res.TotalCostUSD), res.TotalTokens, res.Requests)
		fmt.Printf("  %-20s %10s %14s %6s\n", "model", "cost", "tokens", "reqs")
		for _, m := range res.Models {
			name := m.Model
			if !rc.Known(name) {
				name += " (unpriced)"
			}
			fmt.Printf("  %-20s %10s %14d %6d\n", name, money(m.CostUSD), m.Tokens, m.Requests)
		}
		fmt.Println()
	}

	hooksDir, _ := gitutil.HooksDir()
	if hook.IsInstalled(hooksDir) {
		fmt.Println("  hooks: installed (trailers attach on commit)")
	} else {
		fmt.Println("  hooks: not installed — run `claude-budget setup`")
	}
	return nil
}

func runPriceDemo() error {
	rc, err := pricing.Load(pricingData)
	if err != nil {
		return fmt.Errorf("load rate card: %w", err)
	}
	u := pricing.Usage{Input: 3282, Output: 236, CacheRead: 16623, CacheWrite1h: 3128}
	fmt.Printf("rate card version %s (%s)\n", rc.Version, rc.Currency)
	fmt.Printf("sample claude-opus-4-8 request → %s\n", money(rc.CostUSD("claude-opus-4-8", u)))
	return nil
}

func loadScripts() (map[string]string, error) {
	m := make(map[string]string, len(hook.Names))
	for _, n := range hook.Names {
		b, err := hookFS.ReadFile("hooks/" + n)
		if err != nil {
			return nil, fmt.Errorf("read embedded hook %q: %w", n, err)
		}
		m[n] = string(b)
	}
	return m, nil
}

func money(v float64) string {
	if v >= 0.005 || v == 0 {
		return fmt.Sprintf("$%.2f", v)
	}
	return fmt.Sprintf("$%.6f", v)
}

func usage() {
	fmt.Fprint(os.Stderr, `claude-budget — per-commit Claude Code token-cost trailers

usage:
  claude-budget setup       install the git hook pair in this repo
  claude-budget uninstall   remove the git hook pair
  claude-budget status      show this branch's uncommitted usage and cost
  claude-budget trailer <msgfile>   append cost trailers (prepare-commit-msg)   [pending]
  claude-budget consume             promote the staged watermark (post-commit)  [pending]
  claude-budget price       smoke-test: load the rate card and price a sample
  claude-budget version     print version
`)
}
