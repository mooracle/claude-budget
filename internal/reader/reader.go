// Package reader scans Claude Code session transcripts and aggregates the
// not-yet-consumed token usage for the current repo and branch.
//
// Pipeline: enumerate ~/.claude/projects/* → per-file mtime prune → repo
// membership via the cwd field → scan survivors → keep gitBranch==branch AND
// timestamp>hwm → dedup by requestId (streaming partials share an id) → sum the
// five token buckets per model and price.
package reader

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
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
		entries, _ := os.ReadDir(dirPath)
		var survivors []string
		for _, f := range entries {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			if hwmMs > 0 {
				if info, err := f.Info(); err == nil && info.ModTime().UnixMilli() <= hwmMs {
					continue // prune: every record in this file predates the baseline
				}
			}
			survivors = append(survivors, filepath.Join(dirPath, f.Name()))
		}
		if len(survivors) == 0 {
			continue
		}
		if !underRepo(firstCwd(survivors), repoRoot) {
			continue
		}
		for _, fp := range survivors {
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
		ms.Tokens = ms.Usage.Input + ms.Usage.Output + ms.Usage.CacheRead + ms.Usage.CacheWrite5m + ms.Usage.CacheWrite1h
		res.Models = append(res.Models, *ms)
		res.TotalCostUSD += ms.CostUSD
		res.TotalTokens += ms.Tokens
		res.Requests += ms.Requests
	}
	sort.Slice(res.Models, func(i, j int) bool { return res.Models[i].CostUSD > res.Models[j].CostUSD })
	return res, nil
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
