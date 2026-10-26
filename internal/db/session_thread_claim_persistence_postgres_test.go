package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestThreadClaimPersistenceFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name, kind, outcome  string
		status               models.ThreadStatus
		siblings, maxRunning int
		archivedSibling      bool
		wantErr              error
	}
	tests := []testCase{}
	for _, kind := range []string{"idle", "resume"} {
		status := models.ThreadStatusIdle
		if kind == "resume" {
			status = models.ThreadStatusCompleted
		}
		for _, base := range []testCase{
			{name: "commit", outcome: "commit", maxRunning: 3},
			{name: "rollback", outcome: "rollback", maxRunning: 3},
			{name: "foreign organization", outcome: "foreign", maxRunning: 3, wantErr: pgx.ErrNoRows},
			{name: "wrong session", outcome: "session", maxRunning: 3, wantErr: pgx.ErrNoRows},
			{name: "archived lane", outcome: "archived", maxRunning: 3, wantErr: pgx.ErrNoRows},
			{name: "wrong status", outcome: "ineligible", maxRunning: 3, wantErr: pgx.ErrNoRows},
			{name: "third active lane allowed", outcome: "commit", siblings: 2, maxRunning: 3},
			{name: "fourth active lane returns mutation miss", outcome: "cap", siblings: 3, maxRunning: 3, wantErr: pgx.ErrNoRows},
			{name: "zero cap defaults to three", outcome: "commit", siblings: 2},
			{name: "negative cap defaults to three", outcome: "cap", siblings: 3, maxRunning: -1, wantErr: pgx.ErrNoRows},
			{name: "archived sibling does not consume cap", outcome: "commit", siblings: 3, maxRunning: 3, archivedSibling: true},
		} {
			base.name, base.kind, base.status = kind+"/"+base.name, kind, status
			if base.outcome == "ineligible" {
				base.status = models.ThreadStatusRunning
			}
			tests = append(tests, base)
		}
	}
	for _, status := range []models.ThreadStatus{models.ThreadStatusFailed, models.ThreadStatusCancelled, models.ThreadStatusAwaitingInput} {
		tests = append(tests, testCase{name: "resume/" + string(status), kind: "resume", outcome: "commit", status: status, maxRunning: 3})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := testpostgres.NewPool(t, context.Background(), "../../migrations", 2)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			f := seedThreadPersistencePostgres(t, ctx, pool)
			_, err := pool.Exec(ctx, "UPDATE session_threads SET status=$3,pending_message_count=2 WHERE org_id=$1 AND id=$2", f.orgID, f.threadID, tt.status)
			require.NoError(t, err, "seed the legacy claim status without dropping prior runtime or failure fields")
			for i := 0; i < tt.siblings; i++ {
				sibling := models.SessionThread{OrgID: f.orgID, SessionID: f.sessionID, AgentType: models.DefaultDefaultAgentType, Label: "Synthetic active sibling", Status: []models.ThreadStatus{models.ThreadStatusPending, models.ThreadStatusRunning, models.ThreadStatusAwaitingInput}[i]}
				require.NoError(t, NewSessionThreadStore(pool).Create(ctx, &sibling, 10), "create an actual sibling through the legacy persistence path before checking the supported three-lane cap")
				if tt.archivedSibling && i == tt.siblings-1 {
					_, err := pool.Exec(ctx, "UPDATE session_threads SET archived_at=$3 WHERE org_id=$1 AND id=$2", f.orgID, sibling.ID, f.at)
					require.NoError(t, err, "retain an archived sibling row without counting it as active legacy occupancy")
				}
			}
			if tt.outcome == "archived" {
				_, err := pool.Exec(ctx, "UPDATE session_threads SET archived_at=$3 WHERE org_id=$1 AND id=$2", f.orgID, f.threadID, f.at)
				require.NoError(t, err, "archive the exact target before characterizing the unchanged claim predicate")
			}
			before := claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID)
			owner := beginClaimPersistencePostgresOwner(t, ctx, pool)
			tx := &sessionPersistenceOwnedTx{Tx: owner}
			var transactionID int64
			var at time.Time
			require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current(),now()").Scan(&transactionID, &at), "record the outer transaction's identity and timestamp before claiming its explicit lane")
			_, err = owner.Exec(ctx, "UPDATE sessions SET title='Synthetic outer lane unit' WHERE org_id=$1 AND id=$2", f.orgID, f.sessionID)
			require.NoError(t, err, "stage a parent write so rejection must roll back the complete real outer unit")
			stagedBefore := claimPersistencePostgresRows(t, ctx, owner, f.orgID, f.sessionID)
			baseline := threadPersistencePostgresThread(t, ctx, owner, f)
			store, redis := threadPersistenceStoreWithStreams(t, nil)
			requested := f
			if tt.outcome == "foreign" {
				requested.orgID = uuid.New()
			} else if tt.outcome == "session" {
				requested.sessionID = uuid.New()
			}
			actual, effects, claimErr := callThreadClaimInTx(ctx, store, tx, requested, tt.kind, tt.maxRunning)
			require.Empty(t, tx.settlements, "lane claims must retain the caller's exact transaction without inner boundaries or retries")
			require.Empty(t, redis.Keys(), "enabled runtime publishers must remain silent before the whole unit commits")
			require.Equal(t, before, claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID), "the independent reader must see the complete original parent and all sibling rows while the owner is uncommitted")
			requireClaimPersistencePostgresOwner(t, ctx, owner, transactionID)
			if tt.wantErr != nil {
				require.Equal(t, tt.wantErr, claimErr, "all InTx scope, visibility, status and supported-cap misses must remain raw no-row results until after owner settlement")
				require.Equal(t, models.SessionThread{}, actual, "rejected lane claims must return no accepted runtime row")
				require.Equal(t, ThreadClaimPersistenceEffects{}, effects, "rejected lane claims must return no deferred event")
				require.Equal(t, stagedBefore, claimPersistencePostgresRows(t, ctx, owner, f.orgID, f.sessionID), "a mutation miss must not change any earlier staged row or sibling or run an in-transaction diagnostic")
				require.NoError(t, owner.Rollback(ctx), "only the owner may abort the earlier parent write and rejected lane claim together")
				require.Equal(t, before, claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID), "outer rollback must restore every original raw row after a rejected lane claim")
				require.Empty(t, redis.Keys(), "rejected lane claims must leave enabled Redis entirely unchanged")
				return
			}
			require.NoError(t, claimErr, "the lane claim must complete using only tx despite a nil receiver database and occupied owner pool")
			expected := baseline
			expected.Status, expected.StartedAt, expected.LastActivityAt, expected.CancelRequestedAt = models.ThreadStatusRunning, &at, &at, nil
			fields := []string{"status", "started_at", "last_activity_at", "cancel_requested_at"}
			if tt.kind == "resume" {
				expected.CompletedAt = nil
				fields = append(fields, "completed_at")
			}
			require.Equal(t, expected, actual, "the exact returned row must preserve all legacy fields, including idle completion and resume failure metadata")
			expectedEvent := models.NewThreadRuntimeEvent(expected)
			require.Equal(t, ThreadClaimPersistenceEffects{runtime: expectedEvent, runtimeChanged: true}, effects, "claim effects must capture the complete returned runtime rather than a later by-ID reload")
			require.Equal(t, expected, threadPersistencePostgresThread(t, ctx, owner, f), "owner-visible persisted lane fields must exactly match the claimed projection")
			staged := claimPersistencePostgresRows(t, ctx, owner, f.orgID, f.sessionID)
			requireClaimPersistencePostgresDelta(t, stagedBefore, staged, f.threadID, fields)
			require.NotEqual(t, stagedBefore, staged, "rollback proof must follow an actual lane mutation, not just a no-op claim")
			if tt.outcome == "rollback" {
				require.NoError(t, owner.Rollback(ctx), "the owner may reject a successful lane claim as part of its larger unit")
				require.Equal(t, before, claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID), "rollback must atomically restore every parent, target, sibling and side-table column")
				require.Empty(t, redis.Keys(), "discarded claim effects must never advertise rolled-back runtime state")
				return
			}
			require.NoError(t, owner.Commit(ctx), "only the owner may make its parent write and lane claim durable")
			require.Equal(t, staged, claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID), "commit must store exactly the complete owner-visible v0 mutation")
			require.Empty(t, redis.Keys(), "committing the database cannot publish the captured effects implicitly")
			_, err = pool.Exec(ctx, `UPDATE session_threads SET status='pending',current_turn=19,pending_message_count=23,
			 agent_session_id='synthetic later provider',failure_explanation='synthetic later failure',last_activity_at=$3
			 WHERE org_id=$1 AND id=$2`, f.orgID, f.threadID, at.Add(time.Minute))
			require.NoError(t, err, "create a different later committed row before publishing the earlier claim snapshot")
			// Mutating the returned row must not alias the opaque effects.
			if actual.AgentSessionID != nil {
				*actual.AgentSessionID = "synthetic returned-row mutation"
			}
			if actual.LastActivityAt != nil {
				*actual.LastActivityAt = at.Add(2 * time.Minute)
			}
			if actual.StartedAt != nil {
				*actual.StartedAt = at.Add(3 * time.Minute)
			}
			if actual.CompletedAt != nil {
				*actual.CompletedAt = at.Add(4 * time.Minute)
			}
			if actual.FailureExplanation != nil {
				*actual.FailureExplanation = "synthetic returned failure mutation"
			}
			if actual.FailureCategory != nil {
				*actual.FailureCategory = "synthetic_returned_category"
			}
			store.PublishCommittedThreadClaimEffects(ctx, uuid.New(), effects)
			require.Empty(t, redis.Keys(), "an explicit organization mismatch must refuse another tenant's captured claim event")
			store.PublishCommittedThreadClaimEffects(ctx, f.orgID, effects)
			requireThreadPersistenceEvent(t, redis, expectedEvent)
		})
	}
}

