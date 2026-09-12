#!/usr/bin/env bash
# fetch-rates.sh — refresh base prices in data/claude-pricing.json from Anthropic.
#
# There is no pricing API. `GET /v1/models` is authoritative for which model ids
# exist but returns no rates at all (id, display_name, capabilities, token
# limits, type — that's it). The only machine-readable Anthropic source for
# prices is the pricing docs page, served as markdown, so this parses that table.
#
# Parsing a human-facing page is inherently brittle, so nothing is written until
# the scrape passes the checks in validate(): most importantly, the table gives
# the cache-write tiers *explicitly*, and they must equal input x 1.25 / 2. That
# is redundant data, which makes it a genuine column-alignment check — if the
# docs gain, lose, or reorder a column, the arithmetic stops matching and the
# run aborts instead of writing garbage rates into everyone's commit trailers.
#
#   ./scripts/fetch-rates.sh            # fetch, validate, rewrite the card
#   ./scripts/fetch-rates.sh --dry-run  # print the diff, write nothing
#   PRICING_MD_FILE=x.md ./scripts/fetch-rates.sh --dry-run   # parse a local file
#
# Three numbers per model come from the page: input, output, and cacheRead.
# The cache-read multiplier is no longer uniform (0.1x on most models, 0.025x on
# Claude Fable 5.1 and Claude Mythos 5.1), so cacheRead has to be taken from the
# table rather than derived. The two write tiers are still a fixed multiple of
# input on every model; update-rates.sh re-derives those after the base rates
# land, keeping one source of truth for the write multipliers.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
card="$here/data/claude-pricing.json"
url="${PRICING_MD_URL:-https://platform.claude.com/docs/en/about-claude/pricing.md}"
dry_run=false
[ "${1:-}" = "--dry-run" ] && dry_run=true

command -v jq >/dev/null 2>&1 || { echo "fetch-rates: jq is required" >&2; exit 1; }
[ -f "$card" ] || { echo "fetch-rates: $card not found" >&2; exit 1; }

raw="$(mktemp)"; scraped="$(mktemp)"
trap 'rm -f "$raw" "$scraped"' EXIT

if [ -n "${PRICING_MD_FILE:-}" ]; then
  cp "$PRICING_MD_FILE" "$raw"
else
  curl -fsS --retry 3 --retry-delay 5 "$url" > "$raw"
fi

# Parse the model-pricing table into {id: {input, output, w5m, w1h, read}}.
#
# Row shape: | Model | Base Input | 5m Cache Write | 1h Cache Write | Cache Hit | Output |
# The batch-pricing and fast-mode tables further down the page also start with
# "| Claude ...", but carry three data cells rather than six — the cell count is
# what keeps them out, so their (discounted, and much lower) rates can never be
# mistaken for list prices.
#
# A cell is read as its first "$<number>" and nothing else. Cells can carry a
# footnote marker after the unit ("$0.25 / MTok1" — the "1" points at a note
# below the table), and stripping every non-digit would fold that marker into
# the price (0.251). A cell with no dollar amount parses as 0 and the row is
# dropped by the input/output > 0 filter.
awk -F'|' '
  function dollars(c,   m) {
    if (match(c, /\$[0-9]+(\.[0-9]+)?/)) { m = substr(c, RSTART + 1, RLENGTH - 1); return m + 0 }
    return 0
  }
  /^\| *Claude / {
    if (NF < 8) next                      # 6 data cells + leading/trailing empties
    name = $2
    gsub(/\[[^]]*\]\([^)]*\)/, "", name)  # markdown links: [text](url)
    gsub(/\([^)]*\)/, "", name)           # leftover parentheticals, incl. "()"
    sub(/ +(starting|through) .*/, "", name)  # "Claude Sonnet 5 starting September 1, 2026"
    gsub(/^ +| +$/, "", name)
    if (name == "") next

    id = tolower(name); gsub(/\./, "-", id); gsub(/ +/, "-", id)

    # $3 input, $4 5m write, $5 1h write, $6 cache read, $7 output
    printf "%s\t%s\t%s\t%s\t%s\t%s\n", id, dollars($3), dollars($7), dollars($4), dollars($5), dollars($6)
  }
' "$raw" \
| jq -R -s '
    [ split("\n")[] | select(length > 0) | split("\t")
      | {id: .[0], input: (.[1]|tonumber), output: (.[2]|tonumber),
         w5m: (.[3]|tonumber), w1h: (.[4]|tonumber), read: (.[5]|tonumber)} ]
    # One model can occupy two rows (Claude Sonnet 5 carried an introductory
    # price alongside its standard one). The card documents list prices, so the
    # higher of the two wins — which is also the conservative choice, since an
    # over-estimate can never understate a commit.
    | group_by(.id) | map(max_by(.input))
    | map(select(.input > 0 and .output > 0))
    | INDEX(.id)
  ' > "$scraped"

