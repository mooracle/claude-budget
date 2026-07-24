// Package reader scans Claude Code session transcripts and aggregates the
// not-yet-consumed token usage for the current repo and branch.
//
// Pipeline: enumerate ~/.claude/projects/* → repo membership via the cwd field →
// collect every transcript under the project dir at any depth (main session files
// plus nested subagent and workflow-agent turns) → per-file mtime prune → scan
// survivors → keep gitBranch==branch AND timestamp>hwm → dedup by requestId
// (streaming partials share an id) → sum the five token buckets per model and
// price.
//
// Recursion matters: Claude Code writes subagent turns (the Task tool) to
// <session>/subagents/agent-*.jsonl and workflow-agent turns to
// <session>/subagents/workflows/wf_*/agent-*.jsonl, not the top level. Those
// records carry the same cwd, gitBranch, requestId, and usage as the main agent,
// so a workflow-heavy repo whose subagents do most of the work would otherwise
// undercount its cost several-fold.
package reader

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mooracle/claude-budget/internal/pricing"
)

// transcript record (only the fields we need)
type record struct {
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
	GitBranch string `json:"gitBranch"`
	RequestID string `json:"requestId"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *usage `json:"usage"`
	} `json:"message"`
}

type usage struct {
	Input       int64 `json:"input_tokens"`
	Output      int64 `json:"output_tokens"`
	CacheRead   int64 `json:"cache_read_input_tokens"`
	CacheCreate int64 `json:"cache_creation_input_tokens"`
	CacheTiers  *struct {
		E5m int64 `json:"ephemeral_5m_input_tokens"`
		E1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

// ModelStat is one model's summed, priced usage over the scan window.
type ModelStat struct {
	Model    string
	Usage    pricing.Usage
	Requests int
	Tokens   int64
	CostUSD  float64
	// PricedAs names the rate-card key CostUSD was estimated from when the model
	// has no rate of its own, and is empty for an exact hit (or when nothing
	// priced it at all). Only `status` surfaces this; trailers stay a plain
	// number so downstream parsers keep working.
	PricedAs string
}

// Result is the aggregate the reader returns.
type Result struct {
	Branch       string
	Models       []ModelStat
	TotalCostUSD float64
	TotalTokens  int64
	Requests     int
	MaxTsMs      int64  // watermark to stage on commit
	MaxRequestID string // request id at the watermark, persisted alongside MaxTsMs
}

type dedupEntry struct {
	model string
	tsMs  int64
	reqID string
	u     pricing.Usage
	out   int64 // largest output wins → the final (non-partial) record
}

var usageMarker = []byte(`"usage"`)

// maxLineBytes caps a single transcript line for the bufio scanners. Transcript
// lines can be large (pasted files, embedded images), so both scan loops —
// scanFile (usage) and firstCwd (repo membership) — must use the same cap;
// otherwise a project whose first line falls between the two limits has its cwd
// detection fail and the whole directory's usage is silently dropped.
const maxLineBytes = 64 * 1024 * 1024

// Scan walks projectsDir and returns the current branch's usage since hwmMs.
func Scan(projectsDir, repoRoot, branch string, hwmMs int64, rc *pricing.RateCard) (*Result, error) {
	res := &Result{Branch: branch}
	dirs, err := os.ReadDir(projectsDir)
	if os.IsNotExist(err) {
		return res, nil
	}
	if err != nil {
		return nil, err
	}

	best := map[string]dedupEntry{}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dirPath := filepath.Join(projectsDir, d.Name())
		// Decide repo membership from the cheap top-level session files first, so an
		// unrelated project is rejected without walking its (possibly large) nested
		// subagent/workflow tree. All transcripts under one encoded project dir share
		// the same cwd, so the top level is a sufficient probe.
		if !underRepo(firstCwd(topLevelJSONL(dirPath)), repoRoot) {
			continue
		}
		// Under this repo: scan every transcript at any depth (main session files
		// plus nested subagent/workflow-agent turns), mtime-pruned against the hwm.
		for _, fp := range collectTranscripts(dirPath, hwmMs) {
			scanFile(fp, branch, hwmMs, best)
		}
	}

	models := map[string]*ModelStat{}
	for _, e := range best {
		key := pricing.Normalize(e.model)
		ms := models[key]
		if ms == nil {
			ms = &ModelStat{Model: key}
			models[key] = ms
		}
		ms.Usage.Input += e.u.Input
		ms.Usage.Output += e.u.Output
		ms.Usage.CacheRead += e.u.CacheRead
		ms.Usage.CacheWrite5m += e.u.CacheWrite5m
		ms.Usage.CacheWrite1h += e.u.CacheWrite1h
		ms.Requests++
		if e.tsMs > res.MaxTsMs {
			res.MaxTsMs = e.tsMs
			res.MaxRequestID = e.reqID
		}
	}
	for _, ms := range models {
		ms.CostUSD = rc.CostUSD(ms.Model, ms.Usage)
		if _, src, exact := rc.Priced(ms.Model); !exact {
			ms.PricedAs = src
		}
		ms.Tokens = ms.Usage.Input + ms.Usage.Output + ms.Usage.CacheRead + ms.Usage.CacheWrite5m + ms.Usage.CacheWrite1h
		res.Models = append(res.Models, *ms)
		res.TotalCostUSD += ms.CostUSD
		res.TotalTokens += ms.Tokens
		res.Requests += ms.Requests
	}
	// Sort by cost descending, with model name as a deterministic tiebreaker so
	// the -Models line renders identically every run. Without the tiebreaker,
	// equal-cost models (e.g. several unknown models priced to 0) would inherit
	// the randomized map-iteration order, and a reordered line would defeat the
	// idempotency check and append a duplicate trailer block.
	sort.Slice(res.Models, func(i, j int) bool {
		if res.Models[i].CostUSD != res.Models[j].CostUSD {
			return res.Models[i].CostUSD > res.Models[j].CostUSD
		}
		return res.Models[i].Model < res.Models[j].Model
	})
	return res, nil
}

// topLevelJSONL returns the .jsonl files directly under dirPath (no recursion) —
// the main-agent session transcripts. It probes repo membership cheaply, before
// deciding whether the full nested tree is worth walking.
func topLevelJSONL(dirPath string) []string {
	entries, _ := os.ReadDir(dirPath)
	var out []string
	for _, f := range entries {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
			continue
		}
		out = append(out, filepath.Join(dirPath, f.Name()))
	}
	return out
}

// collectTranscripts returns every .jsonl transcript under dirPath at any depth,
// mtime-pruned against hwmMs. Recursion is what captures subagent and
// workflow-agent usage: Claude Code writes those turns under <session>/subagents/
// and <session>/subagents/workflows/wf_*/, not the top level. Non-.jsonl siblings
// (tool-results/*.txt, workflows/*.json, *.meta.json) are skipped by the suffix
// test. The mtime prune stays per-file so a workflow-heavy tree of thousands of
// old agent files is stat-ed but not reopened once its window has been consumed.
func collectTranscripts(dirPath string, hwmMs int64) []string {
	var out []string
	filepath.WalkDir(dirPath, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip it, keep walking siblings
		}
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			return nil
		}
		if hwmMs > 0 {
			if info, err := e.Info(); err == nil && info.ModTime().UnixMilli() <= hwmMs {
				return nil // prune: every record in this file predates the baseline
			}
		}
		out = append(out, p)
		return nil
	})
	return out
}

func scanFile(path, branch string, hwmMs int64, best map[string]dedupEntry) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	line := 0
	for sc.Scan() {
		line++
		b := sc.Bytes()
		if !bytes.Contains(b, usageMarker) {
			continue
		}
		var r record
		if json.Unmarshal(b, &r) != nil || r.Message.Usage == nil {
			continue
		}
		if r.GitBranch != branch {
			continue
		}
		ts := tsToMs(r.Timestamp)
		// With a baseline, keep only records after it. With no baseline (hwm==0,
		// first commit on the branch), keep everything — including records whose
		// timestamp failed to parse (ts==0), which would otherwise be dropped.
		if hwmMs > 0 && ts <= hwmMs {
			continue
		}
		reqID := r.RequestID
		if reqID == "" {
			reqID = r.Message.ID
		}
		key := reqID
		if key == "" {
			key = fmt.Sprintf("%s#%d", path, line) // no id → treat as unique
		}
		out := r.Message.Usage.Output
		if prev, ok := best[key]; ok && prev.out >= out {
			continue
		}
		best[key] = dedupEntry{
			model: r.Message.Model,
			tsMs:  ts,
			reqID: reqID,
			u:     toUsage(r.Message.Usage),
			out:   out,
		}
	}
}

func toUsage(u *usage) pricing.Usage {
	pu := pricing.Usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead}
	if u.CacheTiers != nil && (u.CacheTiers.E5m+u.CacheTiers.E1h) > 0 {
		pu.CacheWrite5m = u.CacheTiers.E5m
		pu.CacheWrite1h = u.CacheTiers.E1h
	} else {
		// Older records carry only the total; attribute to 1h (observed Claude
		// Code default). Documented assumption — see
		// docs/plans/completed/2026-06-14-claude-budget.md.
		pu.CacheWrite1h = u.CacheCreate
	}
	return pu
}

func underRepo(cwd, root string) bool {
	if cwd == "" || root == "" {
		return false
	}
	return cwd == root || strings.HasPrefix(cwd, root+"/")
}

// firstCwd returns the first non-empty cwd found across the given files — the
// source of truth for repo membership (the encoded dir name is lossy).
func firstCwd(files []string) string {
	for _, fp := range files {
		f, err := os.Open(fp)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
		for sc.Scan() {
			var r struct {
				Cwd string `json:"cwd"`
			}
			if json.Unmarshal(sc.Bytes(), &r) == nil && r.Cwd != "" {
				f.Close()
				return r.Cwd
			}
		}
		f.Close()
	}
	return ""
}

func tsToMs(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}
