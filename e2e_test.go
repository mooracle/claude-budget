package main

// End-to-end tests that drive the real prepare-commit-msg/post-commit write path
// through actual `git` commands. They build the claude-budget binary once, put it
// on PATH so the installed shims resolve it by name, and exercise each routing
// path (normal append, cancelled commit, rebase guard, squash sum, amend) against
// throwaway repos with a synthetic ~/.claude/projects transcript tree.
//
// The whole suite is gated on a working `git` (TestMain builds the binary only
// when git is present; each case skips via newE2ERepo otherwise) — matching
// tokentrack's harness, no build tag.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooracle/claude-budget/internal/state"
)

// binDir holds the freshly built claude-budget binary; binDir is prepended to the
// scrubbed PATH so the shims' `command -v claude-budget` resolves it. Both are
// empty when git is unavailable, in which case the e2e cases skip.
var (
	binDir  string
	binPath string
)

// fixed, strictly-increasing record timestamps; mustMs parses them to UnixMilli.
const (
	e2eTs1 = "2026-06-14T10:00:00Z"
	e2eTs2 = "2026-06-14T11:00:00Z"
	e2eTs3 = "2026-06-14T12:00:00Z"
)

// fixedMtime is stamped on every seeded transcript file. It sits far in the future
// so the reader's per-file mtime prune (modtime <= hwm) never drops the file; the
// per-record timestamp cutoff alone then decides what's new. This keeps the
// multi-commit fixtures (squash) simple: each commit's scan sees exactly the
// records added after the prior commit's watermark.
var fixedMtime = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

// seq-editor script: rewrite the 2nd todo line's `pick` to `squash` so `rebase -i`
// folds the last commit into the previous one non-interactively.
const seqEditorScript = `#!/bin/sh
awk 'NR==2 && $1=="pick"{sub(/^pick/,"squash")} {print}' "$1" > "$1.tmp" && mv "$1.tmp" "$1"
`

