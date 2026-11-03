package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// These two direct-call schedules prove only the observation-to-insertion
// freshness fence. They do not establish a global confirmation/claim commit
// exclusion, Worker.poll, provider delivery, recovery or activation. Each leaf
// owns a full-schema DB and exactly two pool slots: the real supplied Run and a
// separately pre-acquired fixture writer/observer. Every application row,
// Scan, writer commit, later insertion result and outer settlement is real.
type confirmedPrepareObservationProjection struct {
	id         *uuid.UUID
	version    string
	structural bool
	fits       bool
	receipt    []byte
	digest     string
	head       string
	reviewID   *int64
}

type confirmedPrepareObservationRun struct {
	actual           *fullAssessmentDispatchRunObserver
	late             func(context.Context) error
	applicationPID   uint32
	prelocks         int
	observations     int
	scanErr          error
	writerErr        error
	read             confirmedPrepareObservationProjection
	writerGraph      json.RawMessage
	writerForeign    json.RawMessage
	writerVersion    string
	writerAssessment models.CodeReviewAssessment
	writerMetadata   models.CodeReviewSessionMetadata
}

func (r *confirmedPrepareObservationRun) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	return r.actual.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		r.applicationPID = tx.Conn().PgConn().PID()
		return work(unitCtx, &confirmedPrepareObservationTx{Tx: tx, owner: r})
	})
}

type confirmedPrepareObservationTx struct {
	pgx.Tx
	owner *confirmedPrepareObservationRun
}

func (tx *confirmedPrepareObservationTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	rows, err := tx.Tx.Query(ctx, query, args...)
	normalized := strings.ToLower(strings.Join(strings.Fields(query), " "))
	if strings.HasPrefix(normalized, "select m.id from code_review_session_metadata m") && strings.Contains(normalized, "for share of m") {
		tx.owner.prelocks++
	}
	return rows, err
}

func (tx *confirmedPrepareObservationTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	row := tx.Tx.QueryRow(ctx, query, args...)
	normalized := strings.ToLower(strings.Join(strings.Fields(query), " "))
	// Recognize only the real bounded observation. In particular this is NOT
	// a prelock hook or a hook on the later INSERT/freshness SQL. Removing
	// only that later fence leaves the same writer choreography intact.
	if strings.HasPrefix(normalized, "select a.id,coalesce(a.xmin::text,'')") &&
		strings.Contains(normalized, "from code_review_session_metadata m left join code_review_revision_assessments a") {
		return &confirmedPrepareObservationRow{Row: row, owner: tx.owner, ctx: ctx}
	}
	return row
}

type confirmedPrepareObservationRow struct {
	pgx.Row
	owner *confirmedPrepareObservationRun
	ctx   context.Context
}

func (row *confirmedPrepareObservationRow) Scan(dest ...any) error {
	err := row.Row.Scan(dest...)
	row.owner.scanErr = err
	if err != nil {
		return err
	}
	// These are the actual private helper's eight Scan destinations, copied
	// only AFTER PostgreSQL has populated them. Nothing answers or rewrites
	// application SQL, candidate presence, version, receipt or error results.
	read := confirmedPrepareObservationProjection{version: *dest[1].(*string), structural: *dest[2].(*bool), fits: *dest[3].(*bool),
		receipt: bytes.Clone(*dest[4].(*[]byte)), digest: *dest[5].(*string), head: *dest[6].(*string)}
	if value := *dest[0].(**uuid.UUID); value != nil {
		id := *value
		read.id = &id
	}
	if value := *dest[7].(**int64); value != nil {
		id := *value
		read.reviewID = &id
	}
	row.owner.observations++
	row.owner.read = read
	row.owner.writerErr = row.owner.late(row.ctx)
	if row.owner.writerErr != nil {
		return row.owner.writerErr
	}
	return nil
}

