//go:build !windows

package kernel

import (
	"fmt"
	"os"
	"syscall"
)

// openLogExclusive opens (creating if absent) the log file for writing and
// takes a non-blocking exclusive flock. A second writer — same or different
// process — gets an immediate error instead of sharing the log
// (moos-kernel#40). flock is advisory but sufficient: every log writer goes
// through NewLogStore.
func openLogExclusive(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("locked by another kernel process (single-writer enforced, moos-kernel#40): %w", err)
	}
	return f, nil
}
