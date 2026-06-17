// Command claude-budget tracks GitHub Claude Code token usage per commit and
// appends git trailers (e.g. "Claude-Cost: 0.42") to commit messages.
//
// It is the "brain" invoked by two thin shell hooks installed by `setup`:
//
//	prepare-commit-msg → claude-budget trailer "$1"   (scan, price, append, stage watermark)
//	post-commit        → claude-budget consume         (promote the staged watermark)
//
// See docs/plans/completed/2026-06-14-claude-budget.md for the full design.
package main

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooracle/claude-budget/internal/config"
	"github.com/mooracle/claude-budget/internal/gitutil"
	"github.com/mooracle/claude-budget/internal/hook"
	"github.com/mooracle/claude-budget/internal/pricing"
	"github.com/mooracle/claude-budget/internal/reader"
	"github.com/mooracle/claude-budget/internal/state"
	"github.com/mooracle/claude-budget/internal/trailer"
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

const version = "0.1.0"

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
	case "trailer":
		err = runTrailer(os.Args[2:])
	case "consume":
		err = runConsume()
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
	cfg, err := config.Load(root)
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

	fmt.Printf("  config: trailers %s   ·   cost precision %d\n", enabledTrailers(cfg), cfg.Format.CostPrecision)

	hooksDir, _ := gitutil.HooksDir()
	if hook.IsInstalled(hooksDir) {
		fmt.Println("  hooks: installed (trailers attach on commit)")
	} else {
		fmt.Println("  hooks: not installed — run `claude-budget setup`")
	}
	return nil
}

// enabledTrailers lists the config keys for trailers turned on, in declaration
// order. The rendered trailer names (with [format.rename] applied) come from the
// Task 2 formatter — this surfaces the raw config toggles.
func enabledTrailers(cfg *config.Config) string {
	t := cfg.Trailers
	var on []string
	for _, kv := range []struct {
		name string
		on   bool
	}{
		{"cost", t.Cost},
		{"costModels", t.CostModels},
		{"tokens", t.Tokens},
		{"tokensModels", t.TokensModels},
		{"interactions", t.Interactions},
	} {
		if kv.on {
			on = append(on, kv.name)
		}
	}
	if len(on) == 0 {
		return "(none)"
	}
	return strings.Join(on, ", ")
}

// --- trailer command (prepare-commit-msg brain) ------------------------------

// trailerRoute is how a `trailer` invocation is dispatched, decided from the
// commit source ($2) and rebase state. The thin shim pushes all routing into the
// binary; see the routing table in docs/plans/completed/2026-06-14-claude-budget.md.
type trailerRoute int

const (
	routeNormal trailerRoute = iota // scan → append trailers → stage watermark
	routeSum                        // rebase/squash: sum duplicate cost trailers (Task 4)
	routeClear                      // merge / cherry-pick reuse: no trailer, drop the marker
)

// routeTrailer maps (source, rebaseInProgress) to a dispatch decision.
//
//	merge | commit            → clear  (merge commit, or message reuse via -c/-C/cherry-pick)
//	squash                    → sum
//	<rebase in progress>      → sum    (rebase guard wins over a normal source)
//	empty | template | message→ normal
func routeTrailer(source string, rebasing bool) trailerRoute {
	switch source {
	case "merge", "commit":
		return routeClear
	case "squash":
		return routeSum
	}
	if rebasing {
		return routeSum
	}
	return routeNormal
}

// parseTrailerArgs extracts the message-file path and the --source value from the
// args following the "trailer" token. The shim always calls
//
//	claude-budget trailer "$1" --source "${2:-}"
//
// but we parse defensively: --source may be absent or carry an empty value, and
// the --source=<v> form is accepted too.
func parseTrailerArgs(args []string) (msgFile, source string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--source":
			if i+1 < len(args) {
				source = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--source="):
			source = strings.TrimPrefix(a, "--source=")
		case msgFile == "" && !strings.HasPrefix(a, "-"):
			msgFile = a
		}
	}
	return msgFile, source
}

// runTrailer is the prepare-commit-msg entry point. It must never block a commit:
// any internal failure is logged to stderr and we still report success (exit 0).
func runTrailer(args []string) error {
	if err := trailerMain(args); err != nil {
		fmt.Fprintln(os.Stderr, "claude-budget trailer:", err)
	}
	return nil
}

func trailerMain(args []string) error {
	msgFile, source := parseTrailerArgs(args)
	if msgFile == "" {
		return fmt.Errorf("missing commit-message file argument")
	}
	gitDir, err := gitutil.GitDir()
	if err != nil {
		return fmt.Errorf("resolve git dir (not in a git repo?): %w", err)
	}
	if err := dispatchTrailer(gitDir, source, msgFile); err != nil {
		// A prepare-hook failure must not leave a stale pending marker behind. A
		// previous cancelled commit may have staged one, and post-commit promotes
		// whatever marker it finds regardless of whether this commit actually got
		// a trailer. Without clearing it, an error here (e.g. a malformed config)
		// lets the commit proceed trailer-less while post-commit still consumes
		// the old watermark — silently eating usage no trailer ever recorded. Drop
		// it so the unconsumed usage carries forward (state is untouched) to the
		// next commit that successfully decides a trailer.
		if cerr := state.ClearPending(gitDir); cerr != nil {
			fmt.Fprintln(os.Stderr, "claude-budget trailer: clear stale pending marker:", cerr)
		}
		return err
	}
	return nil
}