func TestMain(m *testing.M) {
	if !gitAvailable() {
		// e2e cases self-skip; the package's non-e2e unit tests still run.
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "claude-budget-e2e-bin")
	if err != nil {
		panic("e2e: mktemp: " + err.Error())
	}
	binDir = dir
	binPath = filepath.Join(dir, "claude-budget")
	if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
		panic("e2e: go build: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func gitAvailable() bool { return exec.Command("git", "--version").Run() == nil }

// --- harness ----------------------------------------------------------------

type e2eRepo struct {
	t       *testing.T
	root    string // worktree top-level as git reports it (symlinks resolved)
	gitDir  string
	home    string // fake HOME; transcripts live under home/.claude/projects
	session string // the seeded JSONL path (appended across commits)
	env     []string
}

func newE2ERepo(t *testing.T) *e2eRepo {
	t.Helper()
	if binPath == "" {
		t.Skip("git not available — skipping e2e write-path suite")
	}
	work := t.TempDir()
	home := t.TempDir()
	r := &e2eRepo{t: t, home: home}
	r.env = scrubbedEnv(home)
	r.run(work, "git", "init", "-q", "-b", "main")
	// Resolve the canonical worktree root + git dir. On macOS the temp dir reached
	// via a symlinked path, so what git reports differs from t.TempDir(); the
	// fixture cwd must equal what git reports or reader's membership check fails.
	r.root = r.gitOut(work, "rev-parse", "--show-toplevel")
	r.gitDir = r.gitOut(r.root, "rev-parse", "--absolute-git-dir")

	projDir := filepath.Join(home, ".claude", "projects", "repo")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir projects: %v", err)
	}
	r.session = filepath.Join(projDir, "session.jsonl")

	// Install the hook pair via the real `setup` command.
	r.run(r.root, binPath, "setup")
	return r
}

// scrubbedEnv builds a hermetic environment: no global/system git config, an
// explicit identity, a no-op editor, the fake HOME, and binDir on PATH so the
// shims find the binary. GIT_DIR/GIT_WORK_TREE are absent by construction.
func scrubbedEnv(home string) []string {
	return []string{
		"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_EDITOR=true",
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
	}
}

// run executes a command in dir under the scrubbed env, failing on a non-zero exit.
func (r *e2eRepo) run(dir, name string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitOut runs git capturing only stdout, so callers can parse a clean value.
func (r *e2eRepo) gitOut(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = r.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

func (r *e2eRepo) headMessage() string { return r.gitOut(r.root, "log", "-1", "--format=%B") }

func (r *e2eRepo) writeFile(name, content string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.root, name), []byte(content), 0o644); err != nil {
		r.t.Fatalf("write %s: %v", name, err)
	}
}

func (r *e2eRepo) commit(msg, file, content string) {
	r.t.Helper()
	r.writeFile(file, content)
	r.run(r.root, "git", "add", file)
	r.run(r.root, "git", "commit", "-q", "-m", msg)
}

func (r *e2eRepo) state() *state.State {
	r.t.Helper()
	st, err := state.Load(r.gitDir)
	if err != nil {
		r.t.Fatalf("load state: %v", err)
	}
	return st
}

func (r *e2eRepo) pendingExists() bool {
	r.t.Helper()
	_, ok, err := state.ReadPending(r.gitDir)
	if err != nil {
		r.t.Fatalf("read pending: %v", err)
	}
	return ok
}

func (r *e2eRepo) writeScript(name, body string) string {
	r.t.Helper()
	p := filepath.Join(r.home, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		r.t.Fatalf("write script %s: %v", name, err)
	}
	return p
}

// usageRec is one synthetic transcript record's salient fields.
type usageRec struct {
	ts, reqID, model string
	input, output    int64
}

// seed appends records to the session transcript and stamps a future mtime.
func (r *e2eRepo) seed(branch string, recs ...usageRec) {
	r.t.Helper()
	var b strings.Builder
	if existing, err := os.ReadFile(r.session); err == nil && len(existing) > 0 {
		b.Write(existing)
		if !strings.HasSuffix(string(existing), "\n") {
			b.WriteByte('\n')
		}
	}
	for _, rec := range recs {
		b.WriteString(r.recJSON(branch, rec))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(r.session, []byte(b.String()), 0o644); err != nil {
		r.t.Fatalf("write session: %v", err)
	}
	if err := os.Chtimes(r.session, fixedMtime, fixedMtime); err != nil {
		r.t.Fatalf("chtimes session: %v", err)
	}
}

func (r *e2eRepo) recJSON(branch string, rec usageRec) string {
	r.t.Helper()
	m := map[string]any{
		"timestamp": rec.ts,
		"cwd":       r.root,
		"gitBranch": branch,
		"requestId": rec.reqID,
		"message": map[string]any{
			"id":    rec.reqID + "-msg",
			"model": rec.model,
			"usage": map[string]any{
				"input_tokens":  rec.input,
				"output_tokens": rec.output,
			},
		},
	}
	b, err := json.Marshal(m)
	if err != nil {
		r.t.Fatalf("marshal record: %v", err)
	}
	return string(b)
}

func mustMs(t *testing.T, s string) int64 {
	t.Helper()
	tm, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm.UnixMilli()
}

// withEnv returns a copy of base with the given key=value overrides applied.
func withEnv(base []string, kv ...string) []string {
	out := append([]string{}, base...)
	for i := 0; i+1 < len(kv); i += 2 {
		out = setEnv(out, kv[i], kv[i+1])
	}
	return out
}

func setEnv(env []string, key, val string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + val
			return env
		}
	}
	return append(env, prefix+val)
}

// --- cases ------------------------------------------------------------------

// A real commit appends the cost trailer, the post-commit promotes the watermark,
// and the pending marker is cleared.
func TestE2E_CommitAppendsTrailerAndAdvancesWatermark(t *testing.T) {
	r := newE2ERepo(t)
	r.seed("main", usageRec{e2eTs1, "r1", "claude-opus-4-8", 0, 4000}) // 4000 * $25/Mtok = $0.10
	r.commit("add feature", "f.txt", "hello\n")

	msg := r.headMessage()
	if !strings.Contains(msg, "Claude-Cost: 0.10") {
		t.Fatalf("commit message missing cost trailer:\n%s", msg)
	}
	if got, want := r.state().HwmFor("main"), mustMs(t, e2eTs1); got != want {
		t.Errorf("watermark = %d, want %d", got, want)
	}
	if r.pendingExists() {
		t.Error(".pending should be cleared after a successful commit")
	}
}

