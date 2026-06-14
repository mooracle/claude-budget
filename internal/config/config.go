// Package config parses the committed .claude-budget.toml that controls which
// trailers attach to commit messages and how they're rendered.
//
// The file is checked into the repo so the whole team gets identical trailers.
// An absent file (or a missing key) falls back to the defaults: only Claude-Cost
// on, cost precision 2 — never zeroing out unspecified keys.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// FileName is the per-repo config file, read from the repo root.
const FileName = ".claude-budget.toml"

// maxCostPrecision bounds Format.CostPrecision so a stray config value (e.g. a
// fat-fingered costPrecision = 100000) can't produce a multi-KB trailer line.
// Eight decimals is already far finer than any real per-commit USD cost.
const maxCostPrecision = 8

// Config is the parsed .claude-budget.toml.
type Config struct {
	Trailers Trailers `toml:"trailers"`
	Format   Format   `toml:"format"`
}

// Trailers toggles which trailer lines are appended to a commit message.
type Trailers struct {
	Cost         bool `toml:"cost"`         // Claude-Cost: total USD
	CostModels   bool `toml:"costModels"`   // Claude-Cost-Models: per-model USD
	Tokens       bool `toml:"tokens"`       // Claude-Tokens: total tokens
	TokensModels bool `toml:"tokensModels"` // Claude-Tokens-Models: per-model tokens
	Interactions bool `toml:"interactions"` // Claude-Interactions: deduped request count
}

// Format controls how trailer values and names are rendered.
type Format struct {
	CostPrecision int               `toml:"costPrecision"` // decimal places on cost trailers
	Rename        map[string]string `toml:"rename"`        // trailer key → custom name (e.g. cost → AI-Cost)
}

// Defaults returns the baseline config: only Claude-Cost on, precision 2,
// everything else off.
func Defaults() *Config {
	return &Config{
		Trailers: Trailers{Cost: true},
		Format:   Format{CostPrecision: 2, Rename: map[string]string{}},
	}
}

// Load reads <repoRoot>/.claude-budget.toml. An absent file yields Defaults().
// Present keys override the defaults; keys missing from the file keep their
// default value (no zeroing-out) because decoding merges into a Defaults() base.
func Load(repoRoot string) (*Config, error) {
	cfg := Defaults()
	path := filepath.Join(repoRoot, FileName)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := toml.Unmarshal(b, cfg); err != nil {
		return nil, err
	}
	sanitize(cfg)
	return cfg, nil
}

// sanitize clamps and cleans parsed values so a typo in the committed config
// can't yield a malformed commit message. CostPrecision is bounded to
// [0, maxCostPrecision], and rename values are stripped of CR/LF — a newline in a
// trailer name would otherwise split one trailer into several message lines.
func sanitize(cfg *Config) {
	if cfg.Format.CostPrecision < 0 {
		cfg.Format.CostPrecision = 0
	}
	if cfg.Format.CostPrecision > maxCostPrecision {
		cfg.Format.CostPrecision = maxCostPrecision
	}
	if cfg.Format.Rename == nil {
		cfg.Format.Rename = map[string]string{}
		return
	}
	stripper := strings.NewReplacer("\r", "", "\n", "")
	for k, v := range cfg.Format.Rename {
		if clean := stripper.Replace(v); clean != v {
			cfg.Format.Rename[k] = clean
		}
	}
}
