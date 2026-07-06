package kernel

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"moos/kernel/internal/graph"
)

const maxLogLineBytes = 10 * 1024 * 1024 // 10 MB max per log line

// LogStore is a JSONL (newline-delimited JSON) append-only Store.
// Each line is one PersistedRewrite. The file is opened in O_APPEND mode.
//
// Multi-process sharing is enforced against: NewLogStore takes an exclusive
// single-writer lock (a <path>.lock sidecar held for the store's lifetime),
// because two kernels replaying the same file hold independent logSeq
// counters and stale folds — they stamp duplicate log_seq values and pass
// gates against state that never saw the other's writes (moos-kernel#40).
type LogStore struct {
	mu   sync.Mutex
	path string
	lock *os.File // single-writer lock handle; nil when opened shared
}

// NewLogStore opens the JSONL store and takes the single-writer lock.
// A second store on the same path — in this process or any other — fails
// fast instead of interleaving. Release with Close.
func NewLogStore(path string) (*LogStore, error) {
	lock, err := acquireLogLock(path)
	if err != nil {
		return nil, err
	}
	ls, err := newLogStoreUnlocked(path)
	if err != nil {
		lock.Close()
		return nil, err
	}
	ls.lock = lock
	return ls, nil
}

// NewSharedLogStore opens the store WITHOUT the single-writer lock. Unsafe:
// concurrent writers interleave duplicate log_seq values and apply against
// stale folds (moos-kernel#40). Exists only as the --allow-shared-log
// emergency escape hatch.
func NewSharedLogStore(path string) (*LogStore, error) {
	return newLogStoreUnlocked(path)
}

func newLogStoreUnlocked(path string) (*LogStore, error) {
	// Create or verify the file is writable
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("log_store: open %q: %w", path, err)
	}
	f.Close()
	return &LogStore{path: path}, nil
}

// Close releases the single-writer lock. The store must not be used after.
// No-op for stores opened via NewSharedLogStore.
func (l *LogStore) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lock == nil {
		return nil
	}
	err := l.lock.Close()
	l.lock = nil
	return err
}

func (l *LogStore) Append(entries []graph.PersistedRewrite) error {
	if len(entries) == 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("log_store: open for append: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
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
