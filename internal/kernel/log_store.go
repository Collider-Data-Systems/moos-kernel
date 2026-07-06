package kernel

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"moos/kernel/internal/graph"
)

const maxLogLineBytes = 10 * 1024 * 1024 // 10 MB max per log line

// LogStore is a JSONL (newline-delimited JSON) append-only Store.
// Single-writer enforced (moos-kernel#40): NewLogStore takes an exclusive
// OS-level lock on the file (Windows: deny-write sharing; Unix: flock) and
// holds the write handle for the store's lifetime. A second process pointed
// at the same log — e.g. a stdio MCP sidecar sharing the live kernel's file —
// fails fast at open instead of replaying stale state and re-stamping
// duplicate log_seq values. Concurrent readers stay allowed.
type LogStore struct {
	mu   sync.Mutex
	path string
	f    *os.File // exclusively locked write handle, held for the store's lifetime
}

func NewLogStore(path string) (*LogStore, error) {
	f, err := openLogExclusive(path)
	if err != nil {
		return nil, fmt.Errorf("log_store: open %q: %w", path, err)
	}
	return &LogStore{path: path, f: f}, nil
}

// Close releases the exclusive lock. The store is unusable afterwards.
func (l *LogStore) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

func (l *LogStore) Append(entries []graph.PersistedRewrite) error {
	if len(entries) == 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.f == nil {
		return fmt.Errorf("log_store: store is closed")
	}
	// The held handle is the only writer (exclusive lock), so seek-to-end +
	// write is equivalent to O_APPEND without releasing the lock.
	if _, err := l.f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("log_store: seek: %w", err)
	}

	w := bufio.NewWriter(l.f)
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("log_store: marshal entry seq %d: %w", entry.LogSeq, err)
		}
		if _, err := w.Write(data); err != nil {
			return fmt.Errorf("log_store: write: %w", err)
		}
		if err := w.WriteByte('\n'); err != nil {
			return fmt.Errorf("log_store: write newline: %w", err)
		}
	}
	return w.Flush()
}

func (l *LogStore) ReadAll() ([]graph.PersistedRewrite, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	f, err := os.Open(l.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("log_store: open for read: %w", err)
	}
	defer f.Close()

	var entries []graph.PersistedRewrite
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, maxLogLineBytes), maxLogLineBytes)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry graph.PersistedRewrite
		if err := json.Unmarshal(line, &entry); err != nil {
			return entries, fmt.Errorf("log_store: unmarshal line: %w", err)
		}
		if entry.Timestamp.IsZero() {
			entry.Timestamp = entry.AppliedAt
		}
		if entry.AppliedAt.IsZero() {
			entry.AppliedAt = entry.Timestamp
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return entries, fmt.Errorf("log_store: scan: %w", err)
	}
	return entries, nil
}
