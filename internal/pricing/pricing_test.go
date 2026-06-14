package pricing

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// testCard is a rate card with round per-Mtok prices so expected costs are easy
// to verify by hand. Each bucket has a distinct rate to catch a wired-up wrong
// bucket.
func testCard() *RateCard {
	return &RateCard{
		Version:  "test",
		Currency: "usd",
		Unit:     "per_mtok",
		Models: map[string]Rate{
			"claude-opus-4-8": {Input: 2, Output: 4, CacheRead: 1, CacheWrite5m: 3, CacheWrite1h: 5},
		},
	}
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCostUSD_EachBucket(t *testing.T) {
	rc := testCard()
	// One Mtok in a single bucket should price to exactly that bucket's rate.
	cases := []struct {
		name string
		u    Usage
		want float64
	}{
		{"input", Usage{Input: 1_000_000}, 2},
		{"output", Usage{Output: 1_000_000}, 4},
		{"cacheRead", Usage{CacheRead: 1_000_000}, 1},
		{"cacheWrite5m", Usage{CacheWrite5m: 1_000_000}, 3},
		{"cacheWrite1h", Usage{CacheWrite1h: 1_000_000}, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rc.CostUSD("claude-opus-4-8", tc.u); !approx(got, tc.want) {
				t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestCostUSD_AllBucketsSummed(t *testing.T) {
	rc := testCard()
	u := Usage{Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000, CacheWrite5m: 1_000_000, CacheWrite1h: 1_000_000}
	want := 2.0 + 4 + 1 + 3 + 5
	if got := rc.CostUSD("claude-opus-4-8", u); !approx(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCostUSD_FractionalTokens(t *testing.T) {
	rc := testCard()
	// 500k input tokens @ 2/Mtok = 1.0
	if got := rc.CostUSD("claude-opus-4-8", Usage{Input: 500_000}); !approx(got, 1.0) {
		t.Fatalf("got %v, want 1.0", got)
	}
}

func TestCostUSD_UnknownModelIsZero(t *testing.T) {
	rc := testCard()
	u := Usage{Input: 1_000_000, Output: 1_000_000}
	if got := rc.CostUSD("totally-made-up-model", u); got != 0 {
		t.Fatalf("unknown model: got %v, want 0", got)
	}
	if got := rc.CostUSD("", u); got != 0 {
		t.Fatalf("empty model: got %v, want 0", got)
	}
}

func TestCostUSD_NormalizesModelBeforeLookup(t *testing.T) {
	rc := testCard()
	u := Usage{Input: 1_000_000}
	// Prefixed / cased ids must still resolve to the bare rate-card key.
	for _, m := range []string{"claude-opus-4-8", "CLAUDE-OPUS-4-8", "anthropic/claude-opus-4-8", "us.anthropic.claude-opus-4-8", "  claude-opus-4-8  "} {
		if got := rc.CostUSD(m, u); !approx(got, 2) {
			t.Fatalf("model %q: got %v, want 2", m, got)
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"claude-opus-4-8", "claude-opus-4-8"},
		{"CLAUDE-OPUS-4-8", "claude-opus-4-8"},
		{"  Claude-Opus-4-8  ", "claude-opus-4-8"},
		{"claude-code/claude-opus-4-8", "claude-opus-4-8"},
		{"anthropic/claude-opus-4-8", "claude-opus-4-8"},
		{"us.anthropic.claude-opus-4-8", "claude-opus-4-8"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalize_OnlyLeadingPrefixStripped(t *testing.T) {
	// The prefix is stripped once at the front; an embedded occurrence stays.
	if got := Normalize("anthropic/anthropic/foo"); got != "anthropic/foo" {
		t.Fatalf("got %q, want anthropic/foo", got)
	}
}

func TestKnown(t *testing.T) {
	rc := testCard()
	if !rc.Known("CLAUDE-OPUS-4-8") {
		t.Error("Known should normalize and find the model")
	}
	if rc.Known("nope") {
		t.Error("Known should be false for an unlisted model")
	}
}

func TestLoad_ParsesCard(t *testing.T) {
	data := []byte(`{
		"version": "v1", "currency": "usd", "unit": "per_mtok",
		"models": {"m": {"input": 1, "output": 2, "cacheRead": 3, "cacheWrite5m": 4, "cacheWrite1h": 5}}
	}`)
	rc, err := Load(data)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rc.Version != "v1" || rc.Currency != "usd" || rc.Unit != "per_mtok" {
		t.Errorf("header parsed wrong: %+v", rc)
	}
	r, ok := rc.Models["m"]
	if !ok {
		t.Fatal("model m missing")
	}
	if r.Input != 1 || r.Output != 2 || r.CacheRead != 3 || r.CacheWrite5m != 4 || r.CacheWrite1h != 5 {
		t.Errorf("rate parsed wrong: %+v", r)
	}
}

func TestLoad_MalformedErrors(t *testing.T) {
	if _, err := Load([]byte("{not json")); err == nil {
		t.Error("expected an error for malformed JSON")
	}
}

// The checked-in rate card must parse and price its own listed models — guards
// against a hand-edit that breaks the embedded data file.
func TestLoad_RealRateCard(t *testing.T) {
	path := filepath.Join("..", "..", "data", "claude-pricing.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("rate card not found at %s: %v", path, err)
	}
	rc, err := Load(b)
	if err != nil {
		t.Fatalf("Load real card: %v", err)
	}
	if len(rc.Models) == 0 {
		t.Fatal("real card has no models")
	}
	for name, r := range rc.Models {
		if r.Input <= 0 || r.Output <= 0 {
			t.Errorf("model %q has non-positive base rate: %+v", name, r)
		}
		got := rc.CostUSD(name, Usage{Input: 1_000_000})
		if !approx(got, r.Input) {
			t.Errorf("model %q: 1Mtok input cost %v, want %v", name, got, r.Input)
		}
	}
}
