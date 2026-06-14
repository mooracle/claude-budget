// Package pricing loads the per-model Anthropic rate card and prices token usage.
//
// The rate card is a checked-in data/claude-pricing.json embedded into the binary
// at build time (see main.go) and parsed at runtime here — the direct analog of how
// Copilot Budget bundles its YAML rate card into the dist bundle and JSON-parses it.
package pricing

import (
	"encoding/json"
	"strings"
)

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

// Normalize strips request-routing prefixes and lowercases the model id, so a
// raw transcript model string matches a rate-card key. No family fallback —
// an unknown id stays unknown (and prices to 0) rather than being mispriced.
func Normalize(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	for _, p := range []string{"claude-code/", "anthropic/", "us.anthropic."} {
		m = strings.TrimPrefix(m, p)
	}
	return m
}

// CostUSD returns the dollar cost of one request's usage. Unknown model → 0,
// to avoid silently mispricing models the rate card hasn't been updated for.
func (rc *RateCard) CostUSD(model string, u Usage) float64 {
	r, ok := rc.Models[Normalize(model)]
	if !ok {
		return 0
	}
	return (float64(u.Input)*r.Input +
		float64(u.Output)*r.Output +
		float64(u.CacheRead)*r.CacheRead +
		float64(u.CacheWrite5m)*r.CacheWrite5m +
		float64(u.CacheWrite1h)*r.CacheWrite1h) / 1e6
}

// Known reports whether the rate card prices this model.
func (rc *RateCard) Known(model string) bool {
	_, ok := rc.Models[Normalize(model)]
	return ok
}
