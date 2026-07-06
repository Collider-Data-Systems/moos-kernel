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
// Each line is one PersistedRewrite.
//
// Multi-process sharing is enforced against: NewLogStore takes an exclusive
// single-writer lock ON THE JSONL ITSELF, held for the store's lifetime
// (Windows: deny-write sharing — mandatory, blocks even lock-unaware old
// binaries; unix: flock — advisory, blocks every store that goes through
// NewLogStore; see the platform log_lock_*.go files). Two kernels replaying
// the same file hold independent logSeq counters and stale folds — they
// stamp duplicate log_seq values and pass gates against state that never
// saw the other's writes (moos-kernel#40). The locked handle doubles as the
// write handle: Append writes through it.
type LogStore struct {
	mu     sync.Mutex
	path   string
	w      *os.File // locked write handle; nil when shared or closed
	shared bool     // opened via NewSharedLogStore (or a no-lock platform)
	closed bool     // Close was called; the store must reject further writes
}

// NewLogStore opens the JSONL store and takes the single-writer lock.
// A second store on the same path — in this process or any other — fails
// fast instead of interleaving. Release with Close.
func NewLogStore(path string) (*LogStore, error) {
	w, err := acquireLogLock(path)
	if err != nil {
		return nil, err
	}
	if w == nil {
		// Platform without OS-level locking (log_lock_other.go): fall back
		// to shared behavior, still verifying writability.
		return NewSharedLogStore(path)
	}
	return &LogStore{path: path, w: w}, nil
}

// NewSharedLogStore opens the store WITHOUT the single-writer lock. Unsafe:
// concurrent writers interleave duplicate log_seq values and apply against
// stale folds (moos-kernel#40). Exists only as the --allow-shared-log
// emergency escape hatch.
func NewSharedLogStore(path string) (*LogStore, error) {
	// Create or verify the file is writable
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("log_store: open %q: %w", path, err)
	}
	f.Close()
	return &LogStore{path: path, shared: true}, nil
}

// Close releases the single-writer lock. The store must not be used after —
// Append returns an error rather than silently degrading to the unlocked
// path. Idempotent; also finalizes stores opened via NewSharedLogStore.
func (l *LogStore) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.w == nil {
		return nil
	}
	err := l.w.Close()
	l.w = nil
	return err
}

func (l *LogStore) Append(entries []graph.PersistedRewrite) error {
	if len(entries) == 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return fmt.Errorf("log_store: store is closed")
	}

	var f *os.File
	if l.w != nil {
		// Locked store: the held handle is the only writer (exclusive lock),
		// so seek-to-end + write is equivalent to O_APPEND without ever
		// releasing the lock.
		if _, err := l.w.Seek(0, io.SeekEnd); err != nil {
			return fmt.Errorf("log_store: seek: %w", err)
		}
		f = l.w
	} else {
		// Shared (escape-hatch) store: per-Append O_APPEND open. On Windows
		// this fails while any locked kernel holds the file — correct: the
		// hatch is for recovery when no locked kernel is running.
		shared, err := os.OpenFile(l.path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("log_store: open for append: %w", err)
		}
		defer shared.Close()
		f = shared
	}

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
