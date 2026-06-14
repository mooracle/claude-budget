// Package trailer renders git commit-message trailer lines from a reader.Result.
//
// Values are bare numbers (no `$`, no unit suffix) — the unit is conveyed by the
// trailer name. Which lines appear, their decimal precision, and their names are
// all driven by config: each trailer is gated by a Trailers flag, cost precision
// comes from Format.CostPrecision, and any line's name can be remapped via
// Format.Rename. The package is pure (no I/O, no state) so it can be unit-tested
// in isolation and reused by both the normal append path and the rebase/squash
// summing path.
package trailer

import (
	"fmt"
	"strings"

	"github.com/mooracle/claude-budget/internal/config"
	"github.com/mooracle/claude-budget/internal/reader"
)

// Logical trailer keys — the stable identifiers used for config gating and for
// Format.Rename lookups. The rendered name defaults to the Claude-* form below.
const (
	KeyCost         = "cost"
	KeyCostModels   = "costModels"
	KeyTokens       = "tokens"
	KeyTokensModels = "tokensModels"
	KeyInteractions = "interactions"
)

// defaultNames maps each logical key to its default rendered trailer name.
var defaultNames = map[string]string{
	KeyCost:         "Claude-Cost",
	KeyCostModels:   "Claude-Cost-Models",
	KeyTokens:       "Claude-Tokens",
	KeyTokensModels: "Claude-Tokens-Models",
	KeyInteractions: "Claude-Interactions",
}

// Name returns the configured (possibly renamed) trailer name for a logical key,
// falling back to the Claude-* default when no rename is set. The rebase/squash
// summing path uses Name(cfg, KeyCost) so it matches whatever the cost trailer
// was actually written as.
func Name(cfg *config.Config, key string) string {
	if cfg != nil && cfg.Format.Rename != nil {
		if n, ok := cfg.Format.Rename[key]; ok && n != "" {
			return n
		}
	}
	return defaultNames[key]
}

// Format renders the enabled trailer lines for res as "Name: value" strings.
// It returns an empty slice when there's nothing to attribute (res is nil or has
// zero deduped requests). Per-model lines preserve res.Models order (the reader
// sorts by cost descending), so output is stable across runs.
func Format(res *reader.Result, cfg *config.Config) []string {
	if res == nil || res.Requests == 0 {
		return nil
	}
	if cfg == nil {
		cfg = config.Defaults()
	}
	prec := cfg.Format.CostPrecision
	if prec < 0 {
		prec = 0
	}

	var lines []string
	if cfg.Trailers.Cost {
		lines = append(lines, fmt.Sprintf("%s: %.*f", Name(cfg, KeyCost), prec, res.TotalCostUSD))
	}
	if cfg.Trailers.CostModels {
		if parts := modelParts(res, func(m reader.ModelStat) string {
			return fmt.Sprintf("%s=%.*f", m.Model, prec, m.CostUSD)
		}); len(parts) > 0 {
			lines = append(lines, fmt.Sprintf("%s: %s", Name(cfg, KeyCostModels), strings.Join(parts, ",")))
		}
	}
	if cfg.Trailers.Tokens {
		lines = append(lines, fmt.Sprintf("%s: %d", Name(cfg, KeyTokens), res.TotalTokens))
	}
	if cfg.Trailers.TokensModels {
		if parts := modelParts(res, func(m reader.ModelStat) string {
			return fmt.Sprintf("%s=%d", m.Model, m.Tokens)
		}); len(parts) > 0 {
			lines = append(lines, fmt.Sprintf("%s: %s", Name(cfg, KeyTokensModels), strings.Join(parts, ",")))
		}
	}
	if cfg.Trailers.Interactions {
		lines = append(lines, fmt.Sprintf("%s: %d", Name(cfg, KeyInteractions), res.Requests))
	}
	return lines
}

// modelParts renders one "model=value" fragment per model, in res.Models order.
func modelParts(res *reader.Result, render func(reader.ModelStat) string) []string {
	parts := make([]string, 0, len(res.Models))
	for _, m := range res.Models {
		parts = append(parts, render(m))
	}
	return parts
}
