package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// activatedLifecycleFixture is shared test setup for A/G protocol tests. Its
// synthetic receipts and active metadata are not a production activation owner,
// a fleet/drain certificate, provider conformance or mutation authority.
type activatedLifecycleFixture struct {
	orgID, sessionID, threadID, primaryThreadID uuid.UUID
	fence, sessionFence                         AdmissionFence
	want                                        lockedLifecycleScope
	cutover                                     lifecycleCutoverObservation
	normalization                               activatedLifecycleNormalizationFixture
	capability                                  activatedLifecycleCapabilityFixture
}

type activatedLifecycleNormalizationFixture struct {
	SchemaVersion  int       `json:"schema_version"`
	ReceiptID      uuid.UUID `json:"receipt_id"`
	FloorReceiptID uuid.UUID `json:"floor_receipt_id"`
	CutoffID       int64     `json:"cutoff_id"`
	Complete       bool      `json:"complete"`
}

type activatedLifecycleCapabilityFixture struct {
	SchemaVersion    int                                        `json:"schema_version"`
	ReleaseReceiptID uuid.UUID                                  `json:"release_receipt_id"`
	Tuples           []activatedLifecycleCapabilityTupleFixture `json:"tuples"`
}

type activatedLifecycleCapabilityTupleFixture struct {
	Provider             string    `json:"provider"`
	CLIBuild             string    `json:"cli_build"`
	ImageBuild           string    `json:"image_build"`
	ResumeIdentity       string    `json:"resume_identity"`
	VerificationProtocol string    `json:"verification_protocol"`
	ConformanceReceiptID uuid.UUID `json:"conformance_receipt_id"`
}

func seedActivatedLifecycleFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) activatedLifecycleFixture {
	t.Helper()
	workCtx, cancel := context.WithTimeout(ctx, LifecycleTransactionTimeout)
	defer cancel()
	tx, err := pool.BeginTx(workCtx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	require.NoError(t, err, "begin the test fixture's explicit READ COMMITTED transaction")
	defer func() { _ = tx.Rollback(context.Background()) }()
	require.NoError(t, ConfigureLifecycleTransactionInTx(workCtx, tx), "configure the supplied fixture transaction before installing any rows")
	fixture := seedActivatedLifecycleFixtureInTx(t, workCtx, tx)
	require.NoError(t, tx.Commit(workCtx), "commit synthetic scope stamps and cutover evidence as one fixture unit")
	return fixture
}

// seedActivatedLifecycleFixtureInTx leaves settlement with its supplied owner so
// tests can prove complete rollback. Every row is synthetic; no runnable job,
// runtime, provider call, binary advertisement or activation API is installed.
func seedActivatedLifecycleFixtureInTx(t *testing.T, ctx context.Context, tx pgx.Tx) activatedLifecycleFixture {
	t.Helper()
	require.NoError(t, requireLifecycleScopeTransactionInTx(ctx, tx), "fixture installation requires an already configured bounded transaction")
	orgID := uuid.New()
	_, err := tx.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic activated lifecycle tenant')`, orgID)
	require.NoError(t, err, "create an isolated synthetic tenant inside the supplied fixture transaction")
	session := models.Session{OrgID: orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, NewSessionStore(tx).CreateInTx(ctx, tx, &session), "create the real session and durable primary lane without a nested transaction")
	require.NotNil(t, session.PrimaryThreadID, "real session creation must return its exact durable primary lane")
	require.NoError(t, tryLockLifecycleCutoverExclusiveInTx(ctx, tx, orgID, session.ID), "hold the exact exclusive cutover interlock through every stamp and outer settlement")
	thread := models.SessionThread{OrgID: orgID, SessionID: session.ID, AgentType: models.DefaultDefaultAgentType, Label: "Synthetic explicit activation lane", Status: models.ThreadStatusIdle}
	require.NoError(t, NewSessionThreadStore(tx).Create(ctx, &thread, 2), "create a distinct explicit lane through the real transaction-bound store")

	const sessionEpoch, primaryEpoch, threadEpoch int64 = 3, 4, 5
	fence, err := NewAdmissionFence(session.ID, &thread.ID, sessionEpoch, requestLifecycleScopePointer(threadEpoch))
	require.NoError(t, err, "retain the exact explicit lane fence rather than infer a primary target")
	sessionFence, err := NewAdmissionFence(session.ID, nil, sessionEpoch, nil)
	require.NoError(t, err, "retain a separate session-wide fence with no inferred lane")
	fixture := activatedLifecycleFixture{
		orgID: orgID, sessionID: session.ID, threadID: thread.ID, primaryThreadID: *session.PrimaryThreadID,
		fence: fence, sessionFence: sessionFence,
		want: lockedLifecycleScope{
			session: lifecycleLockedSession{id: session.ID, orgID: orgID, status: models.SessionStatusIdle, epochs: LifecycleEpochState{AdmissionEpoch: sessionEpoch, LifecycleVersion: 1}},
			threads: []lifecycleLockedThread{
				{id: *session.PrimaryThreadID, orgID: orgID, sessionID: session.ID, status: models.ThreadStatusIdle, epochs: LifecycleEpochState{AdmissionEpoch: primaryEpoch, LifecycleVersion: 1}},
				{id: thread.ID, orgID: orgID, sessionID: session.ID, status: models.ThreadStatusIdle, epochs: LifecycleEpochState{AdmissionEpoch: threadEpoch, LifecycleVersion: 1}},
			},
		},
		normalization: activatedLifecycleNormalizationFixture{SchemaVersion: 1, ReceiptID: uuid.New(), FloorReceiptID: uuid.New(), CutoffID: 1, Complete: true},
		capability:    activatedLifecycleCapabilityFixture{SchemaVersion: 1, ReleaseReceiptID: uuid.New(), Tuples: []activatedLifecycleCapabilityTupleFixture{{Provider: string(session.AgentType), CLIBuild: "synthetic-cli-v1", ImageBuild: "synthetic-image-v1", ResumeIdentity: "native_conversation", VerificationProtocol: "synthetic-verify-v1", ConformanceReceiptID: uuid.New()}}},
	}
	slices.SortFunc(fixture.want.threads, func(a, b lifecycleLockedThread) int { return bytes.Compare(a.id[:], b.id[:]) })
	tag, err := tx.Exec(ctx, `UPDATE sessions SET admission_epoch=$3,end_epoch=NULL,lifecycle_version=1 WHERE org_id=$1 AND id=$2`, orgID, session.ID, sessionEpoch)
	require.NoError(t, err, "stamp only the exact parent inside the exclusive fixture transaction")
	require.Equal(t, int64(1), tag.RowsAffected(), "fixture installation must stamp exactly its own parent")
	tag, err = tx.Exec(ctx, `UPDATE session_threads SET admission_epoch=CASE WHEN id=$3 THEN $5::bigint ELSE $6::bigint END,end_epoch=NULL,lifecycle_version=1 WHERE org_id=$1 AND session_id=$2 AND id=ANY($4::uuid[])`, orgID, session.ID, thread.ID, []uuid.UUID{*session.PrimaryThreadID, thread.ID}, threadEpoch, primaryEpoch)
	require.NoError(t, err, "stamp both explicitly enumerated lanes in the same supplied transaction")
	require.Equal(t, int64(2), tag.RowsAffected(), "the primary and explicit lane must both carry protocol version one")
	normalization, err := json.Marshal(fixture.normalization)
	require.NoError(t, err, "encode the frozen synthetic normalization envelope")
	capability, err := json.Marshal(fixture.capability)
	require.NoError(t, err, "encode the exact synthetic provider CLI image identity and verification tuple")
	activatedAt, minimum := time.Date(2026, time.January, 2, 3, 4, 5, 123456000, time.UTC), 1
	fixture.cutover = lifecycleCutoverObservation{orgID: orgID, sessionID: session.ID, version: 3, state: models.LifecycleCutoverStateActive, legacyInputCutoffID: requestLifecycleScopePointer(fixture.normalization.CutoffID), normalization: normalization, activatedAt: &activatedAt, minimumWriterVersion: &minimum, resumabilityCapability: capability}
	err = tx.QueryRow(ctx, `INSERT INTO session_lifecycle_cutovers(org_id,session_id,version,state,legacy_input_cutoff_id,normalization,activated_at,minimum_writer_version,resumability_capability) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING normalization,activated_at,resumability_capability`, orgID, session.ID, fixture.cutover.version, fixture.cutover.state, fixture.cutover.legacyInputCutoffID, normalization, activatedAt, minimum, capability).
		Scan(&fixture.cutover.normalization, &fixture.cutover.activatedAt, &fixture.cutover.resumabilityCapability)
	require.NoError(t, err, "install and retain the exact PostgreSQL cutover evidence in the same commit as parent and lane stamps")
	return fixture
}

func TestActivatedLifecycleFixtureInvariantsPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fixture := seedActivatedLifecycleFixture(t, ctx, pool)
	workCtx, workCancel := context.WithTimeout(ctx, LifecycleTransactionTimeout)
	defer workCancel()
	tx, err := pool.BeginTx(workCtx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	require.NoError(t, err, "begin an independent reader of the committed activation fixture")
	defer func() { _ = tx.Rollback(context.Background()) }()
	require.NoError(t, ConfigureLifecycleTransactionInTx(workCtx, tx), "configure the exact-scope fixture reader")
	actualCutover, err := observeLifecycleCutoverInTx(workCtx, tx, fixture.orgID, fixture.sessionID)
	require.NoError(t, err, "read the committed cutover through the real shared interlock and tenant-scoped observer")
	require.Equal(t, fixture.cutover, actualCutover, "later protocol tests must receive every exact cutover field and canonical JSON byte")
	actual, err := lockLifecycleScopeInTx(workCtx, tx, fixture.orgID, lifecycleScope{sessionID: fixture.sessionID, threadIDs: []uuid.UUID{fixture.threadID, fixture.primaryThreadID}})
	require.NoError(t, err, "read the exact parent and both explicitly selected lanes through the real scope kernel")
	require.Equal(t, fixture.want, actual, "all fixture statuses visibility fields nonnegative epochs and protocol stamps must match exactly")
	require.NotEqual(t, fixture.primaryThreadID, fixture.threadID, "the admission fence must name an explicit lane distinct from the primary lane")
	wantFence, err := NewAdmissionFence(fixture.sessionID, &fixture.threadID, 3, requestLifecycleScopePointer(int64(5)))
	require.NoError(t, err, "construct the independent expected explicit admission fence")
	require.Equal(t, wantFence, fixture.fence, "the returned fence must preserve exactly the stamped explicit lane epochs")
	wantSessionFence, err := NewAdmissionFence(fixture.sessionID, nil, 3, nil)
	require.NoError(t, err, "construct the independent expected session-wide fence")
	require.Equal(t, wantSessionFence, fixture.sessionFence, "session-wide tests must receive no inferred lane or lane epoch")
	require.Equal(t, int64(3), actualCutover.version, "the fixture must retain cutover CAS revision three independently of the protocol stamp")
	require.Equal(t, []int16{1, 1, 1}, []int16{actual.session.epochs.LifecycleVersion, actual.threads[0].epochs.LifecycleVersion, actual.threads[1].epochs.LifecycleVersion}, "the parent and both lanes must retain protocol stamp one independently of the cutover CAS revision")
	require.NotEqual(t, actualCutover.version, int64(actual.session.epochs.LifecycleVersion), "a cutover CAS revision must never be interpreted as the lifecycle protocol stamp")
	require.Equal(t, models.LifecycleCutoverStateActive, actualCutover.state, "the fixture's persisted state must be explicitly active")
	require.Equal(t, requestLifecycleScopePointer(int64(1)), actualCutover.legacyInputCutoffID, "the positive synthetic watermark must satisfy the existing full-schema cutoff constraint")
	require.Equal(t, requestLifecycleScopePointer(1), actualCutover.minimumWriterVersion, "the fixture must require positive writer version one")
	provider := models.AgentType(fixture.capability.Tuples[0].Provider)
	require.NoError(t, provider.Validate(), "the tested capability tuple must name a supported provider rather than an invented agent type")
	var sessionProvider, primaryProvider, threadProvider models.AgentType
	err = tx.QueryRow(workCtx, `SELECT
		(SELECT agent_type FROM sessions WHERE org_id=$1 AND id=$2),
		(SELECT agent_type FROM session_threads WHERE org_id=$1 AND session_id=$2 AND id=$3),
		(SELECT agent_type FROM session_threads WHERE org_id=$1 AND session_id=$2 AND id=$4)`, fixture.orgID, fixture.sessionID, fixture.primaryThreadID, fixture.threadID).
		Scan(&sessionProvider, &primaryProvider, &threadProvider)
	require.NoError(t, err, "read actual provider identities from the exact persisted parent primary lane and explicit lane")
	require.Equal(t, []models.AgentType{provider, provider, provider}, []models.AgentType{sessionProvider, primaryProvider, threadProvider}, "the tested provider tuple must cover the actual parent and every runnable lane in the fixture")
	require.Equal(t, []int64{1, 1, 2, 1, 0, 0, 0, 0, 0}, activatedLifecycleFixtureCounts(t, ctx, pool, fixture.orgID, fixture.sessionID), "activation setup must create only its tenant parent two lanes and cutover with no jobs dispatches executors runtimes or inbox entries")
	require.JSONEq(t, fmt.Sprintf(`{"schema_version":1,"receipt_id":%q,"floor_receipt_id":%q,"cutoff_id":1,"complete":true}`, fixture.normalization.ReceiptID, fixture.normalization.FloorReceiptID), string(actualCutover.normalization), "normalization metadata must use exactly the frozen synthetic receipt envelope")
	require.JSONEq(t, fmt.Sprintf(`{"schema_version":1,"release_receipt_id":%q,"tuples":[{"provider":%q,"cli_build":"synthetic-cli-v1","image_build":"synthetic-image-v1","resume_identity":"native_conversation","verification_protocol":"synthetic-verify-v1","conformance_receipt_id":%q}]}`, fixture.capability.ReleaseReceiptID, models.DefaultDefaultAgentType, fixture.capability.Tuples[0].ConformanceReceiptID), string(actualCutover.resumabilityCapability), "capability metadata must retain the actual provider and synthetic build tuple without implying real provider conformance")
}

func TestActivatedLifecycleFixtureAtomicSettlementPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	tests := []struct {
		name   string
		commit bool
		want   []int64
	}{
		{name: "commit installs one complete fixture", commit: true, want: []int64{1, 1, 2, 1, 0, 0, 0, 0, 0}},
		{name: "rollback installs no fixture rows", want: []int64{0, 0, 0, 0, 0, 0, 0, 0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			workCtx, workCancel := context.WithTimeout(ctx, LifecycleTransactionTimeout)
			defer workCancel()
			tx, err := pool.BeginTx(workCtx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			require.NoError(t, err, "the test must retain ownership of the fixture installation transaction")
			defer func() { _ = tx.Rollback(context.Background()) }()
			require.NoError(t, ConfigureLifecycleTransactionInTx(workCtx, tx), "configure the supplied owner before fixture installation")
			fixture := seedActivatedLifecycleFixtureInTx(t, workCtx, tx)
			require.Equal(t, []int64{1, 1, 2, 1, 0, 0, 0, 0, 0}, activatedLifecycleFixtureCounts(t, workCtx, tx, fixture.orgID, fixture.sessionID), "the supplied owner must see its entire staged fixture before settlement")
			require.Equal(t, []int64{0, 0, 0, 0, 0, 0, 0, 0, 0}, activatedLifecycleFixtureCounts(t, workCtx, pool, fixture.orgID, fixture.sessionID), "a separate connection must see no partial installation before the outer owner settles")
			contender, err := pool.BeginTx(workCtx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			require.NoError(t, err, "begin an independent contender for the exact staged cutover key")
			defer func() { _ = contender.Rollback(context.Background()) }()
			require.NoError(t, ConfigureLifecycleTransactionInTx(workCtx, contender), "configure the independent interlock contender")
			var busy *lifecycleCutoverInterlockBusyError
			require.ErrorAs(t, tryLockLifecycleCutoverSharedInTx(workCtx, contender, fixture.orgID, fixture.sessionID), &busy, "the supplied fixture owner must retain exclusive interlock until its outer commit or rollback")
			if tt.commit {
				require.NoError(t, tx.Commit(workCtx), "only the supplied owner may commit the complete synthetic activation unit")
			} else {
				require.NoError(t, tx.Rollback(workCtx), "rolling back the supplied owner must discard all fixture rows and stamps")
			}
			require.Equal(t, tt.want, activatedLifecycleFixtureCounts(t, workCtx, contender, fixture.orgID, fixture.sessionID), "outer settlement must install every fixture component or none in the independent READ COMMITTED observer")
			require.NoError(t, tryLockLifecycleCutoverSharedInTx(workCtx, contender, fixture.orgID, fixture.sessionID), "both commit and rollback must release the exact exclusive fixture interlock")
		})
	}
}

func TestActivatedLifecycleFixtureTenantIsolationPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	tests := []struct {
		name    string
		reverse bool
	}{
		{name: "first tenant cannot read second scope"},
		{name: "second tenant cannot read first scope", reverse: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			first := seedActivatedLifecycleFixture(t, ctx, pool)
			second := seedActivatedLifecycleFixture(t, ctx, pool)
			require.NotEqual(t, first.orgID, second.orgID, "each shared fixture call must create a separate synthetic tenant")
			require.NotEqual(t, first.normalization, second.normalization, "normalization and floor receipt identities must belong to only one fixture")
			require.NotEqual(t, first.capability, second.capability, "release and conformance receipts must remain isolated across fixtures")
			queryOrg, scope := first.orgID, second
			if tt.reverse {
				queryOrg, scope = second.orgID, first
			}
			workCtx, workCancel := context.WithTimeout(ctx, LifecycleTransactionTimeout)
			defer workCancel()
			tx, err := pool.BeginTx(workCtx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			require.NoError(t, err, "begin an independent reader with deliberately mismatched tenant scope")
			defer func() { _ = tx.Rollback(context.Background()) }()
			require.NoError(t, ConfigureLifecycleTransactionInTx(workCtx, tx), "configure the mismatched-scope reader without granting row authority")
			actual, err := lockLifecycleScopeInTx(workCtx, tx, queryOrg, lifecycleScope{sessionID: scope.sessionID, threadIDs: []uuid.UUID{scope.threadID, scope.primaryThreadID}})
			require.ErrorIs(t, err, pgx.ErrNoRows, "foreign tenant parent and lane identities must not resolve through the fixture")
			require.Equal(t, lockedLifecycleScope{}, actual, "a failed tenant lookup must expose no partial parent or lane metadata")
			cutover, err := observeLifecycleCutoverInTx(workCtx, tx, queryOrg, scope.sessionID)
			require.ErrorIs(t, err, pgx.ErrNoRows, "cutover evidence must require the same exact tenant and session as its stamped rows")
			require.Equal(t, lifecycleCutoverObservation{}, cutover, "foreign tenant observation must expose no active cutover evidence")
			require.Equal(t, []int64{1, 0, 0, 0, 0, 0, 0, 0, 0}, activatedLifecycleFixtureCounts(t, ctx, pool, queryOrg, scope.sessionID), "tenant-scoped SQL must find only the query tenant and no foreign lifecycle rows")
		})
	}
}

func activatedLifecycleFixtureCounts(t *testing.T, ctx context.Context, database DBTX, orgID, sessionID uuid.UUID) []int64 {
	t.Helper()
	counts := make([]int64, 9)
	err := database.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM organizations WHERE id=$1),
		(SELECT count(*) FROM sessions WHERE org_id=$1 AND id=$2),
		(SELECT count(*) FROM session_threads WHERE org_id=$1 AND session_id=$2),
		(SELECT count(*) FROM session_lifecycle_cutovers WHERE org_id=$1 AND session_id=$2),
		(SELECT count(*) FROM jobs WHERE org_id=$1),
		(SELECT count(*) FROM session_input_dispatches WHERE org_id=$1 AND session_id=$2),
		(SELECT count(*) FROM session_executors WHERE org_id=$1 AND session_id=$2),
		(SELECT count(*) FROM thread_runtimes WHERE org_id=$1 AND session_id=$2),
		(SELECT count(*) FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2)`, orgID, sessionID).
		Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5], &counts[6], &counts[7], &counts[8])
	require.NoError(t, err, "read exact tenant-scoped fixture components and prove setup creates no executable work")
	return counts
}