// This proves only the persistence/diagnostic primitive boundary. It does not
// exercise SendMessage, its callback, admission fences or uncertain-commit logic.
func TestThreadClaimDiagnosticAfterOwnerCommitPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind   string
		status models.ThreadStatus
	}{
		{kind: "idle", status: models.ThreadStatusIdle},
		{kind: "resume", status: models.ThreadStatusCompleted},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			t.Parallel()
			pool := testpostgres.NewPool(t, context.Background(), "../../migrations", 2)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			f := seedThreadPersistencePostgres(t, ctx, pool)
			_, err := pool.Exec(ctx, "UPDATE sessions SET status='idle' WHERE org_id=$1 AND id=$2", f.orgID, f.sessionID)
			require.NoError(t, err, "seed a real idle parent claim in the outer unit rather than a stand-in callback")
			_, err = pool.Exec(ctx, "UPDATE session_threads SET status=$3 WHERE org_id=$1 AND id=$2", f.orgID, f.threadID, tt.status)
			require.NoError(t, err, "seed the lane's fixed legacy claimable status before reaching the supported cap")
			var changedSibling uuid.UUID
			for _, status := range []models.ThreadStatus{models.ThreadStatusPending, models.ThreadStatusRunning, models.ThreadStatusAwaitingInput} {
				sibling := models.SessionThread{OrgID: f.orgID, SessionID: f.sessionID, AgentType: models.DefaultDefaultAgentType, Label: "Synthetic diagnostic sibling", Status: status}
				require.NoError(t, NewSessionThreadStore(pool).Create(ctx, &sibling, 10), "create exactly three active siblings so the original mutation has no returned row")
				changedSibling = sibling.ID
			}
			before := claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID)
			owner := beginClaimPersistencePostgresOwner(t, ctx, pool)
			tx := &sessionPersistenceOwnedTx{Tx: owner}
			var ownerPID int
			var at time.Time
			require.NoError(t, owner.QueryRow(ctx, "SELECT pg_backend_pid(),now()").Scan(&ownerPID, &at), "record the exact owning connection and parent-claim timestamp")
			parentBefore := claimPersistencePostgresSession(t, ctx, owner, f.orgID, f.sessionID)
			parent, err := NewSessionStore(nil).ClaimIdleInTx(ctx, f.orgID, tx, f.sessionID)
			require.NoError(t, err, "the parent claim must remain staged in the same known owner unit")
			require.Equal(t, expectedSessionClaimPersistencePostgres(parentBefore, "idle", at), parent, "the primitive composition must retain the exact legacy parent claim")
			store, redis := threadPersistenceStoreWithStreams(t, nil)
			thread, effects, miss := callThreadClaimInTx(ctx, store, tx, f, tt.kind, models.MaxRunningThreadsPerSession)
			require.Equal(t, pgx.ErrNoRows, miss, "the InTx claim must return only its mutation miss without classifying the still-locked cap")
			require.Equal(t, models.SessionThread{}, thread, "the missed lane mutation must expose no accepted row")
			require.Equal(t, ThreadClaimPersistenceEffects{}, effects, "the missed lane mutation must expose no publishable claim event")
			require.Empty(t, tx.settlements, "neither parent nor lane primitive may settle the caller's transaction")
			require.Empty(t, redis.Keys(), "no primitive may publish for the uncommitted parent claim or lane miss")
			require.Equal(t, before, claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID), "a second connection must still see the exact complete pre-unit graph")
			staged := claimPersistencePostgresRows(t, ctx, owner, f.orgID, f.sessionID)
			requireClaimPersistencePostgresDelta(t, before, staged, uuid.Nil, []string{"status", "started_at", "last_activity_at", "completed_at", "error", "failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised", "runtime_soft_deadline_at", "runtime_hard_deadline_at"})
			contender, err := pool.Begin(ctx)
			require.NoError(t, err, "open the independent connection that will change occupancy only after commit")
			defer func() { _ = contender.Rollback(context.Background()) }()
			var contenderPID int
			require.NoError(t, contender.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&contenderPID), "record the actual second backend before probing the mutation's retained locks")
			require.NotEqual(t, ownerPID, contenderPID, "the occupancy writer must not be the owning transaction's physical connection")
			var lockedID uuid.UUID
			err = contender.QueryRow(ctx, "SELECT id FROM session_threads WHERE org_id=$1 AND id=$2 FOR UPDATE NOWAIT", f.orgID, changedSibling).Scan(&lockedID)
			requireSessionLifecycleSQLState(t, err, "55P03", "the mutation miss must retain its real sibling row locks until the owner settles")
			require.NoError(t, contender.Rollback(ctx), "discard the deliberate NOWAIT probe before the owner commits")
			require.NoError(t, owner.Commit(ctx), "the owner must confirm the parent-claim unit commit before any separate diagnostic")
			require.Equal(t, staged, claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID), "the confirmed unit must make exactly its staged parent claim durable despite the raw lane miss")
			later, err := pool.Begin(ctx)
			require.NoError(t, err, "only after known owner commit may the second pool begin changing sibling occupancy")
			defer func() { _ = later.Rollback(context.Background()) }()
			_, err = later.Exec(ctx, "SET LOCAL lock_timeout='250ms'")
			require.NoError(t, err, "bound the fixture's post-commit row-lock acquisition rather than waiting indefinitely")
			tag, err := later.Exec(ctx, "UPDATE session_threads SET status='completed' WHERE org_id=$1 AND id=$2", f.orgID, changedSibling)
			require.NoError(t, err, "the owner commit must have released the claim's sibling locks for a different connection")
			require.Equal(t, int64(1), tag.RowsAffected(), "the independent writer must change one actual previously active sibling")
			require.NoError(t, later.Commit(ctx), "make the later occupancy reduction visible before the separate diagnostic")
			after := claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID)
			requireClaimPersistencePostgresDelta(t, staged, after, changedSibling, []string{"status"})
			limited, err := callThreadClaimLimitDiagnostic(ctx, NewSessionThreadStore(pool), f, tt.kind, models.MaxRunningThreadsPerSession)
			require.NoError(t, err, "the post-commit diagnostic must read current state without reacquiring claim locks or publishing")
			require.False(t, limited, "the separate diagnostic must observe two now-active siblings, not the three that caused the earlier mutation miss")
			require.Equal(t, pgx.ErrNoRows, miss, "a false later diagnostic leaves the original mutation miss for the future caller's legacy mapping")
			require.Equal(t, after, claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID), "the post-commit diagnostic must leave every complete parent and sibling row unchanged")
			require.Empty(t, redis.Keys(), "neither a missed claim nor its separate read-only diagnostic may publish an event")
		})
	}
}

func TestThreadPendingClaimPersistenceFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, outcome string
		wantErr       error
	}{
		{name: "commit then reload later count", outcome: "commit"},
		{name: "caller rollback", outcome: "rollback"},
		{name: "foreign organization", outcome: "foreign", wantErr: pgx.ErrNoRows},
		{name: "archived lane", outcome: "archived", wantErr: pgx.ErrNoRows},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := testpostgres.NewPool(t, context.Background(), "../../migrations", 2)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			f := seedThreadPersistencePostgres(t, ctx, pool)
			_, err := pool.Exec(ctx, "UPDATE session_threads SET pending_message_count=2 WHERE org_id=$1 AND id=$2", f.orgID, f.threadID)
			require.NoError(t, err, "seed a nonzero pending counter to prove an exact increment rather than replacement")
			if tt.outcome == "archived" {
				_, err := pool.Exec(ctx, "UPDATE session_threads SET archived_at=$3 WHERE org_id=$1 AND id=$2", f.orgID, f.threadID, f.at)
				require.NoError(t, err, "archive only the exact target before testing pending visibility")
			}
			before := claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID)
			owner := beginClaimPersistencePostgresOwner(t, ctx, pool)
			tx := &sessionPersistenceOwnedTx{Tx: owner}
			var transactionID int64
			require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current()").Scan(&transactionID), "record the original owner transaction before incrementing pending work")
			_, err = owner.Exec(ctx, "UPDATE sessions SET title='Synthetic outer pending unit' WHERE org_id=$1 AND id=$2", f.orgID, f.sessionID)
			require.NoError(t, err, "stage an earlier real parent write that must roll back with any pending increment")
			stagedBefore := claimPersistencePostgresRows(t, ctx, owner, f.orgID, f.sessionID)
			baseline := threadPersistencePostgresThread(t, ctx, owner, f)
			store, redis := threadPersistenceStoreWithStreams(t, nil)
			orgID := f.orgID
			if tt.outcome == "foreign" {
				orgID = uuid.New()
			}
			effects, incrementErr := store.IncrementPendingMessagesInTx(ctx, orgID, tx, f.threadID)
			require.Empty(t, tx.settlements, "pending increments must not settle, replace or retry their owner's transaction")
			require.Empty(t, redis.Keys(), "enabled inbox publishers must remain silent before the increment commits")
			require.Equal(t, before, claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID), "an independent pool read must see no uncommitted parent or pending-counter changes")
			requireClaimPersistencePostgresOwner(t, ctx, owner, transactionID)
			if tt.wantErr != nil {
				require.Equal(t, tt.wantErr, incrementErr, "pending scope and archive rejection must retain the raw legacy missing-row error")
				require.Equal(t, ThreadPendingMessagePersistenceEffects{}, effects, "a rejected pending increment must return no deferred reload identity")
				require.Equal(t, stagedBefore, claimPersistencePostgresRows(t, ctx, owner, f.orgID, f.sessionID), "a rejected pending write must leave the earlier owner unit entirely unchanged")
				require.NoError(t, owner.Rollback(ctx), "the owner alone aborts its earlier write and rejected pending increment")
				require.Equal(t, before, claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID), "rollback must restore every original row after pending rejection")
				require.Empty(t, redis.Keys(), "rejected and rolled-back pending work must never publish")
				return
			}
			require.NoError(t, incrementErr, "pending increments must use only the supplied transaction with no receiver fallback")
			require.Equal(t, ThreadPendingMessagePersistenceEffects{orgID: f.orgID, threadID: f.threadID, inboxQueued: true}, effects, "pending effects intentionally retain only the exact scoped later-reload identity")
			expected := baseline
			expected.PendingMessageCount++
			require.Equal(t, expected, threadPersistencePostgresThread(t, ctx, owner, f), "pending persistence must change exactly the counter, preserving all timestamps, statuses and result fields")
			staged := claimPersistencePostgresRows(t, ctx, owner, f.orgID, f.sessionID)
			requireClaimPersistencePostgresDelta(t, stagedBefore, staged, f.threadID, []string{"pending_message_count"})
			require.NotEqual(t, stagedBefore, staged, "the rollback case must contain a genuine staged counter increment")
			if tt.outcome == "rollback" {
				require.NoError(t, owner.Rollback(ctx), "the owner may reject a successful increment with its entire larger unit")
				require.Equal(t, before, claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID), "rollback must restore all original parent and thread fields, not only the pending counter")
				require.Empty(t, redis.Keys(), "discarded pending effects must not emit an inbox event")
				return
			}
			require.NoError(t, owner.Commit(ctx), "only the owner may commit pending persistence")
			require.Equal(t, staged, claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID), "commit must expose exactly the staged complete row graph")
			require.Empty(t, redis.Keys(), "database commit alone cannot invoke a pending reload publisher")
			_, err = pool.Exec(ctx, "UPDATE session_threads SET pending_message_count=31,current_turn=19 WHERE org_id=$1 AND id=$2", f.orgID, f.threadID)
			require.NoError(t, err, "persist a later count so snapshot publication would be observably wrong")
			publisher := NewSessionThreadStore(pool)
			publisher.SetStreams(store.streams)
			publisher.PublishCommittedThreadPendingMessageEffects(ctx, uuid.New(), effects)
			require.Empty(t, redis.Keys(), "organization mismatch must refuse the pending reload before publication")
			publisher.PublishCommittedThreadPendingMessageEffects(ctx, f.orgID, effects)
			requireThreadPendingClaimPersistencePostgresEvent(t, redis, models.ThreadInboxEvent{SessionID: f.sessionID, ThreadID: f.threadID, OrgID: f.orgID, PendingMessageCount: 31})
		})
	}
}

func requireThreadPendingClaimPersistencePostgresEvent(t *testing.T, redis *miniredis.Miniredis, expected models.ThreadInboxEvent) {
	t.Helper()
	entries, err := redis.Stream("143:stream:{ses:" + expected.SessionID.String() + "}:events")
	require.NoError(t, err, "the explicit pending adapter must publish through the established session event stream")
	var actual []models.SessionStreamEvent
	for _, entry := range entries {
		var event models.SessionStreamEvent
		require.NoError(t, json.Unmarshal([]byte(entry.Values[1]), &event), "decode the entire later-row inbox event")
		actual = append(actual, event)
	}
	encoded, err := json.Marshal(expected)
	require.NoError(t, err, "encode every expected later-row inbox field")
	var data any
	require.NoError(t, json.Unmarshal(encoded, &data), "normalize only the interface payload for exact full event comparison")
	require.Equal(t, []models.SessionStreamEvent{{Type: models.SessionStreamEventThreadInboxQueued, SessionID: expected.SessionID, OrgID: expected.OrgID, Data: data}}, actual, "pending publication must reload the later visible count, unlike immutable claim snapshot publication")
}