// dispatchTrailer routes a prepare-commit-msg invocation to the path for its
// commit source. Its caller clears the pending marker on any returned error.
func dispatchTrailer(gitDir, source, msgFile string) error {
	switch routeTrailer(source, gitutil.RebaseInProgress()) {
	case routeClear:
		return state.ClearPending(gitDir)
	case routeSum:
		return runTrailerSum(gitDir, msgFile)
	default:
		return runTrailerNormal(gitDir, msgFile)
	}
}

// runTrailerSum handles the rebase/squash path: a combined message can carry one
// cost trailer per folded commit, so we fold those into a single summed line.
// This path never reads or advances the watermark and always clears the pending
// marker, so the underlying usage carries forward to the next normal commit.
func runTrailerSum(gitDir, msgFile string) error {
	// Sum on whatever the cost trailer is actually named (config-derived, so a
	// [format.rename] still sums). Config-load failures fall back to the default
	// name rather than skipping the fold.
	costName := trailer.Name(nil, trailer.KeyCost)
	if root, err := gitutil.RepoRoot(); err == nil {
		if cfg, err := config.Load(root); err == nil {
			costName = trailer.Name(cfg, trailer.KeyCost)
		} else {
			fmt.Fprintln(os.Stderr, "claude-budget trailer: load config:", err)
		}
	}
	if cur, err := os.ReadFile(msgFile); err != nil {
		fmt.Fprintf(os.Stderr, "claude-budget trailer: read commit message %q: %v\n", msgFile, err)
	} else if summed := strings.Join(trailer.SumDuplicates(strings.Split(string(cur), "\n"), costName), "\n"); summed != string(cur) {
		if err := os.WriteFile(msgFile, []byte(summed), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "claude-budget trailer: write summed message:", err)
		}
	}
	// Always clear the marker, even if the read/write above failed.
	return state.ClearPending(gitDir)
}

