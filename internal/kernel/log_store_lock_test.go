package kernel

import (
	"path/filepath"
	"strings"
	"testing"

	"moos/kernel/internal/graph"
)

// TestLogStoreSingleWriter — moos-kernel#40: a second LogStore on the same
// path must fail fast while the first holds the exclusive lock, and succeed
// again once the first closes.
func TestLogStoreSingleWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moos.jsonl")

	s1, err := NewLogStore(path)
	if err != nil {
		t.Fatalf("first writer must acquire the lock: %v", err)
	}

	if _, err := NewLogStore(path); err == nil {
		t.Fatal("second writer on a locked log must fail fast")
	} else if !strings.Contains(err.Error(), "moos-kernel#40") {
		t.Fatalf("second-writer error should cite the single-writer doctrine, got: %v", err)
	}

	// The held handle still appends and reads back while locked.
	entry := graph.PersistedRewrite{
		Envelope: graph.Envelope{
			RewriteType: graph.ADD,
			Actor:       "urn:moos:agent:test",
			NodeURN:     "urn:moos:program:lock-test",
			TypeID:      "program",
		},
		LogSeq: 1,
	}
	if err := s1.Append([]graph.PersistedRewrite{entry}); err != nil {
		t.Fatalf("append through the locked handle: %v", err)
	}
	got, err := s1.ReadAll()
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if len(got) != 1 || got[0].LogSeq != 1 {
		t.Fatalf("readback mismatch: %+v", got)
	}

	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Lock released — a new writer acquires it and sees the persisted entry.
	s2, err := NewLogStore(path)
	if err != nil {
		t.Fatalf("writer after close must acquire the lock: %v", err)
	}
	defer s2.Close()
	got, err = s2.ReadAll()
	if err != nil {
		t.Fatalf("readback after reopen: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 persisted entry after reopen, got %d", len(got))
	}
}

// TestLogStoreAppendAfterClose — appends on a closed store error instead of
// silently writing through a dead handle.
func TestLogStoreAppendAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moos.jsonl")
	s, err := NewLogStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	err = s.Append([]graph.PersistedRewrite{{LogSeq: 1}})
	if err == nil {
		t.Fatal("append after Close must error")
	}
}
