#!/usr/bin/env bash
# update-rates.sh — re-derive Claude cache-tier prices from base input/output.
#
# There is no machine-readable upstream rate card to mirror byte-for-byte (unlike
# tokentrack's Copilot YAML), so the refresh procedure is deliberately manual at
# the base-price step and automated only for the derived cache tiers:
#
#   1. Open https://platform.claude.com/docs/en/about-claude/pricing
#   2. In data/claude-pricing.json, update each model's base `input` and `output`
#      (per-MTok, USD) and bump the top-level `version` to today's date.
#   3. Run `make update-rates` (this script) to re-derive the three cache tiers
#      from `input` via the standard Anthropic multipliers below.
#   4. `go test ./...` and commit the diff.
#
# Cache multipliers (kept in sync with the rate card's `note` field):
#   cacheRead    = 0.1  x input
#   cacheWrite5m = 1.25 x input
#   cacheWrite1h = 2.0  x input
#
# `input`, `output`, and the top-level metadata (version/source/note/currency/
# unit) are preserved untouched; only the three cache tiers are recomputed.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
card="$here/data/claude-pricing.json"

command -v jq >/dev/null 2>&1 || { echo "update-rates: jq is required" >&2; exit 1; }
[ -f "$card" ] || { echo "update-rates: $card not found" >&2; exit 1; }

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

# Round each derived tier to 2 decimals so the JSON stays human-diffable.
jq '
  .models |= with_entries(
    .value as $m
    | .value = ($m + {
        cacheRead:    (($m.input * 0.1)  * 100 | round / 100),
        cacheWrite5m: (($m.input * 1.25) * 100 | round / 100),
        cacheWrite1h: (($m.input * 2.0)  * 100 | round / 100)
      })
  )
' "$card" > "$tmp"

mv "$tmp" "$card"
trap - EXIT
echo "re-derived cache tiers in data/claude-pricing.json"
