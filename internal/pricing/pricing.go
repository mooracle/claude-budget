// Package pricing loads the per-model Anthropic rate card and prices token usage.
//
// The rate card is a checked-in data/claude-pricing.json embedded into the binary
// at build time (see main.go) and parsed at runtime here — the direct analog of how
// Copilot Budget bundles its YAML rate card into the dist bundle and JSON-parses it.
package pricing

import (
	"encoding/json"
	"regexp"
	"strings"
)

// snapshotSuffix matches a trailing dated snapshot like "-20251001".
var snapshotSuffix = regexp.MustCompile(`-\d{8}$`)

// ctrlStripper removes CR/LF from a model id. The normalized name is rendered
// verbatim into -Models trailer values, so a transcript-supplied newline would
// otherwise split one trailer across several commit-message lines (the same
// corruption guarded against for rename values in config.sanitize).
var ctrlStripper = strings.NewReplacer("\r", "", "\n", "")

// Rate is the per-1M-token price for one model, in the rate card's currency (USD).
type Rate struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheRead    float64 `json:"cacheRead"`
	CacheWrite5m float64 `json:"cacheWrite5m"`
	CacheWrite1h float64 `json:"cacheWrite1h"`
}

// RateCard is the parsed data/claude-pricing.json.
type RateCard struct {
	Version  string          `json:"version"`
	Currency string          `json:"currency"`
	Unit     string          `json:"unit"`
	Models   map[string]Rate `json:"models"`
	// Fallbacks maps a model-family prefix ("claude-opus") to the rate-card key
	// whose price stands in for an unlisted member of that family, with the ""
	// key as the last-resort default for an unrecognized family. It exists so a
	// model released after this binary was built is estimated rather than
	// counted as free — see Priced.
	Fallbacks map[string]string `json:"fallbacks"`
}

// Usage is one request's disjoint token buckets, as recorded by Claude Code.
// Anthropic's input_tokens is already the uncached remainder, so no subtraction
// is needed — the five buckets are summed independently.
type Usage struct {
	Input        int64
	Output       int64
	CacheRead    int64
	CacheWrite5m int64 // cache_creation.ephemeral_5m_input_tokens
	CacheWrite1h int64 // cache_creation.ephemeral_1h_input_tokens
}

// Load parses an embedded/loaded rate card.
func Load(data []byte) (*RateCard, error) {
	var rc RateCard
	if err := json.Unmarshal(data, &rc); err != nil {
		return nil, err
	}
	return &rc, nil
}

// Normalize maps a raw transcript model id to its rate-card key. It lowercases,
// strips request-routing prefixes, and drops two suffix forms that name the same
// priced model: a context-window tag ("claude-opus-4-8[1m]") and a dated snapshot
// ("claude-haiku-4-5-20251001"). Both appear verbatim in real Claude Code
// transcripts, and without this they miss the bare rate-card key and price to 0.
// This is alias normalization only: it resolves ids that name a model already in
// the card. Pricing an id the card has never heard of is Priced's job.
func Normalize(model string) string {
	m := strings.ToLower(strings.TrimSpace(ctrlStripper.Replace(model)))
	for _, p := range []string{"claude-code/", "anthropic/", "us.anthropic."} {
		m = strings.TrimPrefix(m, p)
	}
	if i := strings.IndexByte(m, '['); i >= 0 && strings.HasSuffix(m, "]") {
		m = m[:i] // drop a trailing context-window tag like "[1m]"
	}
	m = snapshotSuffix.ReplaceAllString(m, "") // drop a trailing "-YYYYMMDD" snapshot
	return m
}

// Priced resolves a model to a rate. source is the rate-card key the price came
// from, and exact reports whether that was a direct hit.
//
// A model with no entry of its own is *estimated* rather than treated as free:
// the longest matching family prefix in Fallbacks wins, and the "" key is the
// last-resort default for a family the card has never seen. Anthropic ships new
// models faster than this binary is released, and a released model priced at 0
// silently understates a commit's cost — an over-estimate is the safer error for
// a budget tool, because it can never leave you believing you spent less than
// you did. Callers that need to disclose the guess use source and exact; see
// runStatus. Which key stands in for a family is data, not inference: the same
// human who adds a price also updates the fallbacks table.
//
// Estimation is opt-in per card. With no Fallbacks configured, an unknown model
// resolves to no rate and prices to 0, the behavior before fallbacks existed.
// An empty model id is corrupt input rather than a new release, so it never
// borrows a rate.
func (rc *RateCard) Priced(model string) (r Rate, source string, exact bool) {
	m := Normalize(model)
	if r, ok := rc.Models[m]; ok {
		return r, m, true
	}
	if m == "" {
		return Rate{}, "", false
	}
	best := "" // "" doubles as "no family matched" and as the default key
	for fam := range rc.Fallbacks {
		if fam != "" && len(fam) > len(best) && strings.HasPrefix(m, fam+"-") {
			best = fam
		}
	}
	key := rc.Fallbacks[best]
	if r, ok := rc.Models[key]; ok {
		return r, key, false
	}
	return Rate{}, "", false
}

// CostUSD returns the dollar cost of one request's usage, estimating via
// Priced when the model has no rate of its own.
func (rc *RateCard) CostUSD(model string, u Usage) float64 {
	r, _, _ := rc.Priced(model)
	return (float64(u.Input)*r.Input +
		float64(u.Output)*r.Output +
		float64(u.CacheRead)*r.CacheRead +
		float64(u.CacheWrite5m)*r.CacheWrite5m +
		float64(u.CacheWrite1h)*r.CacheWrite1h) / 1e6
}

// Known reports whether the card carries a rate for this model itself. It is
// deliberately not satisfied by a Fallbacks estimate — callers use it to tell a
// quoted price from a guessed one.
func (rc *RateCard) Known(model string) bool {
	_, ok := rc.Models[Normalize(model)]
	return ok
}