func confirmedPrepareObservationAssertAppearanceOnly(t *testing.T, before, after, original json.RawMessage, f strandedReviewFixture) {
	t.Helper()
	expected, actual := terminalOwnerPointerPositiveDecode(t, before), terminalOwnerPointerPositiveDecode(t, after)
	for _, row := range expected.rows["assessments"] {
		require.NotEqual(t, fullAssessmentDispatchJSON(t, f.assessment), row["id"], "the prior absence must be genuine in the complete graph, not a hidden candidate projected as NULL")
	}
	restored := fullAssessmentDispatchObject(t, original)
	// Both readbacks are JSONB-derived, but this separately saved whole row
	// retains whitespace inside nested RawMessages. Compare every complete
	// field in the same compact representation as the graph decoder; do not
	// drop or reconstruct any nested receipt, manifest or outcome value.
	for key, value := range restored {
		var compact bytes.Buffer
		require.NoError(t, json.Compact(&compact, value), "normalize only JSON presentation of the complete saved assessment field %s", key)
		restored[key] = json.RawMessage(compact.Bytes())
	}
	require.Equal(t, fullAssessmentDispatchJSON(t, f.assessment), restored["id"], "the separate writer must restore precisely the saved original candidate identity")
	require.Equal(t, fullAssessmentDispatchJSON(t, f.job.OutputKey), restored["publication_key"], "the appeared candidate must use the exact original publication key, not a sibling/latest key")
	expected.rows["assessments"] = append(expected.rows["assessments"], restored)
	sort.Slice(expected.rows["assessments"], func(i, j int) bool {
		return string(expected.rows["assessments"][i]["id"]) < string(expected.rows["assessments"][j]["id"])
	})
	require.Equal(t, expected, actual, "the independent legal appearance may add only the entire saved exact original A; metadata, cleared preexisting references, siblings, children, wakes, evidence and control must remain exact")
}

