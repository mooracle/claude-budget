package config

import (
	"os"
	"path/filepath"
	"testing"
)

// write a .claude-budget.toml into a temp repo root and return that root.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, FileName), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return root
}

func TestLoadAbsentFileReturnsDefaults(t *testing.T) {
	root := t.TempDir() // no config file written
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	def := Defaults()
	if cfg.Trailers != def.Trailers {
		t.Errorf("trailers = %+v, want defaults %+v", cfg.Trailers, def.Trailers)
	}
	if cfg.Format.CostPrecision != def.Format.CostPrecision {
		t.Errorf("CostPrecision = %d, want %d", cfg.Format.CostPrecision, def.Format.CostPrecision)
	}
	if cfg.Format.Rename == nil {
		t.Error("Rename should be a non-nil empty map")
	}
	if len(cfg.Format.Rename) != 0 {
		t.Errorf("Rename = %v, want empty", cfg.Format.Rename)
	}
}

func TestDefaults(t *testing.T) {
	def := Defaults()
	if !def.Trailers.Cost {
		t.Error("default cost trailer should be on")
	}
	if def.Trailers.CostModels || def.Trailers.Tokens || def.Trailers.TokensModels || def.Trailers.Interactions {
		t.Errorf("only cost should be on by default, got %+v", def.Trailers)
	}
	if def.Format.CostPrecision != 2 {
		t.Errorf("default precision = %d, want 2", def.Format.CostPrecision)
	}
}

func TestLoadFullFile(t *testing.T) {
	root := writeConfig(t, `
[trailers]
cost          = true
costModels    = true
tokens        = true
tokensModels  = true
interactions  = true

[format]
costPrecision = 4

[format.rename]
cost = "AI-Cost"
tokens = "AI-Tokens"
`)
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Trailers{Cost: true, CostModels: true, Tokens: true, TokensModels: true, Interactions: true}
	if cfg.Trailers != want {
		t.Errorf("trailers = %+v, want %+v", cfg.Trailers, want)
	}
	if cfg.Format.CostPrecision != 4 {
		t.Errorf("CostPrecision = %d, want 4", cfg.Format.CostPrecision)
	}
	if cfg.Format.Rename["cost"] != "AI-Cost" || cfg.Format.Rename["tokens"] != "AI-Tokens" {
		t.Errorf("rename = %v, want cost=AI-Cost tokens=AI-Tokens", cfg.Format.Rename)
	}
}

func TestLoadPartialFileFallsBackToDefaults(t *testing.T) {
	// Only flip costModels on and bump precision; cost (default on) and the
	// other toggles must keep their defaults rather than zeroing out.
	root := writeConfig(t, `
[trailers]
costModels = true

[format]
costPrecision = 3
`)
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Trailers.Cost {
		t.Error("cost should keep its default (true) when omitted from a partial file")
	}
	if !cfg.Trailers.CostModels {
		t.Error("costModels should be on (set in file)")
	}
	if cfg.Trailers.Tokens || cfg.Trailers.TokensModels || cfg.Trailers.Interactions {
		t.Errorf("omitted toggles should stay off (default), got %+v", cfg.Trailers)
	}
	if cfg.Format.CostPrecision != 3 {
		t.Errorf("CostPrecision = %d, want 3", cfg.Format.CostPrecision)
	}
}

func TestLoadExplicitOffOverridesDefault(t *testing.T) {
	// An explicit cost = false must override the default-on, not be ignored.
	root := writeConfig(t, `
[trailers]
cost = false
`)
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Trailers.Cost {
		t.Error("explicit cost = false should override the default")
	}
	if cfg.Format.CostPrecision != 2 {
		t.Errorf("CostPrecision = %d, want default 2 when [format] omitted", cfg.Format.CostPrecision)
	}
}

func TestLoadMalformedFileErrors(t *testing.T) {
	root := writeConfig(t, "this is not = = valid toml [[[")
	if _, err := Load(root); err == nil {
		t.Error("expected an error for malformed TOML")
	}
}
