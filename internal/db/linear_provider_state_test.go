package db

import (
	"context"
	"errors"
	"testing"

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

	mock.ExpectQuery("SELECT state FROM session_issue_link_provider_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow([]byte(`{"identifier":"ACS-1","comment_id":"comment-1"}`)))
	mock.ExpectExec("INSERT INTO session_issue_link_provider_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	require.NoError(t, store.Merge(context.Background(), orgID, linkID, LinearProviderState{LastWriteOutcome: "merged"}), "Merge should read, merge, and upsert provider state")
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
