//go:build windows

package kernel

import (
	"fmt"
	"os"
	"syscall"
)

// openLogExclusive opens (creating if absent) the log file with deny-write
// sharing: concurrent read handles stay allowed, but any second write-handle
// open — same or different process — fails with a sharing violation. This is
// the Windows equivalent of an exclusive flock for single-writer enforcement
// (moos-kernel#40).
func openLogExclusive(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p,
		syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ, // no FILE_SHARE_WRITE / DELETE: second writer fails fast
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0)
	if err != nil {
		return nil, fmt.Errorf("locked by another kernel process (single-writer enforced, moos-kernel#40): %w", err)
	}
	return os.NewFile(uintptr(h), path), nil
}
