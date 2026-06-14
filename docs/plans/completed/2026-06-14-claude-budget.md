# claude-budget — implementation plan (remaining work)

## Overview

`claude-budget` is a standalone Go binary that tracks GitHub Claude Code token
usage per commit and appends measured-cost git trailers (e.g. `Claude-Cost: 0.42`)
to commit messages. Per-commit, branch-scoped attribution sourced from Claude
Code's session transcripts; no daemon (the binary is the brain, two thin shell
hooks call it); rate card embedded at build time.

This plan covers the **remaining** work: the `trailer`/`consume` write path,
config parsing, the test suite, and distribution. The read path is done.

> Revised 2026-06-14 after a plan-review pass: explicit hook source-routing table
> (the thin shims push all routing into the binary), rename-aware + pure
> `SumDuplicates`, consume rebase-guard ordering, detached-HEAD handling, and an
> amend e2e case.

## Already built & validated (no further work)

- **`internal/pricing`** — embedded `data/claude-pricing.json`; `CostUSD(model, Usage)`; model normalize; unknown→0.
- **`internal/reader`** — scan `~/.claude/projects/*` → mtime prune → cwd membership → `gitBranch` + high-water-mark filter → `requestId` dedup (max-output) → per-model aggregate + price. Validated against real transcripts ($49.61 / 56.8M tokens / 420 reqs on tokentrack@main; reconciled with an independent recount).
- **`internal/gitutil`** — repo root, git dir, branch (handles unborn + detached HEAD), hooks dir (honors `core.hooksPath`), rebase-in-progress check.
- **`internal/hook`** — collision-safe install/uninstall of the `prepare-commit-msg` + `post-commit` shims (marker-guarded, validate-all-before-write).
- **`internal/state`** — `Load`/`HwmFor` (read path), `Save`/`SetBranch`, and `Pending` read/write/clear (`<gitDir>/claude-budget` + `.pending`).
- **`main.go`** — `setup`, `status`, `uninstall`, `price`, `version` working.

## Context (from discovery)

- Transcript fields used: `cwd`, `gitBranch` (100% of usage records), `message.model`, `message.usage` (5 disjoint buckets incl. 5m/1h cache tiers), `timestamp`, `requestId` (dedup; ~43% of records are streaming dupes).
- Hook shims (`hooks/prepare-commit-msg`, `hooks/post-commit`) already call `claude-budget trailer "$1" --source "${2:-}"` and `claude-budget consume`; both currently no-op (stub returns 1 under `|| true`).
- State + pending types already exist — the write path wires them, no new state design needed.
- Trailer convention: **bare numbers**, no `$`/suffix; unit conveyed by the trailer name.
- Sibling reference for hook edge-case behavior (rebase/squash/cancel): tokentrack `commitHook.ts` + `hook-git-e2e.test.ts`.

## Development Approach

- **Testing approach: regular** (implement, then tests, within the same task).
- **Every task includes its own tests as separate checklist items** — Go table tests for pure logic; the hook write path gets a real `git` e2e task.
- Complete each task fully (code + tests green) before the next.
- Test command: `go test ./...`. Build/vet gate: `go vet ./... && go build ./...`.
- Keep `go build ./...` green at every task boundary (no half-compiling stubs).
- Update this plan's checkboxes as work lands; `➕` for new tasks, `⚠️` for blockers.

## Testing Strategy

- **Unit** (required per task): pure functions (config parse, trailer formatting), and backfill for already-built packages (pricing, reader, state, hook).
- **Reader fixtures**: synthesize a temp `~/.claude/projects` tree of real-shaped JSONL — cover dedup by `requestId`, branch filter, hwm cutoff, cwd membership (incl. subdir), mtime prune.
- **git e2e** (its own task): throwaway repos via `os.MkdirTemp`, gated on `git --version`, scrubbed env (`GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_SYSTEM=/dev/null`, `GIT_EDITOR=true`, explicit author/committer, scrub `GIT_DIR`/`GIT_WORK_TREE`). Mirrors tokentrack's harness. Cases: real commit appends trailer + advances watermark; cancelled commit preserves counter; rebase guard; squash summing.

## Progress Tracking

- Mark `[x]` immediately on completion. Add `➕`/`⚠️` as needed. Keep in sync with actual work.

## What Goes Where

- **Implementation Steps** (`[ ]`): code, tests, docs inside this repo.
- **Post-Completion** (no checkboxes): Homebrew tap publish, GitHub release verification, and the deferred open questions.

## Implementation Steps

### Task 1: Config loader (`.claude-budget.toml`)

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Modify: `go.mod` (add `github.com/BurntSushi/toml`)
- Modify: `main.go` (load config in `status`; add the "would attach" line)

