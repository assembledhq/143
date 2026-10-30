package db

import (
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestCaptureThreadMessageFenceInTx(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		cause error
	}{
		{name: "exact raw target epochs are captured without eligibility"},
		{name: "deleted parent", cause: pgx.ErrNoRows},
		{name: "archived parent", cause: pgx.ErrNoRows},
		{name: "archived explicit lane", cause: pgx.ErrNoRows},
		{name: "parent stamp changed", cause: ErrLifecycleGuardProvenanceRequired},
		{name: "target stamp changed", cause: ErrLifecycleGuardProvenanceRequired},
		{name: "sibling stamp changed", cause: ErrLifecycleGuardProvenanceRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			snapshot := activeLifecycleModeSnapshot()
			expectLifecycleModeRead(mock, snapshot)
			f := newLifecycleScopeMockFixture()
			f.orgID, f.sessionID = snapshot.orgID, snapshot.sessionID
			f.session.id, f.session.orgID = f.sessionID, f.orgID
			f.first.sessionID, f.first.orgID = f.sessionID, f.orgID
			f.second.sessionID, f.second.orgID = f.sessionID, f.orgID
			f.session.epochs.LifecycleVersion = 1
			f.first.epochs.LifecycleVersion, f.second.epochs.LifecycleVersion = 1, 1
			f.session.epochs.AdmissionEpoch = 41
			// Capturing a fence does not answer this request or make completed
			// siblings eligible. The primitive still owns ordinary policy.
			f.first.status, f.second.status = models.ThreadStatusAwaitingInput, models.ThreadStatusCompleted
			at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			switch tt.name {
			case "deleted parent":
				f.session.deletedAt = &at
			case "archived parent":
				f.session.archivedAt = &at
			case "archived explicit lane":
				f.first.archivedAt = &at
			case "parent stamp changed":
				f.session.epochs.LifecycleVersion = 0
			case "target stamp changed":
				f.first.epochs.LifecycleVersion = 0
			case "sibling stamp changed":
				f.second.epochs.LifecycleVersion = 0
			}
			expectThreadSendScopeParent(mock, f)
			expectThreadSendScopeKeys(mock, f, f.first.id, f.second.id)
			mock.ExpectQuery(lifecycleThreadScopeLockQuery).
				WithArgs(f.orgID, f.sessionID, []uuid.UUID{f.first.id, f.second.id}).
				WillReturnRows(lifecycleScopeThreadRows(f.first, f.second)).RowsWillBeClosed()
			actual, err := CaptureThreadMessageFenceInTx(ctx, mock, f.orgID, f.sessionID, f.first.id)
			if tt.cause != nil {
				require.ErrorIs(t, err, tt.cause, "hidden or inconsistent current scope must not become a newly captured admission expectation")
				require.Equal(t, AdmissionFence{}, actual, "failed scope validation must expose no usable captured fence")
			} else {
				expected, constructorErr := NewAdmissionFence(f.sessionID, &f.first.id, 41, &f.first.epochs.AdmissionEpoch)
				require.NoError(t, constructorErr, "synthetic expected scope must be a valid explicit fence")
				require.NoError(t, err, "capture may return exact epochs without performing current admission policy")
				require.Equal(t, expected, actual, "captured fence must retain the selected target rather than its sibling or a primary lane")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "fence capture must only resolve mode and lock/read canonical scope, never mutate, reserve, settle or notify")
		})
	}
}

func TestVerifyThreadMessageFenceDoesNotRecapture(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		edit  func(*lockedLifecycleScope, *AdmissionFence)
		cause error
	}{
		{name: "same scope is unchanged"},
		{name: "parent advanced", edit: func(scope *lockedLifecycleScope, _ *AdmissionFence) { scope.session.epochs.AdmissionEpoch++ }, cause: ErrMessageAdmissionConflict},
		{name: "target advanced", edit: func(scope *lockedLifecycleScope, _ *AdmissionFence) { scope.threads[0].epochs.AdmissionEpoch++ }, cause: ErrMessageAdmissionConflict},
		{name: "other parent", edit: func(scope *lockedLifecycleScope, _ *AdmissionFence) { scope.session.id = uuid.New() }, cause: ErrMessageAdmissionConflict},
		{name: "missing target", edit: func(scope *lockedLifecycleScope, _ *AdmissionFence) { scope.threads = scope.threads[1:] }, cause: pgx.ErrNoRows},
		{name: "sibling end does not change target fence", edit: func(scope *lockedLifecycleScope, _ *AdmissionFence) { scope.threads[1].epochs.AdmissionEpoch++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newLifecycleScopeMockFixture()
			scope := lockedLifecycleScope{session: f.session, threads: []lifecycleLockedThread{f.first, f.second}}
			expected, err := NewAdmissionFence(f.sessionID, &f.first.id, f.session.epochs.AdmissionEpoch, &f.first.epochs.AdmissionEpoch)
			require.NoError(t, err, "frozen command fixture must carry exact original epochs")
			if tt.edit != nil {
				tt.edit(&scope, &expected)
			}
			frozen := expected
			err = verifyThreadMessageFence(scope, expected)
			if tt.cause != nil {
				require.ErrorIs(t, err, tt.cause, "a changed current scope must reject rather than recapture the command expectation")
			} else {
				require.NoError(t, err, "only the command's own exact scope controls its fence comparison")
			}
			require.Equal(t, frozen, expected, "comparison must never modify the immutable command or adopt current epochs")
		})
	}
}
