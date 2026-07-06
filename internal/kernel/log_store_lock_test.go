package kernel

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"moos/kernel/internal/graph"
)

// TestLogStoreLockErrorDiscrimination — only true contention gets the
// "owned by another moos-kernel process / --allow-shared-log" guidance; a
// bad path must read as a plain open failure so operators are never steered
// toward the unsafe bypass for a typo (fold item 2 from #41).
func TestLogStoreLockErrorDiscrimination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moos.jsonl")

	s, err := NewLogStore(path)
	if err != nil {
		t.Fatalf("first NewLogStore: %v", err)
	}
	defer s.Close()

	_, err = NewLogStore(path)
	if err == nil || !strings.Contains(err.Error(), "already owned by another moos-kernel process") {
		t.Fatalf("contention must cite another kernel process, got: %v", err)
	}

	_, err = NewLogStore(filepath.Join(t.TempDir(), "no-such-dir", "moos.jsonl"))
	if err == nil {
		t.Fatal("bad path must fail")
	}
	if strings.Contains(err.Error(), "already owned") || strings.Contains(err.Error(), "allow-shared-log") {
		t.Fatalf("bad path must NOT read as contention or suggest the bypass, got: %v", err)
	}
}

// TestLogStoreAppendAfterClose — appends on a closed store must error, not
// silently fall back to the unlocked per-Append path (Copilot re-review
// finding on #42).
func TestLogStoreAppendAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moos.jsonl")
	s, err := NewLogStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := s.Append([]graph.PersistedRewrite{{LogSeq: 1}}); err == nil {
		t.Fatal("Append after Close must error, not degrade to the unlocked path")
	}
	// And the lock is released for the next writer.
	s2, err := NewLogStore(path)
	if err != nil {
		t.Fatalf("re-acquire after Close must succeed: %v", err)
	}
	s2.Close()
}

// TestLogStoreSingleWriterLock verifies the moos-kernel#40 guard: a second
// LogStore on the same path fails fast while the first holds the lock, the
// shared (escape-hatch) open follows the platform lock semantics (advisory
// on unix, mandatory on Windows), and Close releases the lock for
// re-acquisition.
func TestLogStoreSingleWriterLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moos.jsonl")

	ls1, err := NewLogStore(path)
	if err != nil {
		t.Fatalf("first NewLogStore: %v", err)
	}
	defer ls1.Close() // idempotent; keeps TempDir cleanup working on early Fatal

	if _, err := NewLogStore(path); err == nil {
		t.Fatalf("second NewLogStore on %q succeeded; want single-writer lock error", path)
	}

	// Escape-hatch semantics while the lock is held are platform-asymmetric
	// under the deny-write-on-jsonl design: unix flock is advisory, so the
	// shared open succeeds; Windows sharing is mandatory, so even the hatch
	// is blocked while a locked kernel is live — it exists for recovery when
	// no locked kernel is running.
	_, sharedErr := NewSharedLogStore(path)
	if runtime.GOOS == "windows" {
		if sharedErr == nil {
			t.Fatal("NewSharedLogStore while lock held must fail on windows (mandatory deny-write)")
		}
	} else if sharedErr != nil {
		t.Fatalf("NewSharedLogStore while lock held (advisory flock): %v", sharedErr)
	}

	if err := ls1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// With no locked kernel running, the escape hatch opens everywhere.
	if _, err := NewSharedLogStore(path); err != nil {
		t.Fatalf("NewSharedLogStore after Close: %v", err)
	}

	ls2, err := NewLogStore(path)
	if err != nil {
		t.Fatalf("re-acquire after Close: %v", err)
	}
	defer ls2.Close()

	// The locked store still round-trips entries.
	now := time.Now().UTC()
	in := []graph.PersistedRewrite{{
		Envelope: graph.Envelope{
			RewriteType: graph.ADD,
			Actor:       "urn:moos:kernel",
			NodeURN:     "urn:moos:ki:lock-roundtrip",
			TypeID:      "knowledge_item",
		},
		AppliedAt: now,
		Timestamp: now,
		LogSeq:    1,
	}}
	if err := ls2.Append(in); err != nil {
		t.Fatalf("Append: %v", err)
	}
	out, err := ls2.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(out) != 1 || out[0].LogSeq != 1 || out[0].Envelope.NodeURN != "urn:moos:ki:lock-roundtrip" {
		t.Fatalf("round-trip mismatch: got %+v", out)
	}
}
