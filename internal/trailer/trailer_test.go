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

func TestSumDuplicates_TwoCostLines(t *testing.T) {
	in := []string{
		"reworded subject",
		"",
		"Claude-Cost: 0.43",
		"Claude-Cost: 0.50",
	}
	got := SumDuplicates(in, "Claude-Cost")
	want := []string{
		"reworded subject",
		"",
		"Claude-Cost: 0.93",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestSumDuplicates_ThreeLinesKeepsMaxPrecision(t *testing.T) {
	in := []string{
		"Claude-Cost: 0.10",
		"Claude-Cost: 0.2000",
		"Claude-Cost: 0.30",
	}
	got := SumDuplicates(in, "Claude-Cost")
	want := []string{"Claude-Cost: 0.6000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// The summed line lands at the first duplicate's position; later duplicates are
// dropped while any interleaved non-cost lines stay put.
func TestSumDuplicates_PositionAndInterleaving(t *testing.T) {
	in := []string{
		"Claude-Cost: 0.40",
		"Claude-Interactions: 7",
		"Claude-Cost: 0.20",
	}
	got := SumDuplicates(in, "Claude-Cost")
	want := []string{
		"Claude-Cost: 0.60",
		"Claude-Interactions: 7",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// A renamed cost trailer must sum under its new name.
func TestSumDuplicates_RenamedTrailer(t *testing.T) {
	in := []string{
		"AI-Cost: 0.10",
		"AI-Cost: 0.15",
	}
	got := SumDuplicates(in, "AI-Cost")
	want := []string{"AI-Cost: 0.25"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// The -Models aggregate shares the cost prefix but not the exact name, and any
// non-numeric value under the cost name is left untouched.
func TestSumDuplicates_NonNumericAndModelsUntouched(t *testing.T) {
	in := []string{
		"Claude-Cost: 0.40",
		"Claude-Cost-Models: claude-opus-4=0.40",
		"Claude-Cost: 0.20",
		"Claude-Cost: n/a",
	}
	got := SumDuplicates(in, "Claude-Cost")
	want := []string{
		"Claude-Cost: 0.60",
		"Claude-Cost-Models: claude-opus-4=0.40",
		"Claude-Cost: n/a",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// Fewer than two numeric matches is a no-op: zero, one, or one numeric + one
// non-numeric all return the input unchanged.
func TestSumDuplicates_NoCollapseBelowTwo(t *testing.T) {
	cases := [][]string{
		{"subject", "", "Claude-Tokens: 1500"},
		{"Claude-Cost: 0.43"},
		{"Claude-Cost: 0.43", "Claude-Cost: oops"},
	}
	for _, in := range cases {
		got := SumDuplicates(in, "Claude-Cost")
		if !reflect.DeepEqual(got, in) {
			t.Fatalf("expected unchanged for %#v, got %#v", in, got)
		}
	}
}

// SumDuplicates is pure: it must not mutate its input slice and must be
// deterministic across repeated calls.
func TestSumDuplicates_PureNoMutation(t *testing.T) {
	in := []string{"Claude-Cost: 0.40", "Claude-Cost: 0.20"}
	orig := append([]string(nil), in...)
	first := SumDuplicates(in, "Claude-Cost")
	if !reflect.DeepEqual(in, orig) {
		t.Fatalf("input mutated: got %#v, want %#v", in, orig)
	}
	second := SumDuplicates(in, "Claude-Cost")
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("non-deterministic: %#v vs %#v", first, second)
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
