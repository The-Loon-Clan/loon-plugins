package usenet

import (
	"bufio"
	"embed"
	"fmt"
	"strings"
)

// Shipped reference data lives in seed/*.tsv: embedded in the binary, seeded
// into the database on boot, and diffable one record per line so a contributed
// addition reads as one line in review.
//
// Deliberately NOT migrations: migrations here are append-only, so a shipped
// data migration could never be corrected — every fix would need another
// migration, and reviewers would see one opaque blob of INSERTs.
//
// Seeding never overwrites operator intent. Each dataset states its own rule:
// junk rules update only source='seed' rows and never touch `enabled`;
// newsgroups insert-only, so a group you enabled or deleted stays that way.

//go:embed seed/*.tsv
var seedData embed.FS

// seedRecords reads a tab-separated seed file, skipping blank lines and #
// comments, and verifies each record has at least minCols columns. Returned
// records keep their raw column values (no trimming beyond the line ending), so
// callers decide what whitespace means for their fields.
func seedRecords(fsys embed.FS, path string, minCols int) ([][]string, error) {
	f, err := fsys.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out [][]string
	sc := bufio.NewScanner(f)
	// Seed lines can be long (regexes + notes); the default 64 KiB token limit is
	// plenty, but be explicit rather than silently truncating a future entry.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(text) == "" || strings.HasPrefix(text, "#") {
			continue
		}
		cols := strings.Split(text, "\t")
		if len(cols) < minCols {
			return nil, fmt.Errorf("%s:%d: want at least %d tab-separated columns, got %d",
				path, line, minCols, len(cols))
		}
		out = append(out, cols)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// col returns column i trimmed, or "" when the record is shorter.
func col(rec []string, i int) string {
	if i >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[i])
}
