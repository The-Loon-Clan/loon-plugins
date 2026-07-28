//go:build unix

package backup

import (
	"os"
	"syscall"
)

// statIdentity returns ctime and inode, the two fields that make the stat gate
// safe against a clock that moves backward and against a replace-by-rename that
// preserved size and mtime. Both are POSIX-only; see stat_other.go.
func statIdentity(info os.FileInfo) (ctimeNS, inode int64) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return st.Ctim.Nano(), int64(st.Ino)
}