- [x] define `Config` struct: `Trailers{Cost,CostModels,Tokens,TokensModels,Interactions bool}`, `Format{CostPrecision int, Rename map[string]string}`
- [x] `Load(repoRoot string) (*Config, error)` — read `<repoRoot>/.claude-budget.toml`; return defaults (Cost=true, CostPrecision=2, all else off) when the file is absent
- [x] `Defaults()` helper; missing keys fall back to defaults (don't zero-out)
- [x] add `BurntSushi/toml` require; `go mod tidy`
- [x] wire into `status`: print enabled trailer config keys (the rendered "would attach: <names>" line reuses Task 2's formatter — add it after Task 2 to avoid duplicating name logic)
- [x] write tests: full file parse, absent file → defaults, partial file → defaults for missing keys, rename + precision
- [x] run `go test ./...` — must pass before next task

### Task 2: Trailer formatter (pure)

**Files:**
- Create: `internal/trailer/trailer.go`
- Create: `internal/trailer/trailer_test.go`

- [x] `Format(res *reader.Result, cfg *config.Config) []string` returning trailer lines `"Name: value"` (bare numbers, no `$`/suffix)
- [x] `Claude-Cost` (cost at `CostPrecision`); `Claude-Cost-Models` (`model=val,…`); `Claude-Tokens`; `Claude-Tokens-Models`; `Claude-Interactions` — each gated by config
- [x] apply `Format.Rename` to trailer keys (e.g. `cost → AI-Cost`)
- [x] return empty slice when `res.Requests == 0` (nothing to attach)
- [x] write tests: each trailer type on/off, precision rounding, rename, multi-model ordering (stable), empty result
- [x] run `go test ./...` — must pass before next task

### Task 3: `trailer` command — source routing + normal path (prepare-commit-msg brain)

**Files:**
- Modify: `main.go` (implement `runTrailer`)

The shim is thin (`claude-budget trailer "$1" --source "${2:-}"`), so the **binary owns
all source routing** tokentrack does in shell (`commitHook.ts` lines 63-92). Route on
`--source` ($2) + rebase state:

| `--source` ($2) | rebase in progress? | action |
|---|---|---|
| empty / `template` | no | normal: scan → append → stage pending |
| `message` | no | normal: scan → append → stage pending |
| `message` | yes (`git rebase -i` reword) | **sum path** (Task 4) |
| `merge` | — | ClearPending, exit 0 (no trailer) |
| `commit` | — | ClearPending, exit 0 (cherry-pick / `-c` / `-C` reuse) |
| `squash` | — | **sum path** (Task 4) |
| any | yes | **sum path** (Task 4) — rebase guard wins |

- [x] parse args: `trailer <msgfile> --source <s>`
- [x] route per the table: rebase (`gitutil.RebaseInProgress`) or `squash` → Task 4; `merge`/`commit` → ClearPending + exit 0; else normal
- [x] normal path: resolve repoRoot/gitDir/branch; load rate card, config, state; `reader.Scan` since `state.HwmFor(branch)`
- [x] `branch == "HEAD"` (detached) → scan matches ~0 records (records carry a real branch) → ClearPending, exit 0 (no attribution; finalize policy in Task 10)
- [x] if trailers non-empty: append a blank-line-separated trailer block. Idempotency: if the exact block this run would append is already the file's tail (re-run of the same commit), skip; else append
- [x] stage `state.Pending{Branch, HwmMs: res.MaxTsMs, LastRequestID: res.MaxRequestID}` via `WritePending` — **do not** touch the state file (deferred consume)
- [x] empty result / trailers disabled → ClearPending, exit 0
- [x] never error-out the hook: log to stderr, exit 0 on any internal failure
- [x] write tests: routing table (each `$2` × rebase state → correct path); normal append; idempotent re-run; detached HEAD → no trailer; pending staged with correct watermark; `merge`/`commit`/empty-result clear the marker
- [x] run `go test ./...` — must pass before next task

> `git commit --amend` arrives as a normal source and re-scans/re-appends — covered by the amend e2e case in Task 7; if problematic, defer via Open Questions.

### Task 4: `trailer` command — rebase/squash summing path

**Files:**
- Modify: `main.go` (extend `runTrailer` — read msgfile, call SumDuplicates, write back)
- Create: `internal/trailer/sum.go`
- Modify: `internal/trailer/trailer_test.go`

- [x] keep `internal/trailer` pure: `SumDuplicates(lines []string, trailerName string) []string` (lines in, lines out); file read/write stays in `main.go`
- [x] sum duplicate **bare-number** total-cost trailer lines into one; the trailer name is **config-derived** (`config.Format.Rename["cost"]`, default `Claude-Cost`) — NOT hard-coded. (tokentrack hard-codes `Copilot-AI-Credits`, but our cost trailer is renameable, so a hard-coded match would silently break summing for any team using `[format.rename]`.)
- [x] leave non-numeric lines untouched (`-Models` aggregates, any renamed non-cost trailer)
- [x] summing path must **not** consult or modify state, and must `ClearPending` (usage carries forward to the next normal commit)
- [x] write tests: two `Claude-Cost` lines → one summed; **rename `cost`→`AI-Cost` then two `AI-Cost` lines → one summed**; non-numeric/`-Models` lines untouched; `SumDuplicates` is pure (no file I/O)
- [x] run `go test ./...` — must pass before next task

### Task 5: `consume` command (post-commit brain)

**Files:**
- Modify: `main.go` (implement `runConsume`)

Order matters: check rebase **before** reading the marker, and **never clear the marker
on the rebase path** — a `pick` step fires post-commit, and clearing there would swallow
usage destined for the next real commit.

- [x] if `gitutil.RebaseInProgress()` → exit 0 (do NOT read or clear the marker)
- [x] `ReadPending(gitDir)`; if absent → exit 0
- [x] else promote: `state.SetBranch(pending.Branch, {HwmMs, LastRequestID})`, `state.Save`, then `ClearPending`
- [x] never error-out; log + exit 0 on failure
- [x] write tests: pending present (no rebase) → state advanced + marker cleared; no pending → no-op; **rebase in progress → marker retained AND not read, state unchanged**
- [x] run `go test ./...` — must pass before next task

### Task 6: Backfill unit tests for already-built packages

**Files:**
- Create: `internal/pricing/pricing_test.go`
- Create: `internal/reader/reader_test.go`
- Create: `internal/state/state_test.go`
- Create: `internal/hook/hook_test.go`

- [x] pricing: table tests per model × 5 buckets; unknown→0; `Normalize` prefix stripping
- [x] reader: synthesize a temp projects tree (real-shaped JSONL); assert dedup by `requestId`, branch filter, hwm cutoff, cwd membership incl. subdir, mtime prune, `MaxTsMs` watermark
- [x] state: round-trip `Save`/`Load`; per-branch isolation; pending write/read/clear; atomic-write leaves no `.tmp`
- [x] hook: install writes both 0755 + marker; uninstall removes only marked; third-party collision aborts without partial write; idempotent reinstall
- [x] (sequencing) the `state` + `reader` backfill may run **before** Task 3 to de-risk the write path, which leans on both; the rest can stay here
- [x] run `go test ./...` — must pass before next task

### Task 7: git e2e suite (hook write path)

**Files:**
- Create: `e2e_test.go` (package `main`, runtime `gitAvailable()` gate + `t.Skip` — match tokentrack; no build tag)

- [x] helper: create throwaway repo (`os.MkdirTemp`), scrubbed env, install hooks pointing at the freshly built binary, seed a synthetic `~/.claude/projects` (via `HOME` override) with branch-labeled usage
- [x] case: real commit → message carries `Claude-Cost` trailer AND `<gitDir>/claude-budget` advanced; `.pending` gone
- [x] case: cancelled commit (empty msg / `GIT_EDITOR=false`) → no post-commit → state unchanged, usage still pending
- [x] case: rebase in progress → consume skipped, watermark not advanced
- [x] case: squash path → duplicate `Claude-Cost` lines summed; state untouched
- [x] case: `git commit --amend` → trailer reflects the re-scan; no duplicate trailer block (idempotency rule from Task 3). Note: a bare `--amend` arrives as source `commit` (clear path), so the existing single trailer is reused, not duplicated; the re-scan/idempotency rule itself is covered by the trailer re-run case + `main_test.go`.
- [x] gate on `git --version` (skip + log if absent); run `go test ./...`

### Task 8: Distribution (Makefile + release)

**Files:**
- Create: `Makefile`
- Create: `scripts/update-rates.sh`
- Create: `.github/workflows/release.yml`
- Modify: `README.md`

- [x] `Makefile`: `build`, `test` (`go test ./...`), `vet`, `update-rates` (also `check`, `build-all`, `clean`)
- [x] document a manual rate-refresh procedure (edit base input/output in `data/claude-pricing.json`, bump `version`, `go test`) — there's no upstream machine-readable source to mirror byte-for-byte like tokentrack's YAML (README "Refreshing the rate card" + script header)
- [x] (optional; defer to Post-Completion if fragile) `update-rates` script: **re-derive** cache tiers via the documented multipliers (0.1× / 1.25× / 2×), and **preserve** `note`/`source`/`version` — the upstream base-price *extraction* stays a manual edit (no machine-readable source; deferred per "if fragile"). Rate card reformatted once to the script's canonical jq output so future refreshes are clean minimal diffs.
- [x] release workflow: build `darwin/linux/windows × amd64/arm64`, attach to GitHub release (`.github/workflows/release.yml`, tag-gated release job)
- [x] README: `go install github.com/mooracle/claude-budget@latest` + Homebrew instructions
- [x] write a smoke test / CI step asserting the binary builds for each target (`GOOS`/`GOARCH` matrix `go build`) — release.yml build matrix + `make build-all` (verified: 6/6 targets compile)
- [x] run `go test ./...`

### Task 9: Verify acceptance criteria

- [x] `setup` → edit a file with Claude Code → `git commit` → message has the configured trailers; `status` then shows reduced/zero pending — commit/trailer/watermark mechanics verified by `TestE2E_CommitAppendsTrailerAndAdvancesWatermark` (real `git commit`, trailer appended, `<gitDir>/claude-budget` advanced, `.pending` cleared); the interactive Claude Code edit step is manual (not automatable)
- [x] cancelled commit leaves pending intact (manual) — verified by `TestE2E_CancelledCommitPreservesPending`
- [x] rebase/squash behave per Tasks 4–5 — verified by `TestE2E_RebaseInProgressSkipsConsume`, `TestE2E_SquashSumsCostTrailers`, `TestConsume_RebaseRetainsMarkerUntouched`
- [x] `go vet ./... && go build ./... && go test ./...` all green — ran: vet OK, build OK, all packages pass
- [x] config toggles change attached trailers as expected — verified by `TestFormat_EachTrailerIndependently`, `TestFormat_AllTrailersOn`, `TestFormat_DefaultCostOnly`

### Task 10: Docs + close out

- [x] update `README.md` (write path now live) and any new patterns
- [x] resolve or explicitly defer the Open Questions below
- [x] `mkdir -p docs/plans/completed` and move this plan there

## Technical Details

- **Watermark handoff:** `prepare-commit-msg` stages `{branch, hwmMs=res.MaxTsMs, lastRequestId=res.MaxRequestID}` in `.pending`; `post-commit` promotes it. Freezing at prepare time means usage produced while the editor is open isn't swallowed.
- **Issue-#10 parity:** consume happens only in `post-commit`; a cancelled commit never reaches it, so the counter survives.
- **Non-error hooks:** `trailer`/`consume` log and exit 0 on any internal failure — never block a commit.
- **Trailer block:** append after a blank line; on re-run of `prepare-commit-msg` for the same message, don't duplicate an already-present trailer block.
- **TOML dep:** Task 1 is the first to require network (`go mod download BurntSushi/toml`).

## Open Questions (resolved in Task 10)

- **Detached HEAD — RESOLVED: skip attribution.** Records carry a real branch name, so a `branch == "HEAD"` scan matches ~nothing; `runTrailerNormal` clears the pending marker and attaches no trailer, and `status` notes the detached state. No timestamp-only fallback — it would mis-attribute usage to whatever commit happens to land. Implemented (Task 3) and covered by tests.
- **Subscription users — RESOLVED: no default change; opt-in stays.** Defaults remain cost-only (`Claude-Cost`, precision 2). Max/Pro users who want token visibility set `tokens = true` (and optionally `tokensModels`/`interactions`) in `.claude-budget.toml`. Shipping a USD trailer by default is the right signal for the API-cost framing; a per-account "I'm on a subscription" mode is out of scope for v1.
- **Same-repo dual-window — DEFERRED (out of scope).** Last-writer-wins on `<gitDir>/claude-budget` when two windows share a git dir. Pre-existing tokentrack limitation; no locking added in v1. Revisit only if it bites in practice.
- **Trailer surface for v1 — RESOLVED: ship the full surface.** `tokens` / `tokensModels` / `interactions` + `[format.rename]` stay in the config contract. The rename-aware sum path (Task 4) is implemented and tested (`SumDuplicates` derives the cost trailer name from `Format.Rename["cost"]`), so rename earns its keep without breaking squash summing. All non-cost trailers remain off by default.

## Post-Completion

*Informational — external actions, no checkboxes.*

- Publish/refresh the Homebrew tap formula after the first tagged release.
- Verify release artifacts download + run on a clean machine per OS/arch.
- Re-run `make update-rates` whenever Anthropic changes pricing; commit the diff.
