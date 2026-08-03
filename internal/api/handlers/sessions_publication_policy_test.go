package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func orgSettingsWithPublicationPolicy(t *testing.T, createPR, review bool) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(models.OrgSettings{
		SessionAutomation: models.SessionAutomationSettings{
			AutomaticFollowThrough: models.AutomaticFollowThroughOrgSettings{
				CreatePRWhenAgentReady: &createPR,
				ReviewBeforePR:         &review,
			},
		},
	})
	require.NoError(t, err, "test organization settings should encode")
	return raw
}

// The publication policy is advisory display data. A session whose initiator
// was deleted or moved organizations must still load, so a failed lookup falls
// back to organization policy instead of failing the whole session detail.
func TestSessionHandler_ResolveSessionPublicationPolicyDegradesOnInitiatorFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		initiatorOrgID func(orgID uuid.UUID) uuid.UUID
		initiatorErr   bool
		wantCreatePR   bool
		wantSource     models.PublicationPolicySource
	}{
		{
			name:           "initiator personal preference applies",
			initiatorOrgID: func(orgID uuid.UUID) uuid.UUID { return orgID },
			wantCreatePR:   false,
			wantSource:     models.PublicationPolicySourcePersonal,
		},
		{
			name:         "missing initiator falls back to organization policy",
			initiatorErr: true,
			wantCreatePR: true,
			wantSource:   models.PublicationPolicySourceOrganization,
		},
		{
			name:           "cross-organization initiator falls back to organization policy",
			initiatorOrgID: func(uuid.UUID) uuid.UUID { return uuid.New() },
			wantCreatePR:   true,
			wantSource:     models.PublicationPolicySourceOrganization,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create the database mock")
			t.Cleanup(mock.Close)

			orgID, sessionID, userID := uuid.New(), uuid.New(), uuid.New()
			now := time.Now()
			mock.ExpectQuery("SELECT id, name, settings, created_at, updated_at").
				WithArgs(pgx.NamedArgs{"id": orgID}).
				WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
					AddRow(orgID, "acme", orgSettingsWithPublicationPolicy(t, true, true), now, now))

			userQuery := mock.ExpectQuery("FROM users").WithArgs(pgx.NamedArgs{"id": userID})
			if tt.initiatorErr {
				userQuery.WillReturnError(pgx.ErrNoRows)
			} else {
				personal, marshalErr := json.Marshal(models.UserSettings{
					AutomaticPRFollowThrough: &models.AutomaticPRFollowThroughSettings{
						CreatePRWhenAgentReady: models.AutomaticFollowThroughPreferenceOff,
					},
				})
				require.NoError(t, marshalErr, "test user settings should encode")
				userQuery.WillReturnRows(pgxmock.NewRows([]string{
					"id", "org_id", "email", "name", "role", "github_id",
					"github_login", "avatar_url", "google_id", "email_verified_at",
					"created_at", "settings",
				}).AddRow(
					userID, tt.initiatorOrgID(orgID), "dev@acme.test", "Dev", "member",
					(*int64)(nil), (*string)(nil), (*string)(nil), (*string)(nil),
					(*time.Time)(nil), now, personal,
				))
			}

			handler := &SessionHandler{
				orgStore:  db.NewOrganizationStore(mock),
				userStore: db.NewUserStore(mock),
				logger:    zerolog.Nop(),
			}
			run := &models.Session{ID: sessionID, OrgID: orgID, TriggeredByUserID: &userID}

			policy := handler.resolveSessionPublicationPolicy(context.Background(), orgID, run)

			require.NotNil(t, policy, "an initiator lookup problem must not drop the whole policy block")
			require.Equal(t, tt.wantCreatePR, policy.CreatePRWhenAgentReady, "policy should reflect the resolvable settings")
			require.Equal(t, tt.wantSource, policy.CreatePRSource, "policy should attribute the value it actually used")
			require.NoError(t, mock.ExpectationsWereMet(), "policy resolution should issue the expected tenant-scoped queries")
		})
	}
}

// Runtime kill switches park work without changing the customer's saved
// policy. The API exposes both facts so the workflow can explain the pause.
func TestSessionHandler_ResolveSessionPublicationPolicySeparatesPolicyFromReviewExecution(t *testing.T) {
	t.Parallel()

	for _, reviewEnabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "review enabled", false: "review disabled"}[reviewEnabled], func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create the database mock")
			t.Cleanup(mock.Close)

			orgID := uuid.New()
			now := time.Now()
			mock.ExpectQuery("SELECT id, name, settings, created_at, updated_at").
				WithArgs(pgx.NamedArgs{"id": orgID}).
				WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
					AddRow(orgID, "acme", orgSettingsWithPublicationPolicy(t, true, true), now, now))

			handler := &SessionHandler{
				orgStore:           db.NewOrganizationStore(mock),
				logger:             zerolog.Nop(),
				prePRReviewEnabled: reviewEnabled,
			}

			policy := handler.resolveSessionPublicationPolicy(context.Background(), orgID,
				&models.Session{ID: uuid.New(), OrgID: orgID})

			require.NotNil(t, policy, "organization policy alone should still resolve")
			require.True(t, policy.ReviewBeforePR, "the runtime switch must not rewrite the stored review policy")
			require.Equal(t, reviewEnabled, policy.ReviewExecutionEnabled,
				"the API should expose whether the configured review can currently execute")
			require.True(t, policy.AgentPublicationExecutionEnabled,
				"agent publication should default to enabled when no coordinator gate is installed")
		})
	}
}
