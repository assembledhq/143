package main

// Destructive-migration lint for the canary/stable release-channel split.
//
// The shared database is migrated only by the canary pipeline, so every
// migration must stay compatible with the currently promoted stable release
// — not merely the previous commit. Additive changes are always fine.
// Destructive DDL (dropping/renaming/retyping schema that pinned stable code
// may still read) must carry an explicit floor annotation:
//
//	-- lint:destructive-ok-after schema="000240" reason="stable >= 000240 no longer reads issues.legacy_state"
//
// The floor is enforced at deploy time by cmd/migrate (the canary deploy
// refuses to apply the migration until the stable release's own migration
// set reaches the floor) and recorded in schema_compat_floors so stable
// preflights can enforce it later. This lint only ensures the annotation
// exists. See docs/design/118-canary-stable-release-channels.md.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// destructiveLintStartVersion grandfathers everything that shipped before the
// release-channel split (000245 added the channel plumbing itself). Only
// migrations numbered above it are held to the annotation requirement.
const destructiveLintStartVersion = 245

var (
	migrationVersionRE      = regexp.MustCompile(`^(\d+)_.*\.up\.sql$`)
	destructiveAnnotationRE = regexp.MustCompile(`--[^\n]*lint:destructive-ok-after\s+schema="\d+"\s+reason="[^"]+"`)

	// Statements that remove or reshape schema old stable code may depend
	// on. Loosening changes (DROP CONSTRAINT, DROP INDEX, DROP DEFAULT) are
	// deliberately not listed — old code keeps working when they land.
	destructivePatterns = []struct {
		re    *regexp.Regexp
		label string
	}{
		{regexp.MustCompile(`(?i)\bDROP\s+TABLE\s+(IF\s+EXISTS\s+)?("?)(\w+)`), "DROP TABLE"},
		{regexp.MustCompile(`(?i)\bDROP\s+COLUMN\b`), "DROP COLUMN"},
		{regexp.MustCompile(`(?i)\bRENAME\s+(COLUMN\s+)?\w+.*\bTO\b`), "RENAME"},
		{regexp.MustCompile(`(?i)\bALTER\s+COLUMN\s+\S+\s+(SET\s+DATA\s+)?TYPE\b`), "ALTER COLUMN TYPE"},
		{regexp.MustCompile(`(?i)\bALTER\s+COLUMN\s+\S+\s+SET\s+NOT\s+NULL\b`), "SET NOT NULL"},
	}

	droppedTableNameRE = regexp.MustCompile(`(?i)\bDROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?"?(\w+)"?`)
)

// scanDestructive reports destructive statements in migrations newer than the
// grandfather cutoff that lack the destructive-ok-after annotation.
func scanDestructive(file, src string) []violation {
	version, ok := migrationVersion(file)
	if !ok || version <= destructiveLintStartVersion {
		return nil
	}
	if destructiveAnnotationRE.MatchString(src) {
		return nil
	}

	var out []violation
	for lineIdx, rawLine := range strings.Split(src, "\n") {
		line := stripSQLLineComment(rawLine)
		if strings.TrimSpace(line) == "" {
			continue
		}
		for _, pattern := range destructivePatterns {
			if !pattern.re.MatchString(line) {
				continue
			}
			// Same-migration scratch tables (underscore prefix, e.g. backup
			// tables created and dropped inside one migration) are not a
			// cross-release compatibility surface.
			if pattern.label == "DROP TABLE" {
				if m := droppedTableNameRE.FindStringSubmatch(line); m != nil && strings.HasPrefix(m[1], "_") {
					continue
				}
			}
			out = append(out, violation{
				file:   file,
				line:   lineIdx + 1,
				table:  pattern.label,
				detail: fmt.Sprintf("destructive statement (%s) without a lint:destructive-ok-after annotation", pattern.label),
			})
		}
	}
	return out
}

func migrationVersion(file string) (uint64, bool) {
	m := migrationVersionRE.FindStringSubmatch(filepath.Base(file))
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// stripSQLLineComment removes a trailing `-- ...` comment so commented-out
// DDL doesn't trip the lint. (Block comments are rare in this repo's
// migrations; line comments are the norm.)
func stripSQLLineComment(line string) string {
	if idx := strings.Index(line, "--"); idx >= 0 {
		return line[:idx]
	}
	return line
}
