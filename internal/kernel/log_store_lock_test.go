package kernel

import (
	"path/filepath"
	"testing"
	"time"

	"moos/kernel/internal/graph"
)

// TestLogStoreSingleWriterLock verifies the moos-kernel#40 guard: a second
// LogStore on the same path fails fast while the first holds the lock, the
// shared (escape-hatch) open bypasses the lock, and Close releases it for
// re-acquisition.
func TestLogStoreSingleWriterLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moos.jsonl")

	ls1, err := NewLogStore(path)
	if err != nil {
		t.Fatalf("first NewLogStore: %v", err)
	}

	if _, err := NewLogStore(path); err == nil {
		t.Fatalf("second NewLogStore on %q succeeded; want single-writer lock error", path)
	}

	// The escape hatch opens without contending for the lock.
	if _, err := NewSharedLogStore(path); err != nil {
		t.Fatalf("NewSharedLogStore while lock held: %v", err)
	}

	if err := ls1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
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
