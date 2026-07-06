//go:build unix

package kernel

import (
	"fmt"
	"os"
	"syscall"
)

// acquireLogLock takes the single-writer lock via a non-blocking exclusive
// flock on the log file ITSELF. flock binds to the open file description /
// inode, not the path — a sidecar lock file would silently disarm if unlinked
// (a second process O_CREATEs a fresh inode at the same path and flocks it
// clean), while the jsonl is never legitimately removed from under a live
// kernel. The returned handle is both the lock and the store's write handle
// (Append writes through it), and a second acquire — from any process,
// including this one — fails fast. The OS releases the lock when the owning
// process exits: crash-safe, no stale locks. Note the asymmetry with the
// Windows variant: flock is advisory, so a lock-unaware old binary can still
// append on unix; Windows deny-write sharing is mandatory and blocks it. See
// moos-kernel#40 for the multi-writer failure this prevents.
func acquireLogLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("log_store: open %q for lock: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		// Only EWOULDBLOCK/EAGAIN means "another kernel owns this log".
		// Anything else (unsupported filesystem, ENOLCK, ...) is a plain
		// lock failure — do NOT steer the operator toward the unsafe bypass.
		if errno, ok := err.(syscall.Errno); ok && (errno == syscall.EWOULDBLOCK || errno == syscall.EAGAIN) {
			return nil, fmt.Errorf("log_store: %q is already owned by another moos-kernel process (%v) — concurrent writers interleave duplicate log_seq values (moos-kernel#40); point this process at its own --log, or pass --allow-shared-log to bypass (unsafe)", path, err)
		}
		return nil, fmt.Errorf("log_store: flock %q: %w", path, err)
	}
	return f, nil
}