// A cancelled commit (editor fails before completion) never reaches post-commit,
// so the staged watermark survives and the consumed baseline does not advance.
func TestE2E_CancelledCommitPreservesPending(t *testing.T) {
	r := newE2ERepo(t)
	r.seed("main", usageRec{e2eTs1, "r1", "claude-opus-4-8", 0, 4000})
	r.writeFile("f.txt", "hello\n")
	r.run(r.root, "git", "add", "f.txt")

	// No -m, and a failing editor: prepare-commit-msg runs and stages the pending
	// watermark, then the editor (`false`) fails so git aborts before post-commit.
	cmd := exec.Command("git", "commit")
	cmd.Dir = r.root
	cmd.Env = withEnv(r.env, "GIT_EDITOR", "false")
	if err := cmd.Run(); err == nil {
		t.Fatal("commit should have aborted with GIT_EDITOR=false")
	}

	if !r.pendingExists() {
		t.Error("the staged watermark must survive a cancelled commit")
	}
	if got := r.state().HwmFor("main"); got != 0 {
		t.Errorf("watermark advanced to %d on a cancelled commit, want 0", got)
	}
}

// During a rebase, a picked commit fires post-commit; consume must skip — leaving
// the pending marker unread and unchanged and the state untouched — so usage
// destined for the next real commit isn't swallowed.
func TestE2E_RebaseInProgressSkipsConsume(t *testing.T) {
	r := newE2ERepo(t)
	pend := state.Pending{Branch: "main", HwmMs: 4242, LastRequestID: "r-pending"}
	if err := state.WritePending(r.gitDir, pend); err != nil {
		t.Fatalf("WritePending: %v", err)
	}
	rebaseDir := filepath.Join(r.gitDir, "rebase-merge")
	if err := os.MkdirAll(rebaseDir, 0o755); err != nil {
		t.Fatalf("mkdir rebase-merge: %v", err)
	}

	r.run(r.root, binPath, "consume")

	got, ok, err := state.ReadPending(r.gitDir)
	if err != nil || !ok {
		t.Fatalf("pending should survive a rebase, got ok=%v err=%v", ok, err)
	}
	if got != pend {
		t.Fatalf("pending mutated during rebase: %+v, want %+v", got, pend)
	}
	if got := r.state().HwmFor("main"); got != 0 {
		t.Errorf("state advanced to %d during a rebase, want 0", got)
	}
}

// A squash combines messages, each carrying its own cost trailer; the sum path
// folds the duplicate cost lines into one and leaves the watermark untouched
// (consume is skipped for the whole rebase).
func TestE2E_SquashSumsCostTrailers(t *testing.T) {
	r := newE2ERepo(t)
	// Three commits; each scan sees exactly one new record (later ts than the
	// prior watermark) and attributes its own cost trailer.
	r.seed("main", usageRec{e2eTs1, "r1", "claude-opus-4-8", 0, 4000}) // 0.10
	r.commit("c1", "f.txt", "1\n")
	r.seed("main", usageRec{e2eTs2, "r2", "claude-opus-4-8", 0, 8000}) // 0.20
	r.commit("c2", "f.txt", "1\n2\n")
	r.seed("main", usageRec{e2eTs3, "r3", "claude-opus-4-8", 0, 12000}) // 0.30
	r.commit("c3", "f.txt", "1\n2\n3\n")

	before := r.state().HwmFor("main")
	if want := mustMs(t, e2eTs3); before != want {
		t.Fatalf("pre-rebase watermark = %d, want %d", before, want)
	}

	// Squash c3 into c2: the combined message carries cost trailers 0.20 and 0.30.
	seq := r.writeScript("seq-editor.sh", seqEditorScript)
	cmd := exec.Command("git", "rebase", "-i", "HEAD~2")
	cmd.Dir = r.root
	cmd.Env = withEnv(r.env, "GIT_SEQUENCE_EDITOR", seq)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rebase -i squash: %v\n%s", err, out)
	}

	msg := r.headMessage()
	if n := strings.Count(msg, "Claude-Cost:"); n != 1 {
		t.Fatalf("expected exactly one summed cost trailer, got %d:\n%s", n, msg)
	}
	if !strings.Contains(msg, "Claude-Cost: 0.50") {
		t.Fatalf("summed cost trailer wrong/missing (want 0.50):\n%s", msg)
	}
	if after := r.state().HwmFor("main"); after != before {
		t.Errorf("watermark changed across rebase: %d -> %d", before, after)
	}
}