func TestCodeReviewConfirmedPrepareObservationFreshnessPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		absent bool
	}{
		{name: "real uncertain candidate confirms after bounded observation before INSERT"},
		{name: "genuinely absent original-key candidate appears after bounded observation before INSERT", absent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Separate full-schema DB per leaf; root may use -parallel=1 to bound aggregate capacity.
			ctx := context.Background()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			require.Equal(t, int32(2), pool.Config().MaxConns, "the actual Run and independent pre-acquired writer/observer must use literally two slots without hidden headroom")
			seed := confirmedInstallerSeed(t, ctx, pool, !tt.absent)
			f, foreign, in := seed.original, seed.foreign, seed.input
			reserved, err := pool.Acquire(ctx)
			require.NoError(t, err, "pre-acquire the only independent real writer connection BEFORE the original bounded Prepare Run begins")
			defer reserved.Release()
			writerPID := reserved.Conn().PgConn().PID()
			assessment, err := db.NewCodeReviewAssessmentStore(reserved).GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "retain the actual original full candidate's complete immutable identity before observing or legally removing it")
			var original json.RawMessage
			require.NoError(t, reserved.QueryRow(ctx, `SELECT to_jsonb(a) FROM code_review_revision_assessments a
				WHERE a.org_id=$1 AND a.id=$2 AND a.publication_key=$3`, f.job.OrgID, f.assessment, f.job.OutputKey).Scan(&original), "freeze the entire real FK-valid original A row for the actual absence/appearance schedule")
			receipt := confirmedRetirementProofReceipt(t, f, foreign, "")
			if tt.absent {
				require.Equal(t, models.CodeReviewPublicationConfirmed, assessment.PublicationState, "the appeared A must restore actual confirmed publication, not create another negative candidate")
				require.NoError(t, terminalLateScopeWrite(ctx, reserved, func(writerCtx context.Context, tx pgx.Tx) error {
					return terminalLateScopeRemoveAssessment(writerCtx, tx, f)
				}), "create real original-key candidate absence by clearing only its actual references before DELETE, with all full-schema FKs and triggers enabled")
			} else {
				require.Equal(t, models.CodeReviewPublicationUncertain, assessment.PublicationState, "the bounded observation must genuinely precede actual A-only confirmation")
			}
			before, err := terminalContentionSnapshot(ctx, reserved, f)
			require.NoError(t, err, "retain the complete original/sibling graph after legal setup and before the observation-to-insertion race")
			foreignBefore, err := terminalContentionSnapshot(ctx, reserved, foreign)
			require.NoError(t, err, "retain every foreign row, including its nonempty original pending intent and wake")
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, before), seed.children)
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, foreignBefore), seed.foreignChildren)
			terminalInstallerOrderAbsent(t, ctx, reserved, f, in)
			observed := &confirmedPrepareObservationRun{actual: &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}}
			observed.late = func(unitCtx context.Context) error {
				if err := terminalLateScopeWrite(unitCtx, reserved, func(writerCtx context.Context, tx pgx.Tx) error {
					if tt.absent {
						// This is explicitly a legal direct fixture insertion of
						// the previously accepted complete row, not production
						// admission or fabricated observation data. Metadata's
						// SHARE lock remains held by the actual Prepare unit.
						tag, err := tx.Exec(writerCtx, `INSERT INTO code_review_revision_assessments
							SELECT a.* FROM jsonb_populate_record(NULL::code_review_revision_assessments,$3::jsonb) a
							WHERE a.org_id=$1 AND a.id=$2 AND a.publication_key=$4`, f.job.OrgID, f.assessment, original, f.job.OutputKey)
						if err != nil {
							return err
						}
						if tag.RowsAffected() != 1 {
							return errors.New("synthetic appeared-candidate writer did not insert its one exact original A")
						}
						return nil
					}
					id, url := int64(77), "https://example.invalid/review/77"
					return db.NewCodeReviewAssessmentStore(tx).RecordPublication(writerCtx, f.job.OrgID, f.assessment, assessment.Generation, assessment.InputDigest,
						models.CodeReviewPublicationConfirmed, receipt, &id, &url)
				}); err != nil {
					return err
				}
				// The separate real transaction has COMMITTED before any of
				// these reads and before the application Scan returns, so
				// the later actual atomic INSERT must take a fresh snapshot.
				var err error
				observed.writerGraph, err = terminalContentionSnapshot(unitCtx, reserved, f)
				if err != nil {
					return err
				}
				observed.writerForeign, err = terminalContentionSnapshot(unitCtx, reserved, foreign)
				if err != nil {
					return err
				}
				observed.writerAssessment, err = db.NewCodeReviewAssessmentStore(reserved).GetByID(unitCtx, f.job.OrgID, f.assessment)
				if err != nil {
					return err
				}
				observed.writerMetadata, err = db.NewCodeReviewStore(reserved).GetByOutputKey(unitCtx, f.job.OrgID, f.job.OutputKey)
				if err != nil {
					return err
				}
				return reserved.QueryRow(unitCtx, `SELECT xmin::text FROM code_review_revision_assessments
					WHERE org_id=$1 AND id=$2 AND publication_key=$3`, f.job.OrgID, f.assessment, f.job.OutputKey).Scan(&observed.writerVersion)
			}
			var prepared db.PreparedCodeReviewDispatch
			var prepareErr error
			runErr := observed.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
				prepared = db.PreparedCodeReviewDispatch{}
				prepared, prepareErr = db.PrepareCodeReviewDispatchInTx(unitCtx, f.job.OrgID, tx, in)
				return prepareErr
			})
			after, err := terminalContentionSnapshot(ctx, reserved, f)
			require.NoError(t, err, "independently read every actual original/sibling row after Prepare and its outer Run settle")
			foreignAfter, err := terminalContentionSnapshot(ctx, reserved, foreign)
			require.NoError(t, err, "independently read every foreign row after the actual preparation result")
			// Assert the real behavioral refusal BEFORE instrument shape or
			// SQL-label checks: a bind-preserving fence-only ablation must
			// fail for its forbidden NEW child, not an earlier hook-name check.
			require.Equal(t, db.ErrCodeReviewDispatchPreparationIneligible, prepareErr, "the real later atomic statement must reject a changed candidate version or invalidated true absence, not grant stale observed eligibility")
			require.Equal(t, db.ErrCodeReviewDispatchPreparationIneligible, runErr, "the actual supplied Run must return the known nonretryable ineligibility result after rollback")
			require.Equal(t, db.PreparedCodeReviewDispatch{}, prepared, "freshness refusal must expose no provisional NEW result or wake identity")
			fullAssessmentDispatchAssertRun(t, observed.actual)
			require.NotZero(t, observed.applicationPID, "the actual supplied transaction must use a real connected application backend")
			require.NotZero(t, writerPID, "the pre-acquired independent writer must use a real connected backend")
			require.NotEqual(t, writerPID, observed.applicationPID, "the committed late writer must use the other actual pool connection, never the application's own transaction")
			require.Equal(t, 1, observed.observations, "the separate writer must run once only after one actual bounded observation Scan, never at the metadata prelock")
			require.NoError(t, observed.scanErr, "the candidate and its prior absence must come from an actual successful finite SQL Scan")
			require.NoError(t, observed.writerErr, "a separate writer error, lock timeout or fake result must not masquerade as the freshness refusal")
			require.Equal(t, 1, observed.prelocks, "actual Prepare must consume exactly its existing eligible metadata SHARE prelock before observation and retain that lock until outer settlement")
			require.True(t, observed.read.fits, "the actual old projection must fit the existing bound; oversize cannot explain this refusal")
			require.False(t, observed.read.structural, "the actual old observation must genuinely be negative before the independent positive writer commit")
			if tt.absent {
				require.Equal(t, confirmedPrepareObservationProjection{fits: true}, observed.read, "the original-key candidate must be truly absent with no id/version/receipt/scalar projection before the real insertion")
				confirmedPrepareObservationAssertAppearanceOnly(t, before, observed.writerGraph, original, f)
			} else {
				require.Equal(t, &f.assessment, observed.read.id, "the actual negative observation must retain the original candidate identity rather than forget it as if absent")
				require.NotEmpty(t, observed.read.version, "the actual existing uncertain candidate must retain its opaque original row version for the later fence")
				require.Equal(t, assessment.InputDigest, observed.read.digest, "bounded observation must retain the real original input digest")
				require.Equal(t, f.job.HeadSHA, observed.read.head, "bounded observation must retain the exact original captured head")
				require.Empty(t, observed.read.receipt, "the old actual uncertain state has no saved confirmed receipt yet")
				require.Nil(t, observed.read.reviewID, "the old actual uncertain state has no assessment provider identity yet")
				confirmedInstallerAssertConfirmationOnly(t, before, observed.writerGraph, f, receipt)
			}
			require.NotEmpty(t, observed.writerVersion, "independent committed readback must retain the actual appeared/updated A version")
			require.NotEqual(t, observed.read.version, observed.writerVersion, "the separate real writer must actually invalidate the retained version or prior absence before the INSERT")
			require.Equal(t, f.assessment, observed.writerAssessment.ID, "the positive writer must retain exact original assessment identity")
			require.Equal(t, f.job.OutputKey, observed.writerAssessment.PublicationKey, "the positive writer must retain the original output key rather than move to a sibling owner")
			require.Equal(t, models.CodeReviewPublicationConfirmed, observed.writerAssessment.PublicationState, "the independently committed row must contain actual confirmed publication")
			require.True(t, confirmedFullAssessmentReceiptMatches(observed.writerAssessment, observed.writerMetadata), "the actual committed exact receipt and staged fields must satisfy the unchanged worker reference after the old observation")
			require.Equal(t, foreignBefore, observed.writerForeign, "the separate original-A writer must leave every complete foreign row untouched")
			require.NotEqual(t, before, observed.writerGraph, "the hook must genuinely commit a persistent original candidate change, not merely call an empty writer")
			require.Equal(t, observed.writerGraph, after, "actual Prepare refusal must preserve the entire post-writer original/sibling graph, with no NEW child, wake, domain write or history mutation")
			require.Equal(t, foreignBefore, foreignAfter, "actual Prepare refusal and writer commit together must preserve the entire real foreign pending/history/wake graph")
			terminalInstallerOrderAbsent(t, ctx, reserved, f, in)
		})
	}
}
