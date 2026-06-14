package reader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooracle/claude-budget/internal/pricing"
)

// fixed timestamps used across the cases; ms(t, …) yields their UnixMilli.
const (
	ts1 = "2026-06-14T10:00:00Z"
	ts2 = "2026-06-14T11:00:00Z"
	ts3 = "2026-06-14T12:00:00Z"
)

func ms(t *testing.T, s string) int64 {
	t.Helper()
	tm, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm.UnixMilli()
}

func parseTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm
}

// recJSON builds one real-shaped transcript record line.
func recJSON(ts, cwd, branch, reqID, msgID, model string, input, output int64) string {
	m := map[string]any{
		"timestamp": ts,
		"cwd":       cwd,
		"gitBranch": branch,
		"requestId": reqID,
		"message": map[string]any{
			"id":    msgID,
			"model": model,
			"usage": map[string]any{
				"input_tokens":  input,
				"output_tokens": output,
			},
		},
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// writeJSONL writes a project dir <projectsDir>/<project>/<file>.jsonl containing
// the given lines, optionally stamping the file mtime.
func writeJSONL(t *testing.T, projectsDir, project, file string, lines []string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(projectsDir, project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, file)
	body := ""
	for i, l := range lines {
		if i > 0 {
			body += "\n"
		}
		body += l
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}
	return path
}

func testCard() *pricing.RateCard {
	return &pricing.RateCard{
		Models: map[string]pricing.Rate{"claude-opus-4-8": {Input: 1, Output: 1}},
	}
}

func TestScan_BasicAggregateAndWatermark(t *testing.T) {
	projects := t.TempDir()
	repo := t.TempDir()
	// Two distinct requests on the same branch, plus a noise line with no usage
	// and a malformed line — both must be ignored.
	lines := []string{
		recJSON(ts1, repo, "main", "r1", "r1-id", "claude-opus-4-8", 100, 50),
		`{"type":"summary","note":"no usage here"}`,
		"{ this is not valid json",
		recJSON(ts3, repo, "main", "r2", "r2-id", "claude-opus-4-8", 200, 100),
	}
	writeJSONL(t, projects, "proj", "a.jsonl", lines, time.Time{})

	res, err := Scan(projects, repo, "main", 0, testCard())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Branch != "main" {
		t.Errorf("branch = %q, want main", res.Branch)
	}
	if res.Requests != 2 {
		t.Errorf("requests = %d, want 2", res.Requests)
	}
	if res.TotalTokens != 450 {
		t.Errorf("tokens = %d, want 450", res.TotalTokens)
	}
	if res.TotalCostUSD <= 0 {
		t.Errorf("cost = %v, want > 0", res.TotalCostUSD)
	}
	if res.MaxTsMs != ms(t, ts3) {
		t.Errorf("MaxTsMs = %d, want %d", res.MaxTsMs, ms(t, ts3))
	}
	if res.MaxRequestID != "r2" {
		t.Errorf("MaxRequestID = %q, want r2", res.MaxRequestID)
	}
}

func TestScan_DedupByRequestID(t *testing.T) {
	projects := t.TempDir()
	repo := t.TempDir()
	// Same requestId, streaming partials — only the max-output record survives.
	lines := []string{
		recJSON(ts1, repo, "main", "r1", "r1-id", "claude-opus-4-8", 100, 10),
		recJSON(ts1, repo, "main", "r1", "r1-id", "claude-opus-4-8", 100, 50),
	}
	writeJSONL(t, projects, "proj", "a.jsonl", lines, time.Time{})

	res, err := Scan(projects, repo, "main", 0, testCard())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Requests != 1 {
		t.Fatalf("requests = %d, want 1 (deduped)", res.Requests)
	}
	if res.TotalTokens != 150 { // 100 input + 50 output (the larger record)
		t.Errorf("tokens = %d, want 150 (max-output record)", res.TotalTokens)
	}
}

func TestScan_DedupFallsBackToMessageID(t *testing.T) {
	projects := t.TempDir()
	repo := t.TempDir()
	// Empty requestId → message.id is the dedup key.
	lines := []string{
		recJSON(ts1, repo, "main", "", "shared-msg", "claude-opus-4-8", 100, 10),
		recJSON(ts1, repo, "main", "", "shared-msg", "claude-opus-4-8", 100, 40),
	}
	writeJSONL(t, projects, "proj", "a.jsonl", lines, time.Time{})

	res, err := Scan(projects, repo, "main", 0, testCard())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Requests != 1 {
		t.Fatalf("requests = %d, want 1", res.Requests)
	}
	if res.TotalTokens != 140 {
		t.Errorf("tokens = %d, want 140", res.TotalTokens)
	}
}

func TestScan_BranchFilter(t *testing.T) {
	projects := t.TempDir()
	repo := t.TempDir()
	lines := []string{
		recJSON(ts1, repo, "main", "r1", "r1-id", "claude-opus-4-8", 100, 50),
		recJSON(ts2, repo, "other", "r2", "r2-id", "claude-opus-4-8", 999, 999),
		recJSON(ts3, repo, "main", "r3", "r3-id", "claude-opus-4-8", 10, 5),
	}
	writeJSONL(t, projects, "proj", "a.jsonl", lines, time.Time{})

	res, err := Scan(projects, repo, "main", 0, testCard())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Requests != 2 {
		t.Errorf("requests = %d, want 2 (only main)", res.Requests)
	}
	if res.TotalTokens != 165 { // 150 + 15; the "other" branch record excluded
		t.Errorf("tokens = %d, want 165", res.TotalTokens)
	}
}

func TestScan_HwmCutoff(t *testing.T) {
	projects := t.TempDir()
	repo := t.TempDir()
	lines := []string{
		recJSON(ts1, repo, "main", "old", "old-id", "claude-opus-4-8", 100, 50),
		recJSON(ts3, repo, "main", "new", "new-id", "claude-opus-4-8", 10, 5),
	}
	// File mtime after the hwm so the file survives the prune; the per-record
	// cutoff then drops only the ts1 record (ts1 <= hwm).
	writeJSONL(t, projects, "proj", "a.jsonl", lines, parseTime(t, ts3))

	res, err := Scan(projects, repo, "main", ms(t, ts1), testCard())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Requests != 1 {
		t.Fatalf("requests = %d, want 1 (ts1 cut off)", res.Requests)
	}
	if res.TotalTokens != 15 {
		t.Errorf("tokens = %d, want 15", res.TotalTokens)
	}
	if res.MaxRequestID != "new" {
		t.Errorf("MaxRequestID = %q, want new", res.MaxRequestID)
	}
}

func TestScan_CwdMembershipIncludingSubdir(t *testing.T) {
	projects := t.TempDir()
	repo := t.TempDir()
	outside := t.TempDir()
	sub := filepath.Join(repo, "pkg", "inner")

	writeJSONL(t, projects, "at-root", "a.jsonl",
		[]string{recJSON(ts1, repo, "main", "r1", "r1-id", "claude-opus-4-8", 100, 0)}, time.Time{})
	writeJSONL(t, projects, "in-subdir", "b.jsonl",
		[]string{recJSON(ts1, sub, "main", "r2", "r2-id", "claude-opus-4-8", 50, 0)}, time.Time{})
	writeJSONL(t, projects, "elsewhere", "c.jsonl",
		[]string{recJSON(ts1, outside, "main", "r3", "r3-id", "claude-opus-4-8", 9999, 0)}, time.Time{})

	res, err := Scan(projects, repo, "main", 0, testCard())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Requests != 2 {
		t.Errorf("requests = %d, want 2 (root + subdir, not outside)", res.Requests)
	}
	if res.TotalTokens != 150 {
		t.Errorf("tokens = %d, want 150 (outside repo excluded)", res.TotalTokens)
	}
}

func TestScan_MtimePrunesWholeFile(t *testing.T) {
	projects := t.TempDir()
	repo := t.TempDir()
	hwm := ms(t, ts2)

	// stale.jsonl mtime predates the hwm → pruned entirely, even though its
	// record's timestamp (ts3) is after the hwm.
	writeJSONL(t, projects, "proj", "stale.jsonl",
		[]string{recJSON(ts3, repo, "main", "stale", "stale-id", "claude-opus-4-8", 1000, 0)},
		parseTime(t, ts1))
	// fresh.jsonl mtime is after the hwm → scanned.
	writeJSONL(t, projects, "proj", "fresh.jsonl",
		[]string{recJSON(ts3, repo, "main", "fresh", "fresh-id", "claude-opus-4-8", 20, 0)},
		parseTime(t, ts3))

	res, err := Scan(projects, repo, "main", hwm, testCard())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Requests != 1 {
		t.Fatalf("requests = %d, want 1 (stale file pruned)", res.Requests)
	}
	if res.MaxRequestID != "fresh" {
		t.Errorf("MaxRequestID = %q, want fresh", res.MaxRequestID)
	}
	if res.TotalTokens != 20 {
		t.Errorf("tokens = %d, want 20", res.TotalTokens)
	}
}

func TestScan_ModelNormalizationAggregates(t *testing.T) {
	projects := t.TempDir()
	repo := t.TempDir()
	// A prefixed model id must aggregate under the same normalized key.
	lines := []string{
		recJSON(ts1, repo, "main", "r1", "r1-id", "claude-opus-4-8", 100, 0),
		recJSON(ts2, repo, "main", "r2", "r2-id", "anthropic/claude-opus-4-8", 100, 0),
	}
	writeJSONL(t, projects, "proj", "a.jsonl", lines, time.Time{})

	res, err := Scan(projects, repo, "main", 0, testCard())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Models) != 1 {
		t.Fatalf("models = %d, want 1 (normalized to one key)", len(res.Models))
	}
	if res.Models[0].Model != "claude-opus-4-8" {
		t.Errorf("model = %q, want claude-opus-4-8", res.Models[0].Model)
	}
	if res.Models[0].Requests != 2 {
		t.Errorf("model requests = %d, want 2", res.Models[0].Requests)
	}
}

func TestScan_MissingProjectsDir(t *testing.T) {
	res, err := Scan(filepath.Join(t.TempDir(), "does-not-exist"), t.TempDir(), "main", 0, testCard())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Requests != 0 || len(res.Models) != 0 {
		t.Errorf("expected empty result, got %+v", res)
	}
}

// toUsage carries the bulk of a real transcript's cost (cache tokens dominate),
// so its bucket attribution is verified directly across the tiered, legacy, and
// zero-tier-fallback shapes.
func TestToUsage_CacheBucketAttribution(t *testing.T) {
	cases := []struct {
		name string
		in   *usage
		want pricing.Usage
	}{
		{
			name: "split 5m/1h tiers map to their own buckets",
			in: &usage{
				Input:     10,
				Output:    20,
				CacheRead: 30,
				// CacheCreate is ignored once the tier breakdown is present.
				CacheCreate: 999,
				CacheTiers: &struct {
					E5m int64 `json:"ephemeral_5m_input_tokens"`
					E1h int64 `json:"ephemeral_1h_input_tokens"`
				}{E5m: 40, E1h: 50},
			},
			want: pricing.Usage{Input: 10, Output: 20, CacheRead: 30, CacheWrite5m: 40, CacheWrite1h: 50},
		},
		{
			name: "legacy total (no tier breakdown) attributes all to 1h",
			in:   &usage{Input: 1, Output: 2, CacheRead: 3, CacheCreate: 70},
			want: pricing.Usage{Input: 1, Output: 2, CacheRead: 3, CacheWrite1h: 70},
		},
		{
			name: "zero-sum tier breakdown falls back to the legacy total",
			in: &usage{
				Input:       5,
				CacheCreate: 80,
				CacheTiers: &struct {
					E5m int64 `json:"ephemeral_5m_input_tokens"`
					E1h int64 `json:"ephemeral_1h_input_tokens"`
				}{E5m: 0, E1h: 0},
			},
			want: pricing.Usage{Input: 5, CacheWrite1h: 80},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toUsage(tc.in); got != tc.want {
				t.Fatalf("toUsage = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestScan_EmptyResultWhenNoMatches(t *testing.T) {
	projects := t.TempDir()
	repo := t.TempDir()
	writeJSONL(t, projects, "proj", "a.jsonl",
		[]string{recJSON(ts1, repo, "other-branch", "r1", "r1-id", "claude-opus-4-8", 100, 0)}, time.Time{})

	res, err := Scan(projects, repo, "main", 0, testCard())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Requests != 0 {
		t.Errorf("requests = %d, want 0", res.Requests)
	}
	if res.MaxTsMs != 0 {
		t.Errorf("MaxTsMs = %d, want 0", res.MaxTsMs)
	}
}