// The same squash fold, but with a [format.rename] cost = "AI-Cost" config: the
// trailers are written and summed under the renamed name, proving runTrailerSum
// derives the cost-trailer name from config rather than hard-coding "Claude-Cost".
func TestE2E_SquashSumsRenamedCostTrailers(t *testing.T) {
	r := newE2ERepo(t)
	r.writeFile(".claude-budget.toml", "[format.rename]\ncost = \"AI-Cost\"\n")
	r.seed("main", usageRec{e2eTs1, "r1", "claude-opus-4-8", 0, 4000}) // 0.10
	r.commit("c1", "f.txt", "1\n")
	r.seed("main", usageRec{e2eTs2, "r2", "claude-opus-4-8", 0, 8000}) // 0.20
	r.commit("c2", "f.txt", "1\n2\n")
	r.seed("main", usageRec{e2eTs3, "r3", "claude-opus-4-8", 0, 12000}) // 0.30
	r.commit("c3", "f.txt", "1\n2\n3\n")

	// Each commit must have rendered the renamed trailer, not the default.
	if msg := r.headMessage(); !strings.Contains(msg, "AI-Cost: 0.30") {
		t.Fatalf("pre-squash commit missing renamed trailer:\n%s", msg)
	}

	seq := r.writeScript("seq-editor.sh", seqEditorScript)
	cmd := exec.Command("git", "rebase", "-i", "HEAD~2")
	cmd.Dir = r.root
	cmd.Env = withEnv(r.env, "GIT_SEQUENCE_EDITOR", seq)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rebase -i squash: %v\n%s", err, out)
	}

	msg := r.headMessage()
	if n := strings.Count(msg, "AI-Cost:"); n != 1 {
		t.Fatalf("expected exactly one summed renamed cost trailer, got %d:\n%s", n, msg)
	}
	if !strings.Contains(msg, "AI-Cost: 0.50") {
		t.Fatalf("summed renamed cost trailer wrong/missing (want 0.50):\n%s", msg)
	}
}

// `git commit --amend` reuses the existing message (source "commit" -> clear path);
// the existing cost trailer is preserved, not duplicated, and the watermark holds.
func TestE2E_AmendDoesNotDuplicateTrailer(t *testing.T) {
	r := newE2ERepo(t)
	r.seed("main", usageRec{e2eTs1, "r1", "claude-opus-4-8", 0, 4000}) // 0.10
	r.commit("feat", "f.txt", "hello\n")
	before := r.state().HwmFor("main")

	// GIT_EDITOR=true (from the scrubbed env) makes the amend's editor a no-op; the
	// existing message — already carrying one trailer — is reused as-is.
	r.run(r.root, "git", "commit", "--amend")

	msg := r.headMessage()
	if n := strings.Count(msg, "Claude-Cost:"); n != 1 {
		t.Fatalf("amend duplicated the cost trailer (got %d):\n%s", n, msg)
	}
	if after := r.state().HwmFor("main"); after != before {
		t.Errorf("amend changed the watermark: %d -> %d", before, after)
	}
	if r.pendingExists() {
		t.Error("amend should leave no pending marker")
	}
}

// Re-running the trailer command for the same commit (no consume between, so the
// watermark hasn't advanced) reproduces the same block and must not append it a
// second time — the idempotency rule, exercised through the real binary + reader.
func TestE2E_TrailerRerunIsIdempotent(t *testing.T) {
	r := newE2ERepo(t)
	r.seed("main", usageRec{e2eTs1, "r1", "claude-opus-4-8", 0, 4000}) // 0.10
	msgFile := filepath.Join(r.gitDir, "MSG_TEST")
	if err := os.WriteFile(msgFile, []byte("subject\n"), 0o644); err != nil {
		t.Fatalf("write msg: %v", err)
	}

	r.run(r.root, binPath, "trailer", msgFile, "--source", "")
	r.run(r.root, binPath, "trailer", msgFile, "--source", "")

	got, err := os.ReadFile(msgFile)
	if err != nil {
		t.Fatalf("read msg: %v", err)
	}
	if n := strings.Count(string(got), "Claude-Cost:"); n != 1 {
		t.Fatalf("trailer re-run duplicated the block (got %d):\n%s", n, got)
	}
	if !strings.Contains(string(got), "Claude-Cost: 0.10") {
		t.Fatalf("expected the cost trailer to be present:\n%s", got)
	}
	// The idempotent (no-change) re-run must still stage the watermark, so a
	// later post-commit promotes it rather than dropping the usage.
	pend, ok, err := state.ReadPending(r.gitDir)
	if err != nil || !ok {
		t.Fatalf("idempotent re-run should still stage the pending marker (ok=%v err=%v)", ok, err)
	}
	if want := mustMs(t, e2eTs1); pend.HwmMs != want {
		t.Errorf("staged watermark = %d, want %d", pend.HwmMs, want)
	}
}

