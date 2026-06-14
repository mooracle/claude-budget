package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mooracle/claude-budget/internal/config"
	"github.com/mooracle/claude-budget/internal/reader"
	"github.com/mooracle/claude-budget/internal/state"
)

func TestRouteTrailer(t *testing.T) {
	cases := []struct {
		source   string
		rebasing bool
		want     trailerRoute
	}{
		// normal sources, no rebase
		{"", false, routeNormal},
		{"template", false, routeNormal},
		{"message", false, routeNormal},
		// rebase guard wins over an otherwise-normal source
		{"", true, routeSum},
		{"template", true, routeSum},
		{"message", true, routeSum}, // `git rebase -i` reword
		// merge / message-reuse always clear, regardless of rebase state
		{"merge", false, routeClear},
		{"merge", true, routeClear},
		{"commit", false, routeClear},
		{"commit", true, routeClear},
		// squash always sums
		{"squash", false, routeSum},
		{"squash", true, routeSum},
	}
	for _, tc := range cases {
		if got := routeTrailer(tc.source, tc.rebasing); got != tc.want {
			t.Errorf("routeTrailer(%q, rebasing=%v) = %d, want %d", tc.source, tc.rebasing, got, tc.want)
		}
	}
}

func TestParseTrailerArgs(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantMsg    string
		wantSource string
	}{
		{"shim form", []string{"/tmp/MSG", "--source", "message"}, "/tmp/MSG", "message"},
		{"empty source value", []string{"/tmp/MSG", "--source", ""}, "/tmp/MSG", ""},
		{"no source flag", []string{"/tmp/MSG"}, "/tmp/MSG", ""},
		{"equals form", []string{"/tmp/MSG", "--source=squash"}, "/tmp/MSG", "squash"},
		{"trailing --source with no value", []string{"/tmp/MSG", "--source"}, "/tmp/MSG", ""},
		{"no args", nil, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, src := parseTrailerArgs(tc.args)
			if msg != tc.wantMsg || src != tc.wantSource {
				t.Fatalf("parseTrailerArgs(%q) = (%q, %q), want (%q, %q)", tc.args, msg, src, tc.wantMsg, tc.wantSource)
			}
		})
	}
}

// usefulResult is a single-model scan result with a real watermark.
func usefulResult() *reader.Result {
	return &reader.Result{
		Branch:       "feature",
		TotalCostUSD: 0.42,
		TotalTokens:  1000,
		Requests:     3,
		MaxTsMs:      1700000000123,
		MaxRequestID: "req-zzz",
		Models: []reader.ModelStat{
			{Model: "claude-opus-4", CostUSD: 0.42, Tokens: 1000, Requests: 3},
		},
	}
}

func TestDecideTrailer_NormalAppendAndStage(t *testing.T) {
	d := decideTrailer(usefulResult(), config.Defaults(), "feature", "my subject\n")
	if !d.changed {
		t.Fatalf("expected the message to change")
	}
	want := "my subject\n\nClaude-Cost: 0.42\n"
	if d.newMsg != want {
		t.Fatalf("newMsg = %q, want %q", d.newMsg, want)
	}
	if d.stage == nil {
		t.Fatalf("expected a staged pending watermark")
	}
	wantPend := struct {
		Branch        string
		HwmMs         int64
		LastRequestID string
	}{"feature", 1700000000123, "req-zzz"}
	if d.stage.Branch != wantPend.Branch || d.stage.HwmMs != wantPend.HwmMs || d.stage.LastRequestID != wantPend.LastRequestID {
		t.Fatalf("staged %+v, want %+v", *d.stage, wantPend)
	}
}

func TestDecideTrailer_IdempotentRerun(t *testing.T) {
	cfg := config.Defaults()
	first := decideTrailer(usefulResult(), cfg, "feature", "subject\n")
	if !first.changed {
		t.Fatalf("first run should change the message")
	}
	// Re-running prepare-commit-msg on the now-trailered message must not append
	// a second block, but it must still stage the watermark.
	second := decideTrailer(usefulResult(), cfg, "feature", first.newMsg)
	if second.changed {
		t.Fatalf("second run should be idempotent, got changed=true: %q", second.newMsg)
	}
	if second.newMsg != first.newMsg {
		t.Fatalf("idempotent run altered the message: %q != %q", second.newMsg, first.newMsg)
	}
	if second.stage == nil {
		t.Fatalf("idempotent run should still stage the watermark")
	}
}

func TestDecideTrailer_DetachedHeadNoTrailer(t *testing.T) {
	d := decideTrailer(usefulResult(), config.Defaults(), "HEAD", "subject\n")
	if d.changed {
		t.Fatalf("detached HEAD must not change the message, got %q", d.newMsg)
	}
	if d.stage != nil {
		t.Fatalf("detached HEAD must not stage a watermark, got %+v", *d.stage)
	}
}

func TestDecideTrailer_EmptyResultClearsMarker(t *testing.T) {
	// Zero requests → nothing to attribute → no change, clear the marker.
	d := decideTrailer(&reader.Result{Requests: 0}, config.Defaults(), "feature", "subject\n")
	if d.changed || d.stage != nil {
		t.Fatalf("empty result should change nothing and clear the marker, got changed=%v stage=%v", d.changed, d.stage)
	}
}

