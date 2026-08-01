package backup

import (
	"strings"
	"testing"
)

// A real worker container's mountinfo, trimmed: /app on the overlay, the
// asset directories bind-mounted from the host, and NO db-dumps entry — which
// is the prod configuration that lost four database dumps.
const prodLikeMountinfo = `25 30 0:23 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw
30 22 0:47 / / rw,relatime - overlay overlay rw,lowerdir=/var/lib/docker/overlay2/l/ABC
36 30 0:52 / /app rw,relatime - overlay overlay rw,upperdir=/var/lib/docker/overlay2/x/diff
41 36 259:1 /srv/indexer/web/static/covers /app/web/static/covers rw,relatime - ext4 /dev/nvme0n1p1 rw
42 36 259:1 /srv/indexer/backups /app/backups rw,relatime - ext4 /dev/nvme0n1p1 rw
43 30 0:60 / /dev/shm rw,nosuid,nodev - tmpfs shm rw,size=65536k
`

func TestOverlayPathIsReportedEphemeral(t *testing.T) {
	// The failure this exists to catch: a path with no volume line, so it
	// falls through to /app's overlay.
	fs, ok := fsTypeForPath(strings.NewReader(prodLikeMountinfo), "/app/db-dumps")
	if !ok {
		t.Fatal("no mount found for /app/db-dumps")
	}
	if fs != "overlay" {
		t.Errorf("fs = %q, want overlay — an unmounted dir inherits /app", fs)
	}
	if !ephemeralFS[fs] {
		t.Error("overlay must be classed ephemeral")
	}
}

// The longest matching mount point has to win. Prefix-matching on /app alone
// would report every bind mount inside the container as overlay — a false
// alarm on a correctly configured box, which is worse than silence because it
// teaches the operator to ignore the message.
func TestBindMountInsideTheOverlayIsPersistent(t *testing.T) {
	for _, path := range []string{"/app/backups", "/app/backups/2026", "/app/web/static/covers/a.jpg"} {
		fs, ok := fsTypeForPath(strings.NewReader(prodLikeMountinfo), path)
		if !ok {
			t.Fatalf("no mount found for %s", path)
		}
		if fs != "ext4" {
			t.Errorf("%s: fs = %q, want ext4 (the bind mount, not the overlay under it)", path, fs)
		}
	}
}

// tmpfs is a mount point and is still ephemeral, which is why the check asks
// for the filesystem type rather than "is this a mount".
func TestTmpfsIsEphemeralDespiteBeingAMount(t *testing.T) {
	fs, _ := fsTypeForPath(strings.NewReader(prodLikeMountinfo), "/dev/shm/scratch")
	if fs != "tmpfs" || !ephemeralFS[fs] {
		t.Errorf("fs = %q, ephemeral = %v; a mount point is not proof of persistence", fs, ephemeralFS[fs])
	}
}

// Component-wise matching: /app must not claim /application.
func TestMountPointsMatchOnComponentBoundaries(t *testing.T) {
	mi := "36 30 0:52 / /app rw - overlay overlay rw\n" +
		"37 30 259:1 /srv /application rw - ext4 /dev/sda1 rw\n"
	if fs, _ := fsTypeForPath(strings.NewReader(mi), "/application/data"); fs != "ext4" {
		t.Errorf("/application/data resolved to %q — /app matched a longer name", fs)
	}
	if !underMount("/", "/anything") {
		t.Error("root governs everything")
	}
	if underMount("/app", "/application") {
		t.Error("/app must not claim /application")
	}
}

// Optional fields before the separator are variable in number, so the fstype
// cannot be read at a fixed offset from the left.
func TestVariableOptionalFieldsDoNotShiftTheFsType(t *testing.T) {
	mi := "36 30 0:52 / /app rw,relatime shared:1 master:2 propagate_from:3 - xfs /dev/sdb rw\n"
	if fs, ok := fsTypeForPath(strings.NewReader(mi), "/app/x"); !ok || fs != "xfs" {
		t.Errorf("fs = %q ok=%v, want xfs — optional fields shifted the parse", fs, ok)
	}
}

func TestEscapedMountPointsDecode(t *testing.T) {
	mi := `36 30 259:1 / /app/db\040dumps rw - ext4 /dev/sda1 rw` + "\n"
	if fs, ok := fsTypeForPath(strings.NewReader(mi), "/app/db dumps/x"); !ok || fs != "ext4" {
		t.Errorf("fs = %q ok=%v — the \\040 space escape was not decoded", fs, ok)
	}
}

// Unparseable or absent input must yield "unknown", never a false verdict:
// the caller warns only on a definite answer.
func TestUnknownRatherThanGuessing(t *testing.T) {
	for _, in := range []string{"", "garbage\n", "36 30 0:52 / /other rw - ext4 /dev/sda1 rw\n"} {
		if _, ok := fsTypeForPath(strings.NewReader(in), "/app/db-dumps"); ok {
			t.Errorf("input %q produced a verdict for an unmatched path", in)
		}
	}
	// A line missing the separator entirely is skipped, not fatal.
	if _, ok := fsTypeForPath(strings.NewReader("36 30 0:52 / /app rw overlay\n"), "/app/x"); ok {
		t.Error("a malformed line was treated as a verdict")
	}
}

// The warning has to name the path and say what happens, because its reader is
// someone working out why a restore has nothing to restore.
func TestWarningIsSilentWhenUnknowable(t *testing.T) {
	// On a dev machine /proc/self/mountinfo is absent, so nothing is claimed.
	// (On Linux CI this path IS resolvable and the repo is on a real disk, so
	// either way the correct answer here is an empty warning.)
	if w := ephemeralWarning("BackupDir", "."); w != "" {
		t.Errorf("warned about a persistent or unknowable path: %s", w)
	}
}