// The clear path (merge / cherry-pick / -c/-C message reuse — git source "merge"
// or "commit") attaches no trailer and drops any staged pending marker, so usage
// isn't mis-attributed to a message-reuse commit. Driven through the real binary
// with a pre-seeded marker so ClearPending is genuinely exercised (the amend case
// has no marker staged, so it can't prove the clear actually clears something).
func TestE2E_ClearSourceDropsPendingMarker(t *testing.T) {
	r := newE2ERepo(t)
	if err := state.WritePending(r.gitDir, state.Pending{Branch: "main", HwmMs: 4242, LastRequestID: "r-stale"}); err != nil {
		t.Fatalf("WritePending: %v", err)
	}
	r.seed("main", usageRec{e2eTs1, "r1", "claude-opus-4-8", 0, 4000})
	msgFile := filepath.Join(r.gitDir, "MSG_CLEAR")
	if err := os.WriteFile(msgFile, []byte("merge branch foo\n"), 0o644); err != nil {
		t.Fatalf("write msg: %v", err)
	}

	r.run(r.root, binPath, "trailer", msgFile, "--source", "merge")

	if r.pendingExists() {
		t.Error("clear path must drop the staged pending marker")
	}
	got, err := os.ReadFile(msgFile)
	if err != nil {
		t.Fatalf("read msg: %v", err)
	}
	if strings.Contains(string(got), "Claude-Cost:") {
		t.Errorf("clear path must not append a trailer:\n%s", got)
	}
}

// Verbose commits ('commit -v' / commit.verbose=true) hand prepare-commit-msg a
// message that ends with a scissors cut line and the raw diff. The trailer must
// land above that cut line; appended at the very end it sits below the cut line,
// which git discards — silently dropping the trailer. Driven through the real
// binary with a faithfully-shaped verbose template.
func TestE2E_VerboseScissorsTrailerSurvives(t *testing.T) {
	r := newE2ERepo(t)
	r.seed("main", usageRec{e2eTs1, "r1", "claude-opus-4-8", 0, 4000}) // 0.10
	msgFile := filepath.Join(r.gitDir, "MSG_VERBOSE")
	verbose := "subject\n\n" +
		"# Please enter the commit message for your changes. Lines starting\n" +
		"# with '#' will be ignored, and an empty message aborts the commit.\n" +
		"#\n" +
		"# On branch main\n" +
		"# ------------------------ >8 ------------------------\n" +
		"# Do not modify or remove the line above.\n" +
		"# Everything below it will be ignored.\n" +
		"diff --git a/f.txt b/f.txt\n" +
		"index 0000000..1111111 100644\n" +
		"--- a/f.txt\n+++ b/f.txt\n@@ -1 +1 @@\n-hello\n+change\n"
	if err := os.WriteFile(msgFile, []byte(verbose), 0o644); err != nil {
		t.Fatalf("write msg: %v", err)
	}

	r.run(r.root, binPath, "trailer", msgFile, "--source", "")

	got, err := os.ReadFile(msgFile)
	if err != nil {
		t.Fatalf("read msg: %v", err)
	}
	scissors := strings.Index(string(got), ">8")
	trailer := strings.Index(string(got), "Claude-Cost: 0.10")
	if trailer < 0 {
		t.Fatalf("cost trailer missing from verbose message:\n%s", got)
	}
	if scissors >= 0 && trailer > scissors {
		t.Fatalf("trailer is below the scissors cut line (git would discard it):\n%s", got)
	}
}