func TestDecideTrailer_TrailersDisabledClearsMarker(t *testing.T) {
	cfg := config.Defaults()
	cfg.Trailers = config.Trailers{} // every trailer off → Format yields no lines
	d := decideTrailer(usefulResult(), cfg, "feature", "subject\n")
	if d.changed || d.stage != nil {
		t.Fatalf("disabled trailers should change nothing and clear the marker, got changed=%v stage=%v", d.changed, d.stage)
	}
}

func TestAppendTrailerBlock(t *testing.T) {
	cases := []struct {
		name        string
		content     string
		lines       []string
		wantChanged bool
		want        string
	}{
		{
			name:        "plain subject",
			content:     "subject\n",
			lines:       []string{"Claude-Cost: 0.42"},
			wantChanged: true,
			want:        "subject\n\nClaude-Cost: 0.42\n",
		},
		{
			name:        "subject and body",
			content:     "subject\n\nsome body text\n",
			lines:       []string{"Claude-Cost: 0.42"},
			wantChanged: true,
			want:        "subject\n\nsome body text\n\nClaude-Cost: 0.42\n",
		},
		{
			name:        "multi-line block",
			content:     "subject\n",
			lines:       []string{"Claude-Cost: 0.42", "Claude-Tokens: 1000"},
			wantChanged: true,
			want:        "subject\n\nClaude-Cost: 0.42\nClaude-Tokens: 1000\n",
		},
		{
			name:        "idempotent single line",
			content:     "subject\n\nClaude-Cost: 0.42\n",
			lines:       []string{"Claude-Cost: 0.42"},
			wantChanged: false,
			want:        "subject\n\nClaude-Cost: 0.42\n",
		},
		{
			name:        "idempotent multi-line",
			content:     "subject\n\nClaude-Cost: 0.42\nClaude-Tokens: 1000\n",
			lines:       []string{"Claude-Cost: 0.42", "Claude-Tokens: 1000"},
			wantChanged: false,
			want:        "subject\n\nClaude-Cost: 0.42\nClaude-Tokens: 1000\n",
		},
		{
			name:        "insert before trailing comments",
			content:     "subject\n\n# Please enter the commit message\n# Lines starting with # are ignored\n",
			lines:       []string{"Claude-Cost: 0.42"},
			wantChanged: true,
			want:        "subject\n\nClaude-Cost: 0.42\n\n# Please enter the commit message\n# Lines starting with # are ignored\n",
		},
		{
			name:        "idempotent with trailing comments (amend reuse)",
			content:     "subject\n\nClaude-Cost: 0.42\n\n# Please enter the commit message\n",
			lines:       []string{"Claude-Cost: 0.42"},
			wantChanged: false,
			want:        "subject\n\nClaude-Cost: 0.42\n\n# Please enter the commit message\n",
		},
		{
			// Verbose mode ('commit -v'): the trailer must go above the scissors
			// cut line, otherwise git discards it with the diff below.
			name: "insert above verbose scissors block",
			content: "subject\n\n" +
				"# Please enter the commit message for your changes.\n" +
				"# ------------------------ >8 ------------------------\n" +
				"diff --git a/f b/f\n+change\n",
			lines:       []string{"Claude-Cost: 0.42"},
			wantChanged: true,
			want: "subject\n\nClaude-Cost: 0.42\n\n" +
				"# Please enter the commit message for your changes.\n" +
				"# ------------------------ >8 ------------------------\n" +
				"diff --git a/f b/f\n+change\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := appendTrailerBlock(tc.content, tc.lines)
			if changed != tc.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tc.wantChanged)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSplitTrailingComments(t *testing.T) {
	cases := []struct {
		name         string
		content      string
		wantBody     string
		wantComments string
	}{
		{"no comments", "subject\n\nbody\n", "subject\n\nbody\n", ""},
		{
			name:         "trailing comment block",
			content:      "subject\n\n# c1\n# c2\n",
			wantBody:     "subject\n",
			wantComments: "# c1\n# c2\n",
		},
		{
			// A '#' line jammed directly against body text (no blank line before
			// it, unlike git's template) is user content, so nothing is split off
			// even when a diff-like line follows.
			name:         "hashtag line against body with diff stays body",
			content:      "subject\n# scissors\ndiff --git a b\n",
			wantBody:     "subject\n# scissors\ndiff --git a b\n",
			wantComments: "",
		},
		{
			// git's real verbose/scissors template: comment block, the cut line,
			// then the raw diff. All of it is non-body and must split off so the
			// trailer lands above the cut line (git discards everything below it).
			name: "verbose scissors block is all comments",
			content: "subject\n\n" +
				"# Please enter the commit message for your changes.\n" +
				"# On branch main\n" +
				"# ------------------------ >8 ------------------------\n" +
				"# Do not modify or remove the line above.\n" +
				"diff --git a/f b/f\n+change\n",
			wantBody: "subject\n",
			wantComments: "# Please enter the commit message for your changes.\n" +
				"# On branch main\n" +
				"# ------------------------ >8 ------------------------\n" +
				"# Do not modify or remove the line above.\n" +
				"diff --git a/f b/f\n+change\n",
		},
		{
			// A '#' line directly following body text is the user's content (git
			// puts a blank line before its template), so it must not be split off.
			name:         "hashtag body line is not a comment",
			content:      "Fix bug\n\nSee discussion\n#offtopic note\n",
			wantBody:     "Fix bug\n\nSee discussion\n#offtopic note\n",
			wantComments: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, comments := splitTrailingComments(tc.content)
			if body != tc.wantBody || comments != tc.wantComments {
				t.Fatalf("got (body=%q, comments=%q), want (body=%q, comments=%q)", body, comments, tc.wantBody, tc.wantComments)
			}
		})
	}
}

func TestConsume_PromotesAndClears(t *testing.T) {
	gitDir := t.TempDir()
	if err := state.WritePending(gitDir, state.Pending{Branch: "feature", HwmMs: 1700000000123, LastRequestID: "req-zzz"}); err != nil {
		t.Fatalf("WritePending: %v", err)
	}
	if err := consume(gitDir, false); err != nil {
		t.Fatalf("consume: %v", err)
	}
	// State advanced for the branch.
	st, err := state.Load(gitDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := st.Branches["feature"]; got.HwmMs != 1700000000123 || got.LastRequestID != "req-zzz" {
		t.Fatalf("branch state = %+v, want {1700000000123 req-zzz}", got)
	}
	// Marker cleared.
	if _, ok, err := state.ReadPending(gitDir); err != nil || ok {
		t.Fatalf("ReadPending after consume = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

func TestConsume_NoPendingIsNoop(t *testing.T) {
	gitDir := t.TempDir()
	if err := consume(gitDir, false); err != nil {
		t.Fatalf("consume with no pending: %v", err)
	}
	// No state file written when there's nothing to promote.
	if _, err := os.Stat(filepath.Join(gitDir, "claude-budget")); !os.IsNotExist(err) {
		t.Fatalf("expected no state file, stat err = %v", err)
	}
}

func TestConsume_RebaseRetainsMarkerUntouched(t *testing.T) {
	gitDir := t.TempDir()
	pend := state.Pending{Branch: "feature", HwmMs: 42, LastRequestID: "req-a"}
	if err := state.WritePending(gitDir, pend); err != nil {
		t.Fatalf("WritePending: %v", err)
	}
	if err := consume(gitDir, true); err != nil {
		t.Fatalf("consume during rebase: %v", err)
	}
	// Marker retained exactly as staged (not read, not cleared).
	got, ok, err := state.ReadPending(gitDir)
	if err != nil || !ok {
		t.Fatalf("ReadPending = (ok=%v, err=%v), want marker present", ok, err)
	}
	if got != pend {
		t.Fatalf("pending = %+v, want %+v", got, pend)
	}
	// State untouched: no state file should have been written.
	if _, err := os.Stat(filepath.Join(gitDir, "claude-budget")); !os.IsNotExist(err) {
		t.Fatalf("rebase path must not write state, stat err = %v", err)
	}
}

func TestMoney(t *testing.T) {
	cases := []struct {
		v    float64
		want string
	}{
		{0, "$0.00"},            // exact zero stays two-decimal
		{0.10, "$0.10"},         // normal cents
		{12.5, "$12.50"},        // dollars
		{0.006, "$0.01"},        // >= 0.005 → two-decimal cents
		{0.004, "$0.004000"},    // < 0.005 → six-decimal sub-cent
		{0.000001, "$0.000001"}, // tiny usage stays visible, not rounded to $0.00
	}
	for _, tc := range cases {
		if got := money(tc.v); got != tc.want {
			t.Errorf("money(%v) = %q, want %q", tc.v, got, tc.want)
		}
	}
}

func TestEnabledTrailers(t *testing.T) {
	cases := []struct {
		name string
		tr   config.Trailers
		want string
	}{
		{"default (cost only)", config.Trailers{Cost: true}, "cost"},
		{"none", config.Trailers{}, "(none)"},
		{"subset", config.Trailers{Cost: true, Tokens: true}, "cost, tokens"},
		{
			"all on keeps declaration order",
			config.Trailers{Cost: true, CostModels: true, Tokens: true, TokensModels: true, Interactions: true},
			"cost, costModels, tokens, tokensModels, interactions",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Trailers: tc.tr}
			if got := enabledTrailers(cfg); got != tc.want {
				t.Errorf("enabledTrailers(%+v) = %q, want %q", tc.tr, got, tc.want)
			}
		})
	}
}

// Guard the staged watermark fields against accidental reordering/rename.
func TestDecideTrailer_StagePayloadShape(t *testing.T) {
	res := usefulResult()
	d := decideTrailer(res, config.Defaults(), "feature", "s\n")
	got := []any{d.stage.Branch, d.stage.HwmMs, d.stage.LastRequestID}
	want := []any{"feature", int64(1700000000123), "req-zzz"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stage payload = %v, want %v", got, want)
	}
}
