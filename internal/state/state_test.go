package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAbsentReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Version != 1 {
		t.Errorf("version = %d, want 1", s.Version)
	}
	if s.Branches == nil {
		t.Error("Branches should be a non-nil map")
	}
	if got := s.HwmFor("anything"); got != 0 {
		t.Errorf("HwmFor unseen branch = %d, want 0", got)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, _ := Load(dir)
	s.SetBranch("main", Branch{HwmMs: 1234, LastRequestID: "req-a"})
	s.SetBranch("feature/x", Branch{HwmMs: 5678, LastRequestID: "req-b"})
	if err := s.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.HwmFor("main") != 1234 {
		t.Errorf("main hwm = %d, want 1234", got.HwmFor("main"))
	}
	if got.Branches["main"].LastRequestID != "req-a" {
		t.Errorf("main reqID = %q, want req-a", got.Branches["main"].LastRequestID)
	}
	if got.HwmFor("feature/x") != 5678 {
		t.Errorf("feature/x hwm = %d, want 5678", got.HwmFor("feature/x"))
	}
	if got.Branches["feature/x"].LastRequestID != "req-b" {
		t.Errorf("feature/x reqID = %q, want req-b", got.Branches["feature/x"].LastRequestID)
	}
}

func TestPerBranchIsolation(t *testing.T) {
	dir := t.TempDir()
	s, _ := Load(dir)
	s.SetBranch("main", Branch{HwmMs: 100})
	s.SetBranch("dev", Branch{HwmMs: 200})
	// Advancing one branch must not disturb the other.
	s.SetBranch("main", Branch{HwmMs: 999})
	if s.HwmFor("dev") != 200 {
		t.Errorf("dev hwm = %d, want 200 (unchanged)", s.HwmFor("dev"))
	}
	if s.HwmFor("main") != 999 {
		t.Errorf("main hwm = %d, want 999", s.HwmFor("main"))
	}
}

// SetBranch must work even on a state whose map was nilled out.
func TestSetBranchNilMap(t *testing.T) {
	s := &State{}
	s.SetBranch("main", Branch{HwmMs: 7})
	if s.HwmFor("main") != 7 {
		t.Fatalf("hwm = %d, want 7", s.HwmFor("main"))
	}
}

func TestSaveAssignsVersion(t *testing.T) {
	dir := t.TempDir()
	s := &State{Branches: map[string]Branch{}}
	if err := s.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _ := Load(dir)
	if got.Version != 1 {
		t.Errorf("version = %d, want 1", got.Version)
	}
}

func TestSaveAtomicLeavesNoTmp(t *testing.T) {
	dir := t.TempDir()
	s, _ := Load(dir)
	s.SetBranch("main", Branch{HwmMs: 1})
	if err := s.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "claude-budget.tmp")); !os.IsNotExist(err) {
		t.Errorf("temp file should not survive a Save, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "claude-budget")); err != nil {
		t.Errorf("state file missing after Save: %v", err)
	}
}

func TestLoadMalformedErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude-budget"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("expected an error loading malformed state")
	}
}

func TestPendingWriteReadClear(t *testing.T) {
	dir := t.TempDir()

	// Absent → ok=false, no error.
	if _, ok, err := ReadPending(dir); err != nil || ok {
		t.Fatalf("ReadPending absent: ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	p := Pending{Branch: "main", HwmMs: 42, LastRequestID: "req-z"}
	if err := WritePending(dir, p); err != nil {
		t.Fatalf("WritePending: %v", err)
	}
	got, ok, err := ReadPending(dir)
	if err != nil || !ok {
		t.Fatalf("ReadPending present: ok=%v err=%v", ok, err)
	}
	if got != p {
		t.Errorf("read = %+v, want %+v", got, p)
	}

	if err := ClearPending(dir); err != nil {
		t.Fatalf("ClearPending: %v", err)
	}
	if _, ok, _ := ReadPending(dir); ok {
		t.Error("pending should be gone after ClearPending")
	}
}

// ClearPending on a missing marker is a no-op, not an error.
func TestClearPendingAbsentIsNoError(t *testing.T) {
	if err := ClearPending(t.TempDir()); err != nil {
		t.Fatalf("ClearPending absent: %v", err)
	}
}

func TestWritePendingAtomicLeavesNoTmp(t *testing.T) {
	dir := t.TempDir()
	if err := WritePending(dir, Pending{Branch: "main", HwmMs: 1}); err != nil {
		t.Fatalf("WritePending: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "claude-budget.pending.tmp")); !os.IsNotExist(err) {
		t.Errorf("pending temp file should not survive, stat err = %v", err)
	}
}

func TestReadPendingMalformedErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude-budget.pending"), []byte("{nope"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := ReadPending(dir); err == nil {
		t.Error("expected an error reading malformed pending marker")
	}
}
