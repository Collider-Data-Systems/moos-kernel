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

// acquireLogLock takes the single-writer lock for a JSONL log by opening a
// sidecar file (<path>.lock) with share mode 0: no other handle — from any
// process, including this one — can open it until this handle closes. Share
// mode 0 also denies DELETE access, so the sidecar cannot be unlinked out
// from under the lock (the disarm hazard the unix flock variant guards
// against by locking the jsonl itself). The OS releases the handle when the
// process exits, so a crash never leaves a stale lock behind. See
// moos-kernel#40 for the multi-writer failure this prevents.
func acquireLogLock(path string) (*os.File, error) {
	lockPath := path + ".lock"
	p, err := syscall.UTF16PtrFromString(lockPath)
	if err != nil {
		return nil, fmt.Errorf("log_store: lock path %q: %w", lockPath, err)
	}
	h, err := syscall.CreateFile(p,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0, // no sharing — the handle itself is the lock
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
		return nil, fmt.Errorf("log_store: open lock %q: %w", lockPath, err)
	}
	return os.NewFile(uintptr(h), lockPath), nil
}
