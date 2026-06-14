package trailer

import (
	"reflect"
	"testing"

	"github.com/mooracle/claude-budget/internal/config"
	"github.com/mooracle/claude-budget/internal/reader"
)

// sampleResult is a two-model result already ordered by cost descending (as the
// reader returns it).
func sampleResult() *reader.Result {
	return &reader.Result{
		Branch:       "main",
		TotalCostUSD: 0.426,
		TotalTokens:  1500,
		Requests:     7,
		Models: []reader.ModelStat{
			{Model: "claude-opus-4", CostUSD: 0.4, Tokens: 1000, Requests: 5},
			{Model: "claude-haiku-4", CostUSD: 0.026, Tokens: 500, Requests: 2},
		},
	}
}

func TestFormat_DefaultCostOnly(t *testing.T) {
	got := Format(sampleResult(), config.Defaults())
	want := []string{"Claude-Cost: 0.43"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormat_AllTrailersOn(t *testing.T) {
	cfg := config.Defaults()
	cfg.Trailers = config.Trailers{
		Cost: true, CostModels: true, Tokens: true, TokensModels: true, Interactions: true,
	}
	got := Format(sampleResult(), cfg)
	want := []string{
		"Claude-Cost: 0.43",
		"Claude-Cost-Models: claude-opus-4=0.40,claude-haiku-4=0.03",
		"Claude-Tokens: 1500",
		"Claude-Tokens-Models: claude-opus-4=1000,claude-haiku-4=500",
		"Claude-Interactions: 7",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestFormat_EachTrailerIndependently(t *testing.T) {
	cases := []struct {
		name string
		set  config.Trailers
		want []string
	}{
		{"cost", config.Trailers{Cost: true}, []string{"Claude-Cost: 0.43"}},
		{"costModels", config.Trailers{CostModels: true}, []string{"Claude-Cost-Models: claude-opus-4=0.40,claude-haiku-4=0.03"}},
		{"tokens", config.Trailers{Tokens: true}, []string{"Claude-Tokens: 1500"}},
		{"tokensModels", config.Trailers{TokensModels: true}, []string{"Claude-Tokens-Models: claude-opus-4=1000,claude-haiku-4=500"}},
		{"interactions", config.Trailers{Interactions: true}, []string{"Claude-Interactions: 7"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Trailers = tc.set
			got := Format(sampleResult(), cfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormat_AllOff(t *testing.T) {
	cfg := config.Defaults()
	cfg.Trailers = config.Trailers{} // every flag false
	if got := Format(sampleResult(), cfg); len(got) != 0 {
		t.Fatalf("expected no lines, got %q", got)
	}
}

func TestFormat_PrecisionRounding(t *testing.T) {
	res := sampleResult()
	cases := []struct {
		prec int
		want string
	}{
		{0, "Claude-Cost: 0"},
		{2, "Claude-Cost: 0.43"},
		{4, "Claude-Cost: 0.4260"},
	}
	for _, tc := range cases {
		cfg := config.Defaults()
		cfg.Format.CostPrecision = tc.prec
		got := Format(res, cfg)
		if len(got) != 1 || got[0] != tc.want {
			t.Fatalf("prec=%d: got %q, want [%q]", tc.prec, got, tc.want)
		}
	}
}

func TestFormat_NegativePrecisionClampsToZero(t *testing.T) {
	cfg := config.Defaults()
	cfg.Format.CostPrecision = -3
	got := Format(sampleResult(), cfg)
	want := []string{"Claude-Cost: 0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormat_Rename(t *testing.T) {
	cfg := config.Defaults()
	cfg.Trailers = config.Trailers{Cost: true, CostModels: true}
	cfg.Format.Rename = map[string]string{
		"cost":       "AI-Cost",
		"costModels": "AI-Cost-Models",
	}
	got := Format(sampleResult(), cfg)
	want := []string{
		"AI-Cost: 0.43",
		"AI-Cost-Models: claude-opus-4=0.40,claude-haiku-4=0.03",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// An empty rename value is ignored — fall back to the default name.
func TestFormat_EmptyRenameFallsBack(t *testing.T) {
	cfg := config.Defaults()
	cfg.Format.Rename = map[string]string{"cost": ""}
	got := Format(sampleResult(), cfg)
	want := []string{"Claude-Cost: 0.43"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormat_MultiModelOrderingStable(t *testing.T) {
	res := &reader.Result{
		TotalCostUSD: 0.6,
		Requests:     6,
		Models: []reader.ModelStat{
			{Model: "model-a", CostUSD: 0.3, Tokens: 300},
			{Model: "model-b", CostUSD: 0.2, Tokens: 200},
			{Model: "model-c", CostUSD: 0.1, Tokens: 100},
		},
	}
	cfg := config.Defaults()
	cfg.Trailers = config.Trailers{CostModels: true, TokensModels: true}
	want := []string{
		"Claude-Cost-Models: model-a=0.30,model-b=0.20,model-c=0.10",
		"Claude-Tokens-Models: model-a=300,model-b=200,model-c=100",
	}
	// Format must not mutate input order; run twice to confirm determinism.
	for i := 0; i < 2; i++ {
		got := Format(res, cfg)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: got %q, want %q", i, got, want)
		}
	}
}

func TestFormat_EmptyResult(t *testing.T) {
	cfg := config.Defaults()
	cfg.Trailers = config.Trailers{Cost: true, Tokens: true, Interactions: true}

	if got := Format(&reader.Result{Requests: 0}, cfg); len(got) != 0 {
		t.Fatalf("zero requests: expected no lines, got %q", got)
	}
	if got := Format(nil, cfg); len(got) != 0 {
		t.Fatalf("nil result: expected no lines, got %q", got)
	}
}

func TestFormat_NilConfigUsesDefaults(t *testing.T) {
	got := Format(sampleResult(), nil)
	want := []string{"Claude-Cost: 0.43"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestName(t *testing.T) {
	cfg := config.Defaults()
	if n := Name(cfg, KeyCost); n != "Claude-Cost" {
		t.Fatalf("default cost name: got %q", n)
	}
	cfg.Format.Rename = map[string]string{"cost": "AI-Cost"}
	if n := Name(cfg, KeyCost); n != "AI-Cost" {
		t.Fatalf("renamed cost name: got %q", n)
	}
	if n := Name(cfg, KeyTokens); n != "Claude-Tokens" {
		t.Fatalf("unrenamed tokens name: got %q", n)
	}
	if n := Name(nil, KeyInteractions); n != "Claude-Interactions" {
		t.Fatalf("nil cfg: got %q", n)
	}
}
