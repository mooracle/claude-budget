# claude-budget

Tracks **GitHub Claude Code** token usage per commit and appends measured-cost git
trailers (e.g. `Claude-Cost: 0.42`) to commit messages — so every commit records
the API-equivalent cost of the Claude Code activity that produced it.

It's the Claude Code counterpart to [Copilot Budget](https://github.com/mooracle/tokentrack):
same per-commit attribution idea, but sourced from Claude Code's own session
transcripts instead of an OTel database, and shipped as a single static Go binary
with no runtime dependencies.

> Status: **`setup` / `status` / `uninstall` work today.** Pricing, the transcript
> reader, git/hook/state plumbing are implemented and validated against real
> transcripts. The `trailer` / `consume` write path and `.claude-budget.toml`
> parsing are the remaining work — see
> [docs/plans/2026-06-14-claude-budget.md](docs/plans/2026-06-14-claude-budget.md).

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
  └─ prepare-commit-msg → claude-budget trailer "$1"
       · scan ~/.claude/projects/* for cwd under this repo
       · keep records: gitBranch == <current> AND timestamp > branch high-water mark
       · dedup by requestId (~43% of records are streaming duplicates)
       · sum the 5 token buckets per model, price via embedded rate card
       · append trailers, stage watermark in .git/claude-budget.pending
  └─ post-commit       → claude-budget consume
       · promote the staged watermark into .git/claude-budget (unless rebasing)
```

A cancelled commit never reaches `post-commit`, so its usage carries forward —
the same deferred-truncation guarantee as Copilot Budget (issue #10).

## Build & use

```sh
go build -o claude-budget .

# in any git repo where you use Claude Code:
claude-budget setup        # install the prepare-commit-msg + post-commit hooks
claude-budget status       # show this branch's uncommitted Claude usage + cost
claude-budget uninstall    # remove the hooks
claude-budget price        # smoke-test the embedded rate card
```

## Configuration

A committed `.claude-budget.toml` at the repo root selects which trailers to
attach — team-wide and reviewable. See the example in this repo.

## Pricing

`data/claude-pricing.json` is the checked-in rate card (Anthropic list prices;
cache tiers via the standard 0.1× / 1.25× / 2× multipliers), embedded into the
binary and parsed at runtime. Refresh it from
`platform.claude.com/docs/en/pricing.md` and commit the diff — the analog of
Copilot Budget's `npm run update-rates`.

Unknown models price to `0` rather than being mispriced.
