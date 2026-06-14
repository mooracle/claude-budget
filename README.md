# claude-budget

Tracks **GitHub Claude Code** token usage per commit and appends measured-cost git
trailers (e.g. `Claude-Cost: 0.42`) to commit messages — so every commit records
the API-equivalent cost of the Claude Code activity that produced it.

It's the Claude Code counterpart to [Copilot Budget](https://github.com/mooracle/tokentrack):
same per-commit attribution idea, but sourced from Claude Code's own session
transcripts instead of an OTel database, and shipped as a single static Go binary
with no runtime dependencies.

> Status: **complete.** `setup` / `status` / `uninstall` and the full
> `trailer` / `consume` write path are live: a `git commit` now appends the
> configured trailers and advances the per-branch watermark. Pricing, the
> transcript reader, git/hook/state plumbing, `.claude-budget.toml` parsing, the
> unit suite, and a real-`git` e2e suite are all implemented and green
> (`go test ./...`). The implementation plan is archived under
> [docs/plans/completed/](docs/plans/completed/).

## How it works

Claude Code writes every turn to `~/.claude/projects/<encoded-cwd>/<session>.jsonl`.
Each record already carries `cwd`, `gitBranch`, `message.model`, `message.usage`
(input / output / cache-read / cache-creation, with 5m vs 1h cache tiers split),
`timestamp`, and `requestId` — **no enable step required**. That gives clean
per-branch attribution with zero "which session?" guesswork: a feature is a
branch, and every session that touched it is self-labeled.

There is no daemon. The binary is the brain, invoked by two thin shell hooks:

```
git commit
  └─ prepare-commit-msg → claude-budget trailer "$1" --source "$2"
       · scan ~/.claude/projects/* for cwd under this repo
       · keep records: gitBranch == <current> AND timestamp > branch high-water mark
       · dedup by requestId (~43% of records are streaming duplicates)
       · sum the 5 token buckets per model, price via embedded rate card
       · append trailers, stage watermark in .git/claude-budget.pending
  └─ post-commit       → claude-budget consume
       · promote the staged watermark into .git/claude-budget (unless rebasing)
```

The shims are deliberately thin — they forward git's message file and `$2`
source hint to the binary, which owns all routing. A cancelled commit never
reaches `post-commit`, so its usage carries forward — the same
deferred-truncation guarantee as Copilot Budget (issue #10).

### Rebase, squash & amend

The `trailer` command routes on git's `$2` source hint plus rebase state, so
history rewrites don't double-count or lose usage:

- **merge / cherry-pick / `-c`/`-C` reuse** (`merge`, `commit` source) — clears
  the pending marker and attaches no trailer.
- **squash / `rebase -i` reword** — sums any duplicate cost trailers carried in
  from the squashed commits into a single line (the cost trailer name is
  config-derived, so `[format.rename]` keeps working), and leaves the watermark
  untouched.
- **rebase in progress** — `consume` is a no-op and never reads or clears the
  marker, so usage destined for the next real commit survives the replay.
- **`git commit --amend`** — re-scans and reuses the existing trailer rather than
  appending a duplicate block (the trailer block is idempotent on re-run).
- **detached HEAD** — usage records carry a real branch name, so a detached
  checkout matches nothing; no trailer is attached.

## Install

```sh
# Go toolchain (any platform):
go install github.com/mooracle/claude-budget@latest

# Homebrew (macOS / Linux):
brew install mooracle/tap/claude-budget
```

Or grab a prebuilt binary for your OS/arch from the
[GitHub releases](https://github.com/mooracle/claude-budget/releases) page
(`darwin`/`linux`/`windows` × `amd64`/`arm64`).

## Build & use

```sh
go build -o claude-budget .   # or: make build

# in any git repo where you use Claude Code:
claude-budget setup        # install the prepare-commit-msg + post-commit hooks
claude-budget status       # show this branch's uncommitted Claude usage + cost
claude-budget uninstall    # remove the hooks
claude-budget price        # smoke-test the embedded rate card
```

`trailer` and `consume` also exist as subcommands but are invoked by the
installed hooks, not run by hand. After `setup`, just `git commit` as usual —
the trailers attach automatically.

## Configuration

A committed `.claude-budget.toml` at the repo root selects which trailers to
attach — team-wide and reviewable. See the example in this repo.

## Pricing

`data/claude-pricing.json` is the checked-in rate card (Anthropic list prices;
cache tiers via the standard 0.1× / 1.25× / 2× multipliers), embedded into the
binary and parsed at runtime. Unknown models price to `0` rather than being
mispriced.

**Refreshing the rate card.** There's no machine-readable upstream to mirror
byte-for-byte, so the base prices are edited by hand and the cache tiers are
re-derived:

1. Open `platform.claude.com/docs/en/pricing.md`.
2. Update each model's base `input` / `output` in `data/claude-pricing.json` and
   bump the top-level `version` to today's date.
3. `make update-rates` — recomputes `cacheRead` / `cacheWrite5m` / `cacheWrite1h`
   from `input` via the 0.1× / 1.25× / 2× multipliers, preserving everything else.
4. `go test ./...` and commit the diff (the analog of Copilot Budget's
   `npm run update-rates`).
