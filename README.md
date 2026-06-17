# claude-budget

Tracks **GitHub Claude Code** token usage per commit and appends measured-cost git
trailers (e.g. `Claude-Cost: 0.42`) to commit messages — so every commit records
the API-equivalent cost of the Claude Code activity that produced it.

It's the Claude Code counterpart to [Copilot Budget](https://github.com/mooracle/copilot-budget):
same per-commit attribution idea, but sourced from Claude Code's own session
transcripts instead of an OTel database, and shipped as a single static Go binary
with no runtime dependencies. No daemon, no account linking, no telemetry — it
reads the transcripts Claude Code already writes on your machine.

> Status: **complete.** `setup` / `status` / `uninstall` and the full
> `trailer` / `consume` write path are live: a `git commit` now appends the
> configured trailers and advances the per-branch watermark. Pricing, the
> transcript reader, git/hook/state plumbing, `.claude-budget.toml` parsing, the
> unit suite, and a real-`git` e2e suite are all implemented and green
> (`go test ./...`). The implementation plan is archived under
> [docs/plans/completed/](docs/plans/completed/).

## Contents

- [Quickstart](#quickstart)
- [Installation](#installation)
- [Usage](#usage)
  - [Turn on tracking](#1-turn-on-tracking-in-a-repo)
  - [See what's pending](#2-see-whats-pending)
  - [Commit as usual](#3-commit-as-usual)
  - [Command reference](#command-reference)
- [Configuration](#configuration)
- [How it works](#how-it-works)
  - [Rebase, squash & amend](#rebase-squash--amend)
- [FAQ & troubleshooting](#faq--troubleshooting)
- [Pricing](#pricing)
- [Building from source](#building-from-source)

## Quickstart

```sh
# 1. Install (Go toolchain on any platform)
go install github.com/mooracle/claude-budget@latest

# 2. In a git repo where you use Claude Code:
claude-budget setup

# 3. Commit as usual — the cost trailer attaches automatically:
git commit -m "Add feature"
#   └─ Claude-Cost: 0.42

# 4. Check what's pending before you commit, any time:
claude-budget status
```

That's the whole loop. The rest of this README is reference material for when you
want more trailers, custom names, or an explanation of an edge case.

## Installation

Pick whichever fits your setup. The binary is self-contained — no runtime
dependencies. After any method, verify with `claude-budget version`.

**Go toolchain (any platform):**

```sh
go install github.com/mooracle/claude-budget@latest
```

Drops `claude-budget` into `$(go env GOPATH)/bin` — make sure that's on your `PATH`.

**Homebrew (macOS / Linux):**

```sh
brew install mooracle/tap/claude-budget
```

### From GitHub releases (prebuilt binary, no toolchain)

Each [release](https://github.com/mooracle/claude-budget/releases) attaches a
ready-to-run binary per platform, named `claude-budget-<os>-<arch>`
(`darwin`/`linux`/`windows` × `amd64`/`arm64`; `.exe` on Windows). No Go, no
Homebrew required.

**macOS / Linux** — download the binary for your platform, mark it executable,
and put it on your `PATH`. The `releases/latest/download/…` URL always resolves to
the newest release:

```sh
# Choose one: darwin-arm64 (Apple Silicon), darwin-amd64 (Intel Mac),
#             linux-amd64, linux-arm64
PLATFORM=darwin-arm64

curl -fsSL -o claude-budget \
  "https://github.com/mooracle/claude-budget/releases/latest/download/claude-budget-${PLATFORM}"
chmod +x claude-budget
sudo mv claude-budget /usr/local/bin/    # or any directory already on your PATH

claude-budget version
```

Not sure of your arch? `uname -sm` prints it (`arm64` = Apple Silicon/ARM,
`x86_64` = `amd64`). Pin a specific version by swapping `latest/download` for
`download/v0.1.0`.

> **macOS Gatekeeper.** The binary isn't code-signed. Downloading with `curl` (as
> above) avoids the quarantine flag; if you download via a browser instead, clear
> it with `xattr -d com.apple.quarantine claude-budget`, or allow the binary once
> under System Settings → Privacy & Security.

**Windows** — download `claude-budget-windows-amd64.exe` (or `-arm64`) from the
releases page, rename it to `claude-budget.exe`, and place it in a folder on your
`PATH`. Then run `claude-budget version` in PowerShell or Command Prompt.

**Prerequisite:** GitHub Claude Code, used in the repo you want to track.
`claude-budget` reads the transcripts Claude Code writes under
`~/.claude/projects/` — if you've used Claude Code in a repo, those already exist.
Nothing to enable.

## Usage

### 1. Turn on tracking in a repo

From inside the git repository where you use Claude Code:

```sh
claude-budget setup
```

```
✓ installed claude-budget hooks in /path/to/repo/.git/hooks
  tip: add /path/to/repo/.claude-budget.toml to choose trailers (default: Claude-Cost only)
  run `claude-budget status` any time to see uncommitted usage
```

`setup` installs two git hooks (`prepare-commit-msg` and `post-commit`) into this
repo's `.git/hooks`. It is **safe**: if either hook already exists and isn't one
of ours, `setup` refuses rather than overwriting it.

> **Per repo, per clone.** Hooks live in `.git/hooks`, which git does not commit.
> Run `setup` once in each clone where you want tracking. Sharing the
> _configuration_ (which trailers, what names) is done with a committed
> [`.claude-budget.toml`](#configuration) — so the whole team produces identical
> trailers without coordinating settings.

### 2. See what's pending

Before (or instead of) committing, check the Claude Code usage that will be
attributed to your next commit. It's read-only and changes nothing:

```sh
claude-budget status
```

```
claude-budget — /path/to/repo @ branch "main"

  pending since last commit:  $0.42   ·   128944 tokens   ·   37 requests

  model                      cost         tokens   reqs
  claude-opus-4-8           $0.41         126501     34
  claude-haiku-4-5          $0.01           2443      3

  config: trailers cost   ·   cost precision 2
  hooks: installed (trailers attach on commit)
```

The last lines echo your active configuration and whether the hooks are installed
— handy for confirming a `.claude-budget.toml` edit parsed as intended.

### 3. Commit as usual

```sh
git commit -m "Add the thing"
```

The cost trailer attaches automatically. Inspect it:

```sh
git log -1
```

```
    Add the thing

    Claude-Cost: 0.42
```

A commit with no attributable usage (a typo fix with no Claude Code activity) gets
no trailer block — that's expected, not a bug.

### Command reference

There are four commands you run by hand:

| Command | What it does |
|---------|--------------|
| `claude-budget setup` | Install the hook pair in the current repo, enabling automatic trailers. Refuses to clobber a non-`claude-budget` hook. |
| `claude-budget status` | Show the current branch's uncommitted Claude usage and cost, broken down by model. Read-only. |
| `claude-budget uninstall` | Remove only the `claude-budget` hooks from the current repo. Existing trailers and your `.claude-budget.toml` are left intact. |
| `claude-budget price` | Self-test: load the embedded rate card and price a sample request. Prints the rate-card version. |
| `claude-budget version` | Print the binary version (`-v` / `--version` too). |

Two more subcommands — `trailer` and `consume` — exist but are invoked by the
installed hooks, not run by hand:

- **`trailer <msgfile> --source <s>`** — called by `prepare-commit-msg`. Scans the
  branch's pending usage, appends the configured trailers, and stages a watermark.
- **`consume`** — called by `post-commit`. Promotes the staged watermark so the
  next commit only counts activity that came after.

Both are written to **never block a commit**: any internal failure is logged to
stderr and the commit still succeeds (just without a trailer).

## Configuration

A committed `.claude-budget.toml` at the **repo root** selects which trailers to
attach and how they're rendered — team-wide and reviewable. Every key is optional;
an absent file (or a missing key) keeps the defaults below (only `Claude-Cost`,
precision 2).

```toml
[trailers]
cost          = true    # Claude-Cost:          total USD                 (default: on)
costModels    = false   # Claude-Cost-Models:   per-model USD
tokens        = false   # Claude-Tokens:        total tokens (all buckets)
tokensModels  = false   # Claude-Tokens-Models: per-model tokens
interactions  = false   # Claude-Interactions:  deduped request count

[format]
costPrecision = 2       # decimal places on cost trailers (default: 2, clamped to 0–8)

# Rename any trailer (key → custom name). Summing on squash/rebase follows the
# renamed name, so this keeps working across history rewrites.
[format.rename]
cost = "AI-Cost"        # renders "AI-Cost: 0.42" instead of "Claude-Cost: 0.42"
```

**`[trailers]` — which lines attach.** Turn each on (`true`) or off (`false`):

| Key | Trailer | Example |
|-----|---------|---------|
| `cost` | `Claude-Cost` | `Claude-Cost: 0.42` |
| `costModels` | `Claude-Cost-Models` | `Claude-Cost-Models: claude-opus-4-8=0.41,claude-haiku-4-5=0.01` |
| `tokens` | `Claude-Tokens` | `Claude-Tokens: 128944` |
| `tokensModels` | `Claude-Tokens-Models` | `Claude-Tokens-Models: claude-opus-4-8=126501,...` |
| `interactions` | `Claude-Interactions` | `Claude-Interactions: 37` |

`Claude-Tokens` sums all five token buckets (input, output, cache read, and the
two cache-write tiers). `Claude-Interactions` is the deduplicated request count.

**`[format]`.** `costPrecision` sets the decimal places on cost trailers (default
`2`, clamped to `0`–`8`).

**`[format.rename]`.** Map any trailer key (the same ones from `[trailers]`) to a
name your team prefers. Renaming is safe across history rewrites: squash/reword
folds duplicate cost trailers by their _configured_ name. Newlines in a rename
value are stripped so a stray one can't corrupt a commit message.

See [`.claude-budget.toml`](.claude-budget.toml) in this repo for the annotated
example.

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
history rewrites don't double-count or lose usage — you don't need to do anything
special:

- **merge / cherry-pick / `-c`/`-C` reuse** (`merge`, `commit` source) — clears
  the pending marker and attaches no trailer.
- **squash / `rebase -i` reword** — sums any duplicate cost trailers carried in
  from the squashed commits into a single line (the cost trailer name is
  config-derived, so `[format.rename]` keeps working), and leaves the watermark
  untouched.
- **rebase in progress** — `consume` is a no-op and never reads or clears the
  marker, so usage destined for the next real commit survives the replay.
- **`git commit --amend`** — arrives as git's `commit` source, so the existing
  message (already carrying its cost trailer) is reused as-is: no re-scan, no
  duplicate block. The trailer block is also idempotent if `prepare-commit-msg`
  happens to fire twice for one message.
- **detached HEAD** — usage records carry a real branch name, so a detached
  checkout matches nothing; no trailer is attached.

## FAQ & troubleshooting

**No trailer appeared on my commit.** Work through these in order:

1. **Are the hooks installed in this clone?** Run `claude-budget status` — the last
   line says `hooks: installed` or `hooks: not installed`. Hooks aren't committed,
   so each clone needs its own `setup`.
2. **Was there any usage to attribute?** A commit only gets a trailer if Claude Code
   did measurable work on this branch since your last commit. If `status` says
   "nothing pending", there's nothing to record.
3. **Is `claude-budget` on your `PATH`?** The hooks call the binary by name and exit
   quietly if it isn't found. Confirm with `claude-budget version`.
4. **Are you on a detached HEAD?** Attribution is per-branch; a detached checkout
   matches no branch and gets no trailer.

The hooks are intentionally fail-open: if anything goes wrong your commit still
succeeds, just without a trailer, and the error is printed to stderr — re-run the
commit in a terminal and watch for a `claude-budget` line.

**Does this send my data anywhere?** No. Everything is local: it reads the
transcripts Claude Code already writes under `~/.claude/projects/`, prices them
against a rate card embedded in the binary, and writes the result into your commit
message. No network call, no daemon, no account linking.

**How does it handle rebase, squash, and amend?** It's designed so history
rewrites don't double-count or lose usage, and you don't need to do anything
special. See [Rebase, squash & amend](#rebase-squash--amend) for the per-action
breakdown.

**Will `setup` overwrite my existing git hooks?** No. If a `prepare-commit-msg` or
`post-commit` hook already exists and isn't one `claude-budget` wrote, `setup`
refuses and leaves it untouched. If you use a hook manager (pre-commit, Husky,
Lefthook…) that owns those files, wire the two commands into your manager instead:
`claude-budget trailer "$1" --source "${2:-}"` at the `prepare-commit-msg` stage,
and `claude-budget consume` at `post-commit`.

**How do I turn it off?** Run `claude-budget uninstall` in the repo. It removes
only the `claude-budget` hooks; other hooks, your committed trailers, and your
`.claude-budget.toml` are left in place. To stop tracking everywhere, run it in
each clone.

**A model shows as "unpriced" / costs `$0.00`.** The embedded rate card doesn't
recognize that model name yet (usually because it's newer than the binary). Update
to a newer release, or refresh `data/claude-pricing.json` (see [Pricing](#pricing)).
Unknown models are priced at `0` rather than guessed.

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
3. `make update-rates` (requires [`jq`](https://jqlang.github.io/jq/)) —
   recomputes `cacheRead` / `cacheWrite5m` / `cacheWrite1h` from `input` via the
   0.1× / 1.25× / 2× multipliers, preserving everything else.
4. `go test ./...` and commit the diff (the analog of Copilot Budget's
   `npm run update-rates`).

## Building from source

```sh
go build -o claude-budget .   # or: make build
```

The `Makefile` wraps the common developer tasks:

```sh
make build        # build the local binary
make check        # vet + build + test gate (run before committing)
make build-all    # cross-compile every release target into dist/
make update-rates # re-derive cache-tier prices (requires jq)
make clean        # remove build output
```

## License

[MIT](LICENSE) © mooracle
