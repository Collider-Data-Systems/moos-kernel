//go:build !unix && !windows

package kernel

import "os"

// acquireLogLock — no OS-level single-writer lock on this platform (no
// flock, no share modes). The fleet runs Windows + Unix; exotic targets
// (js, plan9) compile but skip enforcement — a nil handle makes the store
// behave like NewSharedLogStore (per-Append opens), and the in-process
// mutex still serializes appends. See moos-kernel#40.
func acquireLogLock(path string) (*os.File, error) {
	return nil, nil
}
