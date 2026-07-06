//go:build windows

package kernel

import (
	"fmt"
	"os"
	"syscall"
)

// ERROR_SHARING_VIOLATION (winerror.h code 32) — not exported by the stdlib
// syscall package, and x/sys is off-limits (stdlib-only repo policy).
const errSharingViolation syscall.Errno = 32

// acquireLogLock takes the single-writer lock by opening the jsonl ITSELF
// with deny-write sharing: concurrent read handles stay allowed
// (FILE_SHARE_READ), but no other write handle — from any process, including
// lock-unaware OLD binaries — can open the file until this handle closes.
// Windows share modes are mandatory, so during a mixed-binary migration a
// new-binary kernel holding the log physically blocks pre-fix sidecars from
// appending — the exact moos-kernel#40 incident shape. The returned handle
// is both the lock and the store's write handle (Append writes through it;
// see LogStore.Append). The OS releases the handle when the process exits:
// crash-safe, no stale locks, no sidecar file to disarm.
func acquireLogLock(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("log_store: lock path %q: %w", path, err)
	}
	h, err := syscall.CreateFile(p,
		syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ, // no WRITE/DELETE sharing — the handle itself is the lock
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0)
	if err != nil {
		// Only a sharing violation means "another kernel owns this log".
		// Anything else (bad path, permissions) is a plain open failure —
		// do NOT steer the operator toward the unsafe bypass for those.
		if errno, ok := err.(syscall.Errno); ok && errno == errSharingViolation {
			return nil, fmt.Errorf("log_store: %q is already owned by another moos-kernel process (%v) — concurrent writers interleave duplicate log_seq values (moos-kernel#40); point this process at its own --log, or pass --allow-shared-log to bypass (unsafe)", path, err)
		}
		return nil, fmt.Errorf("log_store: open %q for lock: %w", path, err)
	}
	return os.NewFile(uintptr(h), path), nil
}
