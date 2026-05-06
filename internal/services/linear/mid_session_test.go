package linear

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestResolveAndLinkMidSession(t *testing.T) {
	t.Parallel()

	t.Run("disabled integration is no-op", func(t *testing.T) {
		t.Parallel()

		svc := &Service{}
		err := svc.ResolveAndLinkMidSession(context.Background(), MidSessionInput{
			OrgID:       uuid.New(),
			SessionID:   uuid.New(),
			MessageBody: "https://linear.app/acme/issue/ACS-1",
		})
		require.NoError(t, err, "ResolveAndLinkMidSession should silently no-op when Linear is disabled")
	})

	t.Run("empty message is no-op", func(t *testing.T) {
		t.Parallel()

		svc := &Service{integrations: fakeIntegrationReader{}}
		err := svc.ResolveAndLinkMidSession(context.Background(), MidSessionInput{
			OrgID:     uuid.New(),
			SessionID: uuid.New(),
		})
		require.NoError(t, err, "empty body should not even reach detection")
	})

	t.Run("no refs is no-op", func(t *testing.T) {
		t.Parallel()

		svc := &Service{integrations: fakeIntegrationReader{}}
		err := svc.ResolveAndLinkMidSession(context.Background(), MidSessionInput{
			OrgID:       uuid.New(),
			SessionID:   uuid.New(),
			MessageBody: "fix the build please",
		})
		require.NoError(t, err, "messages without refs should silently no-op")
	})

	t.Run("allowlist load failure returns wrapped error", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		svc := newCreatePathService(mock, createPathClient{}, nil)
		svc.teamKeys = db.NewLinearTeamKeyStore(mock)
		mock.ExpectQuery(`SELECT k\.org_id, k\.integration_id, k\.workspace_id, k\.team_id, k\.team_key, k\.team_name, k\.refreshed_at`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnError(errors.New("db unavailable"))

		err = svc.ResolveAndLinkMidSession(context.Background(), MidSessionInput{
			OrgID:       uuid.New(),
			SessionID:   uuid.New(),
			MessageBody: "ACS-123 should fix it",
		})
		require.Error(t, err, "ResolveAndLinkMidSession should surface allowlist errors so the caller can log them")
		require.Contains(t, err.Error(), "load team key allowlist", "error should be wrapped with context")
	})

	t.Run("URL ref is enqueued as mid-session work", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		var enqueuedJobType string
		var enqueuedDedupe string
		var enqueuedPayload map[string]any
		svc := newCreatePathService(mock, createPathClient{}, nil)
		svc.SetJobEnqueuer(func(_ context.Context, gotOrgID uuid.UUID, jobType string, payload any, dedupeKey *string) error {
			require.Equal(t, orgID, gotOrgID, "enqueue should preserve org scope")
			require.NotNil(t, dedupeKey, "enqueue should use a dedupe key")
			enqueuedJobType = jobType
			enqueuedDedupe = *dedupeKey
			body, marshalErr := json.Marshal(payload)
			require.NoError(t, marshalErr)
			require.NoError(t, json.Unmarshal(body, &enqueuedPayload))
			return nil
		})

		err = svc.ResolveAndLinkMidSession(context.Background(), MidSessionInput{
			OrgID:       orgID,
			SessionID:   sessionID,
			MessageBody: "follow-up: also see https://linear.app/acme/issue/ACS-7",
			UserID:      &userID,
		})
		require.NoError(t, err, "URL refs should enqueue async linking without error")
		require.Equal(t, "link_linear_issue_mid_session", enqueuedJobType, "mid-session linking should use the dedicated job type")
		require.Contains(t, enqueuedDedupe, sessionID.String(), "dedupe key should scope by session id")
		require.Equal(t, []any{"ACS-7"}, enqueuedPayload["identifiers"], "payload should carry the detected identifier")
		require.Equal(t, userID.String(), enqueuedPayload["user_id"], "payload should attribute the link to the sender")
	})

	t.Run("bare identifier requires team-key allowlist", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		now := time.Now().UTC()
		orgID := uuid.New()
		sessionID := uuid.New()
		svc := newCreatePathService(mock, createPathClient{}, nil)
		svc.teamKeys = db.NewLinearTeamKeyStore(mock)
		// Allowlist returns a single ACS prefix; the FOO-99 bare id below
		// should be ignored because its prefix isn't allowlisted.
		mock.ExpectQuery(`SELECT k\.org_id, k\.integration_id, k\.workspace_id, k\.team_id, k\.team_key, k\.team_name, k\.refreshed_at`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"org_id", "integration_id", "workspace_id", "team_id", "team_key", "team_name", "refreshed_at"}).
				AddRow(orgID, uuid.New(), "ws-1", "team-1", "ACS", "Core", now))

		var enqueueCalls int
		svc.SetJobEnqueuer(func(context.Context, uuid.UUID, string, any, *string) error {
			enqueueCalls++
			return nil
		})

		err = svc.ResolveAndLinkMidSession(context.Background(), MidSessionInput{
			OrgID:       orgID,
			SessionID:   sessionID,
			MessageBody: "found duplicate of FOO-99 — see ACS-42 for context",
		})
		require.NoError(t, err)
		require.Equal(t, 1, enqueueCalls, "only the allowlisted bare-identifier should produce an enqueue")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("missing job enqueuer surfaces a clear error", func(t *testing.T) {
		t.Parallel()

		svc := newCreatePathService(nil, createPathClient{}, nil)
		err := svc.ResolveAndLinkMidSession(context.Background(), MidSessionInput{
			OrgID:       uuid.New(),
			SessionID:   uuid.New(),
			MessageBody: "see https://linear.app/acme/issue/ACS-1",
		})
		require.Error(t, err, "missing enqueuer should be surfaced so SendMessage can log it")
	})
}
