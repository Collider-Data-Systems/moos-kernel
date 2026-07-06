//go:build windows

package kernel

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLockBlocksLockUnawareWriter — the decisive moos-kernel#40 property on
// Windows: while a locked store holds the jsonl, a plain O_APPEND open (what
// a pre-fix OLD binary's Append does — no lock protocol at all) must fail
// with a sharing violation. This is what protects the log during a
// mixed-binary migration window, where sidecars are still running old code.
func TestLockBlocksLockUnawareWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moos.jsonl")

	s, err := NewLogStore(path)
	if err != nil {
		t.Fatalf("NewLogStore: %v", err)
	}
	defer s.Close()

	// The old binary's exact open (see pre-fix LogStore.Append).
	if f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		f.Close()
		t.Fatal("lock-unaware O_APPEND open succeeded while the lock is held; old binaries could still interleave")
	}

	// Readers stay allowed (FILE_SHARE_READ).
	if f, err := os.Open(path); err != nil {
		t.Fatalf("concurrent reader must stay allowed: %v", err)
	} else {
		f.Close()
	}

	// After Close, the old-binary open works again.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("append open after Close: %v", err)
	}
	f.Close()
}