// runTrailerNormal scans this branch's not-yet-consumed usage, appends the
// configured trailer block to the message, and stages the watermark for
// post-commit to promote. It deliberately does not touch the state file
// (consume does that), so a cancelled commit leaves usage intact.
func runTrailerNormal(gitDir, msgFile string) error {
	root, err := gitutil.RepoRoot()
	if err != nil {
		return fmt.Errorf("resolve repo root: %w", err)
	}
	branch, err := gitutil.CurrentBranch()
	if err != nil {
		return fmt.Errorf("resolve branch: %w", err)
	}
	// Detached HEAD: usage records carry a real branch name, so a scan for
	// "HEAD" matches ~0 records. Skip attribution and drop any stale marker.
	if branch == "HEAD" {
		return state.ClearPending(gitDir)
	}
	rc, err := pricing.Load(pricingData)
	if err != nil {
		return fmt.Errorf("load rate card: %w", err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	st, err := state.Load(gitDir)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	projects := filepath.Join(home, ".claude", "projects")
	res, err := reader.Scan(projects, root, branch, st.HwmFor(branch), rc)
	if err != nil {
		return fmt.Errorf("scan transcripts: %w", err)
	}

	cur, err := os.ReadFile(msgFile)
	if err != nil {
		return fmt.Errorf("read commit message %q: %w", msgFile, err)
	}
	d := decideTrailer(res, cfg, branch, string(cur))
	if d.changed {
		if err := os.WriteFile(msgFile, []byte(d.newMsg), 0o644); err != nil {
			return fmt.Errorf("write commit message: %w", err)
		}
	}
	if d.stage == nil {
		return state.ClearPending(gitDir)
	}
	return state.WritePending(gitDir, *d.stage)
}

// trailerDecision is the pure outcome of the normal path for a scanned result.
type trailerDecision struct {
	newMsg  string         // commit-message content to write
	changed bool           // whether newMsg differs from the input (skip the write if false)
	stage   *state.Pending // watermark to stage, or nil to clear the marker instead
}

// decideTrailer computes the normal-path outcome purely from inputs (no I/O), so
// the routing, idempotency, and watermark logic are unit-testable without git.
// A detached HEAD or an empty/disabled trailer set both yield "change nothing,
// clear the marker".
func decideTrailer(res *reader.Result, cfg *config.Config, branch, curMsg string) trailerDecision {
	if branch == "HEAD" {
		return trailerDecision{newMsg: curMsg}
	}
	lines := trailer.Format(res, cfg)
	if len(lines) == 0 {
		return trailerDecision{newMsg: curMsg}
	}
	newMsg, changed := appendTrailerBlock(curMsg, lines)
	return trailerDecision{
		newMsg:  newMsg,
		changed: changed,
		stage: &state.Pending{
			Branch:        branch,
			HwmMs:         res.MaxTsMs,
			LastRequestID: res.MaxRequestID,
		},
	}
}

// appendTrailerBlock inserts the trailer lines as a blank-line-separated block at
// the end of the editable message body, before any trailing git comment block.
// It is idempotent: if that exact block is already the tail of the body (a re-run
// of prepare-commit-msg for the same commit, or an amend reusing the message), it
// returns the input unchanged.
func appendTrailerBlock(content string, lines []string) (string, bool) {
	block := strings.Join(lines, "\n")
	body, comments := splitTrailingComments(content)
	bodyTrim := strings.TrimRight(body, "\n")

	if bodyTrim == block || strings.HasSuffix(bodyTrim, "\n"+block) {
		return content, false // already present as the body's tail
	}

	var b strings.Builder
	b.WriteString(bodyTrim)
	if bodyTrim != "" {
		b.WriteString("\n\n")
	}
	b.WriteString(block)
	if comments != "" {
		b.WriteString("\n\n")
		b.WriteString(comments)
	} else {
		b.WriteString("\n")
	}
	return b.String(), true
}

// splitTrailingComments separates the editable body from the trailing block of
// git-generated comment lines. The comment block is the suffix of the content
// beginning at a '#' line (preceded by a blank line, per git's template) where
// every following line is blank or another '#' comment — matching git's default
// editor template. Verbose commits ('commit -v' / commit.verbose=true) also
// append a scissors cut line followed by the raw diff; that whole block is
// non-body too, so the comment suffix starts at it and the trailer is inserted
// above it. Without this, the trailer would be appended at the very end, below
// the cut line, and git discards everything from the cut line down — silently
// dropping the trailer.
func splitTrailingComments(content string) (body, comments string) {
	lines := strings.Split(content, "\n")
	start := -1
	for i := range lines {
		if !strings.HasPrefix(lines[i], "#") {
			continue
		}
		// git always emits a blank line before its template comment block, so a
		// '#' line that directly follows body text is the user's content (e.g. a
		// trailing "#hashtag" note), not a comment — don't swallow it.
		if i > 0 && lines[i-1] != "" {
			continue
		}
		allCommentOrBlank := true
		for j := i; j < len(lines); j++ {
			// The scissors cut line begins git's verbose diff block: the comment
			// block, the cut line, and the raw diff below it are all non-body, so
			// treat the suffix as a comment block even though the diff lines that
			// follow aren't '#' comments.
			if isScissorsLine(lines[j]) {
				break
			}
			if lines[j] != "" && !strings.HasPrefix(lines[j], "#") {
				allCommentOrBlank = false
				break
			}
		}
		if allCommentOrBlank {
			start = i
			break
		}
	}
	if start < 0 {
		return content, ""
	}
	return strings.Join(lines[:start], "\n"), strings.Join(lines[start:], "\n")
}

// isScissorsLine reports whether line is git's verbose/scissors cut line, e.g.
// "# ------------------------ >8 ------------------------". git emits it before
// the diff under commit verbose mode and discards everything from it downward,
// so any trailer must be placed above it rather than appended at the end.
func isScissorsLine(line string) bool {
	return strings.HasPrefix(line, "#") && strings.Contains(line, ">8")
}

// --- consume command (post-commit brain) -------------------------------------

// runConsume is the post-commit entry point. Like trailer, it must never block:
// any internal failure is logged to stderr and we still report success (exit 0).
func runConsume() error {
	if err := consumeMain(); err != nil {
		fmt.Fprintln(os.Stderr, "claude-budget consume:", err)
	}
	return nil
}

func consumeMain() error {
	gitDir, err := gitutil.GitDir()
	if err != nil {
		return fmt.Errorf("resolve git dir (not in a git repo?): %w", err)
	}
	return consume(gitDir, gitutil.RebaseInProgress())
}

// consume promotes the watermark that prepare-commit-msg staged in the pending
// marker into per-branch state, then clears the marker.
//
// Order matters: the rebase check comes before reading the marker, and the
// rebase path never reads or clears it. A `pick` step fires post-commit during a
// rebase; clearing the marker there would swallow usage destined for the next
// real commit. A cancelled commit never reaches post-commit at all, so its
// staged marker simply waits for the next successful commit.
//
// gitDir and rebasing are passed in so the promotion logic is unit-testable
// without driving a real git rebase.
func consume(gitDir string, rebasing bool) error {
	if rebasing {
		return nil // marker survives, untouched, to the next real commit
	}
	pending, ok, err := state.ReadPending(gitDir)
	if err != nil {
		return fmt.Errorf("read pending marker: %w", err)
	}
	if !ok {
		return nil // nothing staged (e.g. a cancelled or trailer-less commit)
	}
	st, err := state.Load(gitDir)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	st.SetBranch(pending.Branch, state.Branch{HwmMs: pending.HwmMs, LastRequestID: pending.LastRequestID})
	if err := st.Save(gitDir); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return state.ClearPending(gitDir)
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
  claude-budget trailer <msgfile> --source <s>   append cost trailers (prepare-commit-msg)
  claude-budget consume             promote the staged watermark (post-commit)
  claude-budget price       smoke-test: load the rate card and price a sample
  claude-budget version     print version
`)
}
