//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package kernel

import "os"

// acquireLogLock — no OS-level single-writer lock on this platform (no
// share modes; no stdlib syscall.Flock — solaris/aix lack it too, hence the
// explicit GOOS list on the flock variant). The fleet runs Windows + the
// flock Unixes; everything else compiles but skips enforcement — a nil
// handle makes the store behave like NewSharedLogStore (per-Append opens),
// and the in-process mutex still serializes appends. See moos-kernel#40.
func acquireLogLock(path string) (*os.File, error) {
	return nil, nil
}
