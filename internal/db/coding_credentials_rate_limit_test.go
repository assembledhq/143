package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestApplyRateLimitCheck(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                                              string
		personal, changedConfig, changedLimit, disabled, missing, limited bool
	}{
		{name: "clears org cooldown"},
		{name: "clears personal cooldown", personal: true},
		{name: "updates provider cooldown", limited: true},
		{name: "config changed", changedConfig: true},
		{name: "newer rate limit", changedLimit: true},
		{name: "disabled during check", disabled: true},
		{name: "wrong tenant or owner", missing: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, mock := newMockCodingCredentialStore(t)
			defer mock.Close()
			scope := models.Scope{OrgID: uuid.New()}
			if tt.personal {
				uid := uuid.New()
				scope.UserID = &uid
			}
			id := uuid.New()
			now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
			until := now.Add(time.Hour)
			message := "old limit"
			store.SetClock(func() time.Time { return now })
			row := codingCredentialSnapshotRow(t, store, scope, id, models.ProviderOpenAISubscription, models.CodingCredentialStatusActive)
			row[12], row[13], row[14] = &until, &now, &message
			checked := &models.DecryptedCodingCredential{ID: id, VersionID: row[1].(uuid.UUID), Provider: models.ProviderOpenAISubscription, RateLimitedUntil: &until, RateLimitedObservedAt: &now}
			if tt.changedConfig {
				row[1] = uuid.New()
			}
			if tt.changedLimit {
				later := now.Add(time.Second)
				row[13] = &later
			}
			if tt.disabled {
				row[10] = models.CodingCredentialStatusDisabled
			}
			store.health.shed(id)
			mock.ExpectBegin()
			pattern := `SELECT .*FROM coding_credentials cc.*WHERE cc.id = @id AND cc.org_id = @org_id AND cc.user_id IS NULL AND cc.active = true FOR UPDATE`
			args := pgx.NamedArgs{"id": id, "org_id": scope.OrgID}
			if tt.personal {
				pattern = `SELECT .*FROM coding_credentials cc.*WHERE cc.id = @id AND cc.org_id = @org_id AND cc.user_id = @user_id AND cc.active = true FOR UPDATE`
				args["user_id"] = *scope.UserID
			}
			rows := pgxmock.NewRows(codingCredentialSnapshotColumns)
			if !tt.missing {
				rows.AddRow(row...)
			}
			mock.ExpectQuery(pattern).WithArgs(args).WillReturnRows(rows)
			var limit *models.CodingCredentialRateLimit
			if tt.limited {
				limit = &models.CodingCredentialRateLimit{Until: until.Add(time.Hour), Message: "Provider reports a rate limit"}
			}
			stale := tt.changedConfig || tt.changedLimit || tt.disabled
			if stale || tt.missing {
				mock.ExpectRollback()
			} else {
				mock.ExpectExec(`UPDATE coding_credential_runtime_state\s+SET active = false`).WithArgs(pgx.NamedArgs{"credential_id": id}).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				var wantUntil, wantObserved *time.Time
				var wantMessage *string
				if limit != nil {
					wantUntil = &limit.Until
					wantObserved = &now
					wantMessage = &limit.Message
				}
				mock.ExpectExec(`INSERT INTO coding_credential_runtime_state`).WithArgs(pgx.NamedArgs{"credential_id": id, "org_id": scope.OrgID, "user_id": scope.UserID, "status": "active", "last_verified_at": (*time.Time)(nil), "rate_limited_until": wantUntil, "rate_limited_observed_at": wantObserved, "rate_limit_message": wantMessage}).WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectCommit()
			}
			err := store.ApplyRateLimitCheck(context.Background(), scope, checked, limit)
			if tt.missing {
				require.ErrorIs(t, err, ErrCodingCredentialNotFound, "scope mismatch must not mutate runtime")
			} else if stale {
				require.ErrorIs(t, err, ErrRateLimitCheckStale, "slow checks must preserve newer runtime state")
			} else {
				require.NoError(t, err, "confirmed check should update only cooldown metadata")
			}
			require.Equal(t, stale || tt.missing || tt.limited, store.health.isShed(id), "health cache must only clear after a committed successful check")
			require.NoError(t, mock.ExpectationsWereMet(), "all scoped runtime update expectations should be met")
		})
	}
}
