//go:build !unix

package backup

import "os"

// statIdentity has no portable answer off POSIX: Windows exposes a creation
// time rather than an inode-change time, and no stable inode through
// os.FileInfo. Returning zeros degrades the gate to (size, mtime) — correct,
// just weaker — which is right for a dev machine and irrelevant in production,
// where this runs in a Linux container.
func statIdentity(os.FileInfo) (ctimeNS, inode int64) { return 0, 0 }