validate() {
  local n; n=$(jq 'length' "$scraped")
  echo "parsed $n models from ${PRICING_MD_FILE:-$url}"
  if [ "$n" -lt 8 ]; then
    echo "fetch-rates: only $n models parsed — the docs table has probably changed shape" >&2
    return 1
  fi

  # Column-alignment check against the table's own cache columns. The write
  # tiers are a fixed multiple of input on every model. Cache read is one of two
  # published multipliers (0.1x, or 0.025x on Fable 5.1 / Mythos 5.1); a third
  # value means either a new tier or a misread column, and both deserve a look
  # before it ships — add it here once confirmed against the page.
  local bad
  bad=$(jq -r '
    to_entries[] | select(
      ((.value.input * 1.25) - .value.w5m | fabs) > 0.011 or
      ((.value.input * 2.0 ) - .value.w1h | fabs) > 0.011 or
      (
        (((.value.input * 0.1  ) - .value.read | fabs) > 0.011) and
        (((.value.input * 0.025) - .value.read | fabs) > 0.011)
      ) or
      .value.output <= .value.input
    ) | "  \(.key): in=\(.value.input) out=\(.value.output) 5m=\(.value.w5m) 1h=\(.value.w1h) read=\(.value.read)"
  ' "$scraped")
  if [ -n "$bad" ]; then
    echo "fetch-rates: parsed rates fail the cache-multiplier check — columns look misread:" >&2
    echo "$bad" >&2
    return 1
  fi

  # A model already in the card that the page no longer lists (a retirement, or
  # a renamed row) keeps its existing price rather than vanishing: dropping it
  # would silently downgrade real usage to a fallback estimate.
  local missing
  missing=$(jq -r --slurpfile s "$scraped" '
    .models | keys[] | select(. as $k | ($s[0] | has($k)) | not)' "$card")
  [ -n "$missing" ] && echo "note: not on the page, keeping current price: $(tr '\n' ' ' <<<"$missing")"
  return 0
}

validate

# Report what would change before touching anything.
diff_report=$(jq -r --slurpfile s "$scraped" '
  .models as $old
  | ($s[0] | to_entries | map(
      . as $e
      | ($old[$e.key]) as $o
      | if $o == null then "  + \($e.key): $\($e.value.input)/$\($e.value.output) (new)"
        elif ($o.input != $e.value.input or $o.output != $e.value.output)
        then "  ~ \($e.key): $\($o.input)/$\($o.output) -> $\($e.value.input)/$\($e.value.output)"
        elif ($o.cacheRead != $e.value.read)
        then "  ~ \($e.key): cache read $\($o.cacheRead) -> $\($e.value.read)"
        else empty end))
  | .[]' "$card")

if [ -z "$diff_report" ]; then
  echo "rates already current (card version $(jq -r .version "$card"))"
  exit 0
fi
echo "changes:"; echo "$diff_report"

if $dry_run; then
  echo "(dry run — nothing written)"
  exit 0
fi

# Merge the scraped rates in, stamp the version, and let update-rates.sh
# re-derive the write tiers. Everything else in the card — fallbacks, notes,
# source — is preserved: `*` merges per key rather than replacing the object.
tmp="$(mktemp)"
jq --slurpfile s "$scraped" --arg today "$(date -u +%Y-%m-%d)" '
  .version = $today
  | .models = (.models + ($s[0] | map_values({input: .input, output: .output, cacheRead: .read})))
  | .models |= with_entries(
      .value = {input: .value.input, output: .value.output,
                cacheRead: (.value.cacheRead // 0),
                cacheWrite5m: (.value.cacheWrite5m // 0),
                cacheWrite1h: (.value.cacheWrite1h // 0)})
' "$card" > "$tmp"

# The merge above is additive (`.models + scraped` keeps left-hand keys), but
# assert it rather than trust it: a model silently dropped here would downgrade
# real, already-priced usage to a fallback estimate with nothing to notice it.
# Retiring a model is a deliberate hand edit, never something a sync does.
lost=$(jq -rn --slurpfile old "$card" --slurpfile new "$tmp" '
  ($old[0].models | keys) - ($new[0].models | keys) | .[]')
if [ -n "$lost" ]; then
  echo "fetch-rates: refusing to write — these models would be removed:" >&2
  while IFS= read -r m; do echo "  - $m" >&2; done <<<"$lost"
  exit 1
fi

mv "$tmp" "$card"

"$here/scripts/update-rates.sh"
echo "wrote $card (version $(jq -r .version "$card"))"
