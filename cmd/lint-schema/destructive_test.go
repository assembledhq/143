package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScanDestructive(t *testing.T) {
	t.Parallel()

	t.Run("flags unannotated destructive DDL in post-cutoff migrations", func(t *testing.T) {
		t.Parallel()
		src := "ALTER TABLE issues DROP COLUMN legacy_state;\n"
		violations := scanDestructive("migrations/000250_drop_legacy.up.sql", src)
		require.Len(t, violations, 1, "unannotated DROP COLUMN must be flagged")
		require.Contains(t, violations[0].detail, "DROP COLUMN")
	})

	t.Run("annotation satisfies the lint", func(t *testing.T) {
		t.Parallel()
		src := "-- lint:destructive-ok-after schema=\"000240\" reason=\"stable stopped reading legacy_state\"\n" +
			"ALTER TABLE issues DROP COLUMN legacy_state;\n"
		violations := scanDestructive("migrations/000250_drop_legacy.up.sql", src)
		require.Empty(t, violations, "an annotated destructive migration is allowed; the deploy gate enforces the floor")
	})

	t.Run("grandfathered migrations are exempt", func(t *testing.T) {
		t.Parallel()
		src := "DROP TABLE org_slugs;\n"
		violations := scanDestructive("migrations/000005_drop_org_slug.up.sql", src)
		require.Empty(t, violations, "pre-split migrations shipped under single-plane rules and must not be re-litigated")
	})

	t.Run("additive DDL passes", func(t *testing.T) {
		t.Parallel()
		src := "ALTER TABLE issues ADD COLUMN note text;\nCREATE INDEX idx_foo ON issues (note);\nALTER TABLE issues DROP CONSTRAINT issues_note_check;\n"
		violations := scanDestructive("migrations/000250_additive.up.sql", src)
		require.Empty(t, violations, "additive and loosening changes need no annotation")
	})

	t.Run("commented-out DDL does not trip the lint", func(t *testing.T) {
		t.Parallel()
		src := "-- DROP TABLE issues; (to be done after promotion)\nALTER TABLE issues ADD COLUMN note text;\n"
		violations := scanDestructive("migrations/000250_notes.up.sql", src)
		require.Empty(t, violations, "line comments must be stripped before pattern matching")
	})

	t.Run("same-migration scratch tables may be dropped", func(t *testing.T) {
		t.Parallel()
		src := "CREATE TABLE _dup_backup (id uuid); -- lint:no-org-id reason=\"scratch\"\nDROP TABLE _dup_backup;\n"
		violations := scanDestructive("migrations/000250_dedupe.up.sql", src)
		require.Empty(t, violations, "underscore-prefixed scratch tables are not a cross-release surface")
	})

	t.Run("rename and retype are destructive", func(t *testing.T) {
		t.Parallel()
		src := "ALTER TABLE issues RENAME COLUMN a TO b;\nALTER TABLE issues ALTER COLUMN c TYPE bigint;\nALTER TABLE issues ALTER COLUMN d SET NOT NULL;\n"
		violations := scanDestructive("migrations/000250_reshape.up.sql", src)
		require.Len(t, violations, 3, "rename, retype, and NOT NULL tightening all reshape schema stable code may read")
	})
}
