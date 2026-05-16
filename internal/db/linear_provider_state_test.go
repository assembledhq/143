package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestLinearProviderStateStore_Get(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(mock pgxmock.PgxPoolIface)
		expected    LinearProviderState
		expectedErr string
	}{
		{
			name: "decodes state",
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT state FROM session_issue_link_provider_state").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow([]byte(`{"identifier":"ACS-1","attachment_id":"att-1","coexists_with_github_integration":true}`)))
			},
			expected: LinearProviderState{Identifier: "ACS-1", AttachmentID: "att-1", CoexistsWithGitHubIntegration: BoolPtr(true)},
		},
		{
			name: "returns zero value for missing row",
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT state FROM session_issue_link_provider_state").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(pgx.ErrNoRows)
			},
			expected: LinearProviderState{},
		},
		{
			name: "wraps query errors",
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT state FROM session_issue_link_provider_state").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("db unavailable"))
			},
			expectedErr: "query linear provider state",
		},
		{
			name: "wraps decode errors",
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT state FROM session_issue_link_provider_state").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow([]byte(`{bad json`)))
			},
			expectedErr: "decode linear provider state",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			tt.setup(mock)
			got, err := NewLinearProviderStateStore(mock).Get(context.Background(), uuid.New(), uuid.New())
			if tt.expectedErr != "" {
				require.Error(t, err, "Get should return the expected error")
				require.Contains(t, err.Error(), tt.expectedErr, "Get should wrap errors with context")
			} else {
				require.NoError(t, err, "Get should succeed")
				require.Equal(t, tt.expected, got, "Get should return the expected provider state")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestLinearProviderStateStore_UpsertAndMerge(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewLinearProviderStateStore(mock)
	orgID := uuid.New()
	linkID := uuid.New()

	mock.ExpectExec("INSERT INTO session_issue_link_provider_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	require.NoError(t, store.Upsert(context.Background(), orgID, linkID, LinearProviderState{Identifier: "ACS-1"}), "Upsert should write encoded state")

	// Merge runs inside a transaction with SELECT ... FOR UPDATE: begin,
	// locked read of the current state, write the merged blob, commit.
	// Without the row lock, two concurrent Merge calls could each read
	// pre-merge state and the second Upsert would clobber the first.
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT state FROM session_issue_link_provider_state[\s\S]+FOR UPDATE`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow([]byte(`{"identifier":"ACS-1","comment_id":"comment-1"}`)))
	mock.ExpectExec("INSERT INTO session_issue_link_provider_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	require.NoError(t, store.Merge(context.Background(), orgID, linkID, LinearProviderState{LastWriteOutcome: "merged"}), "Merge should read-lock, merge, and upsert provider state in one transaction")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestLinearProviderStateStore_MergeLocksReadForUpdate pins the row-lock
// behavior that prevents concurrent Merge calls from losing updates. The
// SELECT must carry FOR UPDATE so the second Merge blocks until the first
// commits, then re-reads the now-updated state instead of merging against
// stale rows.
func TestLinearProviderStateStore_MergeLocksReadForUpdate(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewLinearProviderStateStore(mock)
	orgID := uuid.New()
	linkID := uuid.New()

	mock.ExpectBegin()
	// The exact regex matters: a future refactor that drops FOR UPDATE
	// should fail this test rather than silently regress concurrency.
	mock.ExpectQuery(`SELECT state FROM session_issue_link_provider_state[\s\S]+WHERE link_id = @link_id AND org_id = @org_id AND provider = @provider[\s\S]+FOR UPDATE`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow([]byte(`{}`)))
	mock.ExpectExec("INSERT INTO session_issue_link_provider_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	require.NoError(t, store.Merge(context.Background(), orgID, linkID, LinearProviderState{LastSkippedReason: "private_session"}), "Merge should lock with FOR UPDATE and serialize concurrent merges")
	require.NoError(t, mock.ExpectationsWereMet(), "Merge must issue the locked SELECT, the upsert, and a commit")
}

// TestLinearProviderStateStore_MergeRollsBackOnUpsertFailure pins the
// rollback behavior when the locked write fails. Without the rollback,
// the row-level lock would leak until the connection died.
func TestLinearProviderStateStore_MergeRollsBackOnUpsertFailure(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewLinearProviderStateStore(mock)
	orgID := uuid.New()
	linkID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT state FROM session_issue_link_provider_state[\s\S]+FOR UPDATE`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow([]byte(`{}`)))
	mock.ExpectExec("INSERT INTO session_issue_link_provider_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("db unavailable"))
	mock.ExpectRollback()

	err = store.Merge(context.Background(), orgID, linkID, LinearProviderState{LastWriteOutcome: "merged"})
	require.Error(t, err, "Merge should surface upsert errors")
	require.Contains(t, err.Error(), "upsert linear provider state for merge", "Merge should wrap upsert errors with context")
	require.NoError(t, mock.ExpectationsWereMet(), "the deferred rollback must fire on upsert failure")
}

// TestLinearProviderStateStore_MergeRejectsCrossOrg pins the
// defense-in-depth check inside the transactional Merge: a row keyed to
// a different org_id must not be silently overwritten when the link_id
// happens to collide. RowsAffected=0 (the WHERE clause filtered out the
// cross-org update) surfaces as an explicit error rather than a silent
// successful no-op.
func TestLinearProviderStateStore_MergeRejectsCrossOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewLinearProviderStateStore(mock)
	orgID := uuid.New()
	linkID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT state FROM session_issue_link_provider_state[\s\S]+FOR UPDATE`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow([]byte(`{}`)))
	mock.ExpectExec("INSERT INTO session_issue_link_provider_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectRollback()

	err = store.Merge(context.Background(), orgID, linkID, LinearProviderState{LastWriteOutcome: "merged"})
	require.Error(t, err, "Merge must reject a cross-org link_id collision")
	require.Contains(t, err.Error(), "no row written", "Merge error must surface zero-rows-affected so callers don't treat a cross-org rejection (or concurrent delete) as success")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestLinearProviderStateStore_UpsertRejectsCrossOrgCollision pins the
// defense-in-depth check on the ON CONFLICT branch: a row keyed to a
// different org_id must not be silently overwritten or silently dropped.
// Zero rows affected (the WHERE clause filtered out the cross-org update)
// surfaces as an explicit error so a caller bug doesn't masquerade as a
// successful write.
func TestLinearProviderStateStore_UpsertRejectsCrossOrgCollision(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectExec("INSERT INTO session_issue_link_provider_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))

	err = NewLinearProviderStateStore(mock).Upsert(context.Background(), uuid.New(), uuid.New(), LinearProviderState{Identifier: "ACS-1"})
	require.Error(t, err, "Upsert must reject cross-org link_id collision")
	require.Contains(t, err.Error(), "no row written", "Upsert error must surface zero-rows-affected so callers don't treat a cross-org rejection (or concurrent delete) as success")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestLinearProviderStateStore_UpsertWrapsExecError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectExec("INSERT INTO session_issue_link_provider_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("db unavailable"))

	err = NewLinearProviderStateStore(mock).Upsert(context.Background(), uuid.New(), uuid.New(), LinearProviderState{Identifier: "ACS-1"})
	require.Error(t, err, "Upsert should return exec errors")
	require.Contains(t, err.Error(), "upsert linear provider state", "Upsert should wrap exec errors")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestMergeLinearProviderState_PreservesStickyBools is the regression test
// for the Merge bool-clobbering bug. A partial patch (e.g. recording a skip
// reason) must NOT reset CoexistsWithGitHubIntegration once it has been
// set — without this, the suppress-on-coexistence guard is one update away
// from being lost on every state event.
func TestMergeLinearProviderState_PreservesStickyBools(t *testing.T) {
	t.Parallel()

	current := LinearProviderState{
		AttachmentID:                  "attach-1",
		CommentID:                     "comment-1",
		CoexistsWithGitHubIntegration: BoolPtr(true),
	}
	patch := LinearProviderState{LastSkippedReason: "private_session"}

	merged := MergeLinearProviderState(current, patch)

	if merged.CoexistsWithGitHubIntegration == nil || !*merged.CoexistsWithGitHubIntegration {
		t.Fatalf("Merge must preserve CoexistsWithGitHubIntegration=true after a partial patch, got %+v", merged.CoexistsWithGitHubIntegration)
	}
	if merged.LastSkippedReason != "private_session" {
		t.Fatalf("Merge must apply the patch's LastSkippedReason, got %q", merged.LastSkippedReason)
	}
	if merged.AttachmentID != "attach-1" || merged.CommentID != "comment-1" {
		t.Fatalf("Merge must preserve unrelated string fields, got %+v", merged)
	}
}

// TestMergeLinearProviderState_AllowsExplicitBoolUpdate verifies the
// pointer semantics work in the other direction too: a non-nil patch
// overwrites the stored value. Without this, the coexistence detector
// couldn't promote false → true on first observation.
func TestMergeLinearProviderState_AllowsExplicitBoolUpdate(t *testing.T) {
	t.Parallel()

	current := LinearProviderState{}
	patch := LinearProviderState{CoexistsWithGitHubIntegration: BoolPtr(true)}

	merged := MergeLinearProviderState(current, patch)

	if merged.CoexistsWithGitHubIntegration == nil || !*merged.CoexistsWithGitHubIntegration {
		t.Fatalf("explicit Merge(true) must promote the bool, got %+v", merged.CoexistsWithGitHubIntegration)
	}
}

// TestMergeLinearProviderState_AllowsExplicitFalse verifies that a patch
// can also clear a sticky bool — important for the "remove or repair"
// affordance that needs to flip IssueRepoStale back off.
func TestMergeLinearProviderState_AllowsExplicitFalse(t *testing.T) {
	t.Parallel()

	current := LinearProviderState{IssueRepoStale: BoolPtr(true)}
	patch := LinearProviderState{IssueRepoStale: BoolPtr(false)}

	merged := MergeLinearProviderState(current, patch)

	if merged.IssueRepoStale == nil {
		t.Fatalf("Merge with explicit false must set the pointer, got nil")
	}
	if *merged.IssueRepoStale {
		t.Fatalf("Merge with explicit false must clear the flag, got true")
	}
}

// TestMergeLinearProviderState_AllowsCoexistenceFalseClear pins the same
// nil-vs-explicit-false semantics on CoexistsWithGitHubIntegration. The
// flag is sticky on `true` (suppression guard for Linear's GitHub
// integration), but an admin-side "I removed Linear's GitHub integration,
// re-enable our writes" path needs to clear it explicitly back to false.
// Without this test, a future refactor that switches to a bare-bool field
// could regress the clear path silently.
func TestMergeLinearProviderState_AllowsCoexistenceFalseClear(t *testing.T) {
	t.Parallel()

	current := LinearProviderState{CoexistsWithGitHubIntegration: BoolPtr(true)}
	patch := LinearProviderState{CoexistsWithGitHubIntegration: BoolPtr(false)}

	merged := MergeLinearProviderState(current, patch)

	if merged.CoexistsWithGitHubIntegration == nil {
		t.Fatalf("explicit false must set the pointer, got nil")
	}
	if *merged.CoexistsWithGitHubIntegration {
		t.Fatalf("explicit false must clear coexistence, got true")
	}
}

// TestMergeLinearProviderState_EmptyStringDoesNotClear locks the design
// invariant that empty patch strings are no-ops, not clears.
func TestMergeLinearProviderState_EmptyStringDoesNotClear(t *testing.T) {
	t.Parallel()

	current := LinearProviderState{
		AttachmentID:    "attach-1",
		CommentID:       "comment-1",
		LinkAuditReason: "linear_null_repo_carveout",
	}
	patch := LinearProviderState{LastWriteOutcome: "merged"}

	merged := MergeLinearProviderState(current, patch)

	if merged.AttachmentID != "attach-1" {
		t.Errorf("AttachmentID must remain when patch leaves it empty")
	}
	if merged.CommentID != "comment-1" {
		t.Errorf("CommentID must remain when patch leaves it empty")
	}
	if merged.LinkAuditReason != "linear_null_repo_carveout" {
		t.Errorf("LinkAuditReason must remain when patch leaves it empty")
	}
	if merged.LastWriteOutcome != "merged" {
		t.Errorf("LastWriteOutcome must apply from patch")
	}
}

func TestMergeLinearProviderState_AppliesEveryPatchField(t *testing.T) {
	t.Parallel()

	patch := LinearProviderState{
		Identifier:                    "ACS-123",
		AttachmentID:                  "attachment-1",
		AttachmentURL:                 "https://linear.app/attachment/1",
		CommentID:                     "comment-1",
		PriorStateID:                  "state-prior",
		LastKnownStateName:            "In Review",
		LastKnownStateType:            "started",
		TeamID:                        "team-1",
		WorkspaceSlug:                 "acme",
		LinkAuditReason:               "linear_null_repo_carveout",
		LastWriteOutcome:              "pr_open",
		LastSkippedReason:             "private_session",
		CoexistsWithGitHubIntegration: BoolPtr(true),
		IssueRepoStale:                BoolPtr(true),
		PrimarySnapshot:               []byte(`{"identifier":"ACS-123"}`),
	}

	merged := MergeLinearProviderState(LinearProviderState{}, patch)

	require.Equal(t, patch, merged, "MergeLinearProviderState should apply every non-empty patch field")
}

func TestBoolPtr(t *testing.T) {
	t.Parallel()

	got := BoolPtr(false)
	require.NotNil(t, got, "BoolPtr should return a non-nil pointer")
	require.False(t, *got, "BoolPtr should preserve false values")
}

// TestCoexistsCheckIsStale_AsymmetricTTL locks the design intent that the
// "Linear's GitHub integration is present" cache ages out faster than the
// "absent" cache: a cached `true` *suppresses* our state moves, so a sticky
// 24h would silently keep blocking transitions for the rest of the day after
// an operator removed the integration. The 1h active TTL bounds that
// surprise window without churning the API on every milestone.
func TestCoexistsCheckIsStale_AsymmetricTTL(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		cached    *bool
		checkedAt *time.Time
		want      bool
	}{
		{
			name:      "nil checkedAt is always stale",
			cached:    BoolPtr(true),
			checkedAt: nil,
			want:      true,
		},
		{
			name:      "cached=true at 30m is fresh",
			cached:    BoolPtr(true),
			checkedAt: TimePtr(now.Add(-30 * time.Minute)),
			want:      false,
		},
		{
			name:      "cached=true at 90m is stale (active TTL is 1h)",
			cached:    BoolPtr(true),
			checkedAt: TimePtr(now.Add(-90 * time.Minute)),
			want:      true,
		},
		{
			name:      "cached=false at 90m is fresh (long TTL applies)",
			cached:    BoolPtr(false),
			checkedAt: TimePtr(now.Add(-90 * time.Minute)),
			want:      false,
		},
		{
			name:      "cached=false at 25h is stale (long TTL is 24h)",
			cached:    BoolPtr(false),
			checkedAt: TimePtr(now.Add(-25 * time.Hour)),
			want:      true,
		},
		{
			name:      "nil cached uses long TTL (treated as 'no observation yet')",
			cached:    nil,
			checkedAt: TimePtr(now.Add(-90 * time.Minute)),
			want:      false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := CoexistsCheckIsStale(tt.cached, tt.checkedAt, now)
			require.Equal(t, tt.want, got, "CoexistsCheckIsStale should age cached=true out faster than cached=false")
		})
	}
}
