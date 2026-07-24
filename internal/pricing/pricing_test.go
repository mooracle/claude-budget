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
	// Prefixed / cased / suffixed ids must still resolve to the bare rate-card key.
	for _, m := range []string{"claude-opus-4-8", "CLAUDE-OPUS-4-8", "anthropic/claude-opus-4-8", "us.anthropic.claude-opus-4-8", "  claude-opus-4-8  ", "claude-opus-4-8[1m]", "claude-opus-4-8-20251001"} {
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
		// Context-window tag and dated snapshot — both appear in real transcripts
		// and must resolve to the bare rate-card key.
		{"claude-opus-4-8[1m]", "claude-opus-4-8"},
		{"CLAUDE-OPUS-4-8[1M]", "claude-opus-4-8"},
		{"anthropic/claude-opus-4-8[1m]", "claude-opus-4-8"},
		{"claude-haiku-4-5-20251001", "claude-haiku-4-5"},
		{"claude-haiku-4-5-20251001[1m]", "claude-haiku-4-5"},
		// A bare version tail ("-4-5", a single digit) is not a date snapshot.
		{"claude-haiku-4-5", "claude-haiku-4-5"},
		// Embedded CR/LF are stripped so a model id can't split a -Models trailer
		// across commit-message lines.
		{"claude-opus-4-8\ninjected-trailer: 999", "claude-opus-4-8injected-trailer: 999"},
		{"claude-\ropus-4-8", "claude-opus-4-8"},
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

// fallbackCard extends testCard with a families table, so an unlisted model is
// estimated rather than priced to 0.
func fallbackCard() *RateCard {
	rc := testCard()
	rc.Models["claude-haiku-4-5"] = Rate{Input: 1, Output: 2}
	rc.Models["claude-fable-5"] = Rate{Input: 10, Output: 20}
	rc.Fallbacks = map[string]string{
		"claude-opus":  "claude-opus-4-8",
		"claude-haiku": "claude-haiku-4-5",
		"":             "claude-fable-5",
	}
	return rc
}

func TestPriced_ExactHitBeatsFallback(t *testing.T) {
	r, src, exact := fallbackCard().Priced("claude-opus-4-8")
	if !exact || src != "claude-opus-4-8" || r.Input != 2 {
		t.Fatalf("got (%v, %q, %v), want the model's own rate", r, src, exact)
	}
}

// The case that motivated fallbacks: a model released after this binary was
// built inherits its family's current generation instead of costing nothing.
func TestPriced_UnlistedModelBorrowsFromItsFamily(t *testing.T) {
	rc := fallbackCard()
	for _, m := range []string{"claude-opus-6", "claude-opus-9-1", "CLAUDE-OPUS-6[1m]"} {
		r, src, exact := rc.Priced(m)
		if exact {
			t.Errorf("%q: reported exact, want an estimate", m)
		}
		if src != "claude-opus-4-8" || r.Input != 2 {
			t.Errorf("%q: priced via %q (input %v), want claude-opus-4-8 (2)", m, src, r.Input)
		}
	}
	// And the estimate reaches the money: 1Mtok input at the borrowed $2 rate.
	if got := rc.CostUSD("claude-opus-6", Usage{Input: 1_000_000}); !approx(got, 2) {
		t.Errorf("CostUSD = %v, want 2 (estimated, not 0)", got)
	}
}

func TestPriced_UnknownFamilyUsesDefault(t *testing.T) {
	r, src, exact := fallbackCard().Priced("claude-atlas-1")
	if exact || src != "claude-fable-5" || r.Input != 10 {
		t.Fatalf("got (%v, %q, %v), want the default fallback claude-fable-5", r, src, exact)
	}
}

// A family prefix must match a whole segment, so "claude-opusx-1" is a new
// family rather than an Opus.
func TestPriced_FamilyPrefixMatchesWholeSegment(t *testing.T) {
	if _, src, _ := fallbackCard().Priced("claude-opusx-1"); src != "claude-fable-5" {
		t.Fatalf("priced via %q, want the default (not the claude-opus family)", src)
	}
}

// An empty id is corrupt input, not a new release — it must never borrow a rate,
// or a malformed transcript line would silently bill at the default.
func TestPriced_EmptyModelNeverBorrows(t *testing.T) {
	r, src, exact := fallbackCard().Priced("")
	if exact || src != "" || r.Input != 0 {
		t.Fatalf("got (%v, %q, %v), want no rate at all", r, src, exact)
	}
}

// Estimation is opt-in per card: with no fallbacks table, the pre-fallback
// behavior (unknown model → 0) is preserved exactly.
func TestPriced_NoFallbacksConfiguredStaysZero(t *testing.T) {
	if _, _, exact := testCard().Priced("claude-opus-6"); exact {
		t.Error("reported exact for an unlisted model")
	}
	if got := testCard().CostUSD("claude-opus-6", Usage{Input: 1_000_000}); got != 0 {
		t.Errorf("CostUSD = %v, want 0 when no fallbacks are configured", got)
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
	// A floor, not an exact count: the card only grows in normal operation, and
	// a truncating edit (a bad merge, a mangled jq filter) would otherwise leave
	// a card that still parses while silently pricing most usage by fallback.
	// Lower this deliberately if models are ever genuinely retired.
	const minModels = 12
	if len(rc.Models) < minModels {
		t.Fatalf("real card has %d models, want >= %d — did an edit drop some?",
			len(rc.Models), minModels)
	}

	// The families the tool is expected to price outright rather than estimate.
	// Losing one is invisible at runtime: usage just starts resolving through
	// `fallbacks` instead, at a neighbouring model's rate.
	for _, must := range []string{
		"claude-opus-5", "claude-opus-4-8", "claude-sonnet-5", "claude-haiku-4-5",
		"claude-fable-5",
	} {
		if _, ok := rc.Models[must]; !ok {
			t.Errorf("real card is missing %q — its usage would be estimated, not priced", must)
		}
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

	// Every fallback must point at a model the card actually carries. A stale or
	// typo'd target resolves to no rate, which would silently restore the very
	// "new model costs $0.00" bug the table exists to prevent.
	if len(rc.Fallbacks) == 0 {
		t.Fatal("real card has no fallbacks — a new model would price to 0")
	}
	if _, ok := rc.Fallbacks[""]; !ok {
		t.Error(`no "" default fallback — an unrecognized family would price to 0`)
	}
	for fam, target := range rc.Fallbacks {
		if _, ok := rc.Models[target]; !ok {
			t.Errorf("fallback %q → %q, which is not a model in this card", fam, target)
		}
	}

	// A yet-to-be-released member of each known family must price above zero.
	for fam := range rc.Fallbacks {
		if fam == "" {
			continue
		}
		next := fam + "-99"
		if got := rc.CostUSD(next, Usage{Input: 1_000_000}); got <= 0 {
			t.Errorf("hypothetical %q priced to %v, want a positive estimate", next, got)
		}
	}
}
