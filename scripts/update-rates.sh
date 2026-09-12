#!/usr/bin/env bash
# update-rates.sh — re-derive Claude cache-write prices from base input.
#
# There is no machine-readable upstream rate card to mirror byte-for-byte (unlike
# tokentrack's Copilot YAML). fetch-rates.sh scrapes the pricing docs table for
# the per-model numbers (input, output, cacheRead) and then runs this to fill in
# the derived write tiers. The same split applies to a hand edit:
#
#   1. Open https://platform.claude.com/docs/en/about-claude/pricing
#   2. In data/claude-pricing.json, update each model's base `input`, `output`,
#      and `cacheRead` (per-MTok, USD) and bump the top-level `version` to
#      today's date. cacheRead is 0.1 x input on most models but not all (0.025x
#      on Claude Fable 5.1 and Claude Mythos 5.1), so it is copied from the
#      table, not derived; omit it on a new model to get the 0.1x default.
#   3. If a new model is now its family's current generation, repoint that family
#      in the `fallbacks` table — that's the rate stand-in for models newer than
#      this card, so leaving it stale keeps estimating from the old generation.
#   4. Run `make update-rates` (this script) to re-derive the two write tiers
#      from `input` via the standard Anthropic multipliers below.
#   5. `go test ./...` and commit the diff.
#
# Cache multipliers (kept in sync with the rate card's `note` field):
#   cacheWrite5m = 1.25 x input
#   cacheWrite1h = 2.0  x input
#   cacheRead    = 0.1  x input — default only, applied when the field is absent
#
# `input`, `output`, an existing `cacheRead`, and the top-level metadata
# (version/source/note/currency/unit/fallbacks) are preserved untouched.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
card="$here/data/claude-pricing.json"

command -v jq >/dev/null 2>&1 || { echo "update-rates: jq is required" >&2; exit 1; }
[ -f "$card" ] || { echo "update-rates: $card not found" >&2; exit 1; }

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

# Round each derived tier to 2 decimals so the JSON stays human-diffable.
# cacheRead is rounded to 3: 0.025 x $1 (a hypothetical cheap model on the low
# multiplier) would otherwise collapse to $0.03.
jq '
  .models |= with_entries(
    .value as $m
    | .value = ($m + {
        cacheRead:    (if ($m.cacheRead // 0) > 0 then $m.cacheRead
                       else (($m.input * 0.1) * 1000 | round / 1000) end),
        cacheWrite5m: (($m.input * 1.25) * 100 | round / 100),
        cacheWrite1h: (($m.input * 2.0)  * 100 | round / 100)
      })
  )
' "$card" > "$tmp"

mv "$tmp" "$card"
trap - EXIT
echo "re-derived cache tiers in data/claude-pricing.json"
