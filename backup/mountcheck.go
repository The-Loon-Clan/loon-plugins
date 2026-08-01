package backup

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Answering "will what I write here survive the container being recreated?"
//
// The docs on BackupDir and DBDumpDir have always said MUST be a bind mount,
// and nothing checked. Prod found out the expensive way: the database dump job
// wrote 24 GB, logged success, and the index — same container, same path —
// found the class empty, because the volume line existed in the repo's compose
// file and not on the box. Four dumps completed. None survived a deploy. The
// only complaint anywhere was a NOTE in a job log.
//
// The check deliberately is NOT "is this a mount point". A tmpfs is a mount
// and is ephemeral; a bind mount of a host directory is a mount and is not.
// What actually decides the question is the FILESYSTEM the path lands on, and
// a container's writable layer has a name: overlay. So ask the kernel which
// mount governs the path and what type it is, which answers the real question
// with no heuristics about containers at all.

// ephemeralFS names filesystems whose contents do not outlive the container.
//
// tmpfs and ramfs are memory; a reboot is enough to lose them. overlay/aufs
// are the container's writable layer, discarded on recreate — which is every
// deploy.
var ephemeralFS = map[string]bool{
	"overlay": true, "overlay2": true, "aufs": true,
	"tmpfs": true, "ramfs": true,
}

// fsTypeForPath returns the filesystem type of the mount governing path, given
// the contents of a mountinfo file.
//
// Split out from the file read so the parsing — the part with the field
// offsets and the escaping — is testable without a Linux kernel underneath.
//
// mountinfo's shape (proc(5)):
//
//	36 35 98:0 /src /app rw,noatime master:1 - ext3 /dev/root rw
//	                     ^mount point       ^sep ^fstype
//
// The optional fields before the separator are variable in number, which is
// why the line is split on " - " rather than indexed from the left throughout.
func fsTypeForPath(mountinfo io.Reader, path string) (string, bool) {
	best, bestLen := "", -1
	sc := bufio.NewScanner(mountinfo)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		left, right, ok := strings.Cut(sc.Text(), " - ")
		if !ok {
			continue
		}
		lf, rf := strings.Fields(left), strings.Fields(right)
		if len(lf) < 5 || len(rf) < 1 {
			continue
		}
		mp := unescapeMountField(lf[4])
		if !underMount(mp, path) || len(mp) <= bestLen {
			continue
		}
		// Longest matching mount point wins: /app/db-dumps must beat /app when
		// both are mounted, or a bind mount inside a container is reported as
		// the overlay it sits on — the exact false alarm this must not raise.
		best, bestLen = rf[0], len(mp)
	}
	if bestLen < 0 {
		return "", false
	}
	return best, true
}

// underMount reports whether path lies within the mount point mp. Compared by
// path COMPONENT, so /app does not claim /application.
func underMount(mp, path string) bool {
	if mp == "/" {
		return true
	}
	return path == mp || strings.HasPrefix(path, mp+"/")
}

// unescapeMountField decodes the octal escapes mountinfo uses for characters
// that would otherwise split a field.
func unescapeMountField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

// pathPersistence reports whether writes to path survive a container recreate.
//
// The second return is "we could tell": off Linux, or anywhere /proc is not
// mounted, there is no answer and the caller must not warn. A dev machine
// reporting "your backup directory is ephemeral" on every boot would train the
// operator to ignore the one message that matters.
func pathPersistence(path string) (persistent bool, known bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, false
	}
	// Resolve symlinks before matching: mountinfo records real paths, and a
	// symlinked directory would otherwise match the wrong mount. A path that
	// does not exist yet still has a resolvable parent, which is the mount we
	// care about.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	} else if resolved, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		abs = filepath.Join(resolved, filepath.Base(abs))
	}
	abs = filepath.ToSlash(abs)

	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false, false
	}
	defer f.Close()
	fs, ok := fsTypeForPath(f, abs)
	if !ok {
		return false, false
	}
	return !ephemeralFS[fs], true
}

// ephemeralWarning returns the operator-facing message for a directory that
// will not survive a deploy, or "" when the path is fine or unknowable.
//
// One message, phrased the same everywhere, because the reader is someone
// scanning a job log for the reason their restore has nothing to restore.
func ephemeralWarning(label, path string) string {
	persistent, known := pathPersistence(path)
	if !known || persistent {
		return ""
	}
	return label + " (" + path + ") is on the container's writable layer, not a bind mount. " +
		"Everything written there is DISCARDED on the next deploy. Add the volume to the " +
		"compose file on the host and recreate the container — until then this protects nothing."
}

// classBySlug finds a declared class by its slug. Returns false for a slug the
// host never declared, which the caller treats as "nothing to check" rather
// than substituting a guess at the directory.
func classBySlug(classes []AssetClass, slug string) (AssetClass, bool) {
	for _, c := range classes {
		if c.Slug == slug {
			return c, true
		}
	}
	return AssetClass{}, false
}
