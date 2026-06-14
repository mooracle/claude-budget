// Package state persists per-branch consumption baselines in <gitDir>/claude-budget
// and the prepare→post-commit handoff in <gitDir>/claude-budget.pending.
//
// The pending marker carries a payload (branch + watermark), unlike Copilot
// Budget's empty marker — with no daemon holding the snapshot in memory, the
// watermark must survive between the two hook invocations.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Branch is the high-water mark of consumed usage for one branch.
type Branch struct {
	HwmMs         int64  `json:"hwmMs"`
	LastRequestID string `json:"lastRequestId"`
}

// State is the parsed <gitDir>/claude-budget.
type State struct {
	Version  int               `json:"version"`
	Branches map[string]Branch `json:"branches"`
}

func statePath(gitDir string) string   { return filepath.Join(gitDir, "claude-budget") }
func pendingPath(gitDir string) string { return filepath.Join(gitDir, "claude-budget.pending") }

// Load reads the state file, returning an empty state if it doesn't exist.
func Load(gitDir string) (*State, error) {
	b, err := os.ReadFile(statePath(gitDir))
	if errors.Is(err, os.ErrNotExist) {
		return &State{Version: 1, Branches: map[string]Branch{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.Branches == nil {
		s.Branches = map[string]Branch{}
	}
	return &s, nil
}

// HwmFor returns the consumed high-water mark (ms) for a branch, 0 if unseen.
func (s *State) HwmFor(branch string) int64 { return s.Branches[branch].HwmMs }

// SetBranch records a branch's new watermark in memory (call Save to persist).
func (s *State) SetBranch(branch string, b Branch) {
	if s.Branches == nil {
		s.Branches = map[string]Branch{}
	}
	s.Branches[branch] = b
}

// Save writes the state file atomically (temp + rename).
func (s *State) Save(gitDir string) error {
	if s.Version == 0 {
		s.Version = 1
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(statePath(gitDir), b)
}

// Pending is the watermark staged by prepare-commit-msg for post-commit to promote.
type Pending struct {
	Branch        string `json:"branch"`
	HwmMs         int64  `json:"hwmMs"`
	LastRequestID string `json:"lastRequestId"`
}

// WritePending stages the handoff payload.
func WritePending(gitDir string, p Pending) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return writeAtomic(pendingPath(gitDir), b)
}

// ReadPending returns the staged payload; ok=false if no marker exists.
func ReadPending(gitDir string) (Pending, bool, error) {
	b, err := os.ReadFile(pendingPath(gitDir))
	if errors.Is(err, os.ErrNotExist) {
		return Pending{}, false, nil
	}
	if err != nil {
		return Pending{}, false, err
	}
	var p Pending
	if err := json.Unmarshal(b, &p); err != nil {
		return Pending{}, false, err
	}
	return p, true, nil
}

// ClearPending removes the marker (no error if already absent).
func ClearPending(gitDir string) error {
	err := os.Remove(pendingPath(gitDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
