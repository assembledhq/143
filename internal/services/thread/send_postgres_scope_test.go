package thread

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// The comparison scope shares the caller's full-schema database, not a second
// fixture database that would make an accidentally unscoped lookup miss anyway.
func newSendPostgresComparisonScope(t *testing.T, f sendPostgresFixture, foreignOrg bool) sendPostgresFixture {
	t.Helper()
	other := sendPostgresFixture{pool: f.pool, org: f.org, user: f.user}
	if foreignOrg {
		other.org, other.user = uuid.New(), uuid.New()
	}
	require.NoError(t, db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
		if foreignOrg {
			if _, err := tx.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic foreign service admission tenant')`, other.org); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO users(id,org_id,email,name,role) VALUES($1,$2,$3,'Synthetic foreign service admission actor','member')`, other.user, other.org, other.user.String()+"@example.invalid"); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO organization_memberships(user_id,org_id,role) VALUES($1,$2,'member') ON CONFLICT(user_id,org_id) DO UPDATE SET role='member'`, other.user, other.org); err != nil {
				return err
			}
		}
		session := models.Session{OrgID: other.org, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
		if err := db.NewSessionStore(tx).CreateInTx(ctx, tx, &session); err != nil {
			return err
		}
		if session.PrimaryThreadID == nil {
			return errors.New("real comparison session creation returned no primary lane")
		}
		other.session, other.lanes = session.ID, []uuid.UUID{*session.PrimaryThreadID}
		var err error
		other.changeset, err = db.NewSessionStore(tx).GetPrimaryChangesetID(ctx, other.org, other.session)
		return err
	}), "install an independently addressable comparison tenant/session/lane through real stores in the same database")
	return other
}

func TestThreadSendActiveScopeRejectionPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, scope string
		retainedKey bool
	}{
		{name: "fresh foreign caller organization", scope: "organization"},
		{name: "fresh same-tenant other-session lane", scope: "session"},
		{name: "fresh missing exact lane", scope: "missing"},
		{name: "retained client key cannot disclose acceptance to a foreign caller organization", scope: "organization", retainedKey: true},
		{name: "retained client key cannot substitute an other-session lane", scope: "session", retainedKey: true},
		{name: "retained client key cannot substitute a missing lane", scope: "missing", retainedKey: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, true, 1)
			other := newSendPostgresComparisonScope(t, f, tt.scope == "organization")
			input := f.input(0, "synthetic-active-scope")
			if tt.retainedKey {
				owner, _ := f.service(t, true)
				accepted, err := owner.SendMessage(context.Background(), input)
				require.NoError(t, err, "establish the original complete active service receipt before testing an invalid scope tuple with the same client key")
				require.NotNil(t, accepted, "the valid exact scope must return its committed result")
				require.NotNil(t, accepted.Admission, "the valid exact scope must reach canonical active admission")
				require.Equal(t, db.MessageAdmissionNewJob, accepted.Admission.Disposition(), "the retained identity must belong to one actual bound ordinary input, not a synthetic dispatch")
			}
			before, otherBefore := sendPostgresReadGraph(t, f), sendPostgresReadGraph(t, other)
			service, effects := f.service(t, true)
			switch tt.scope {
			case "organization":
				input.OrgID, input.UserID = other.org, &other.user
			case "session":
				input.ThreadID = other.lanes[0]
			case "missing":
				input.ThreadID = uuid.New()
			default:
				t.Fatalf("unrecognized synthetic scope case: %s", tt.scope)
			}
			result, err := service.SendMessage(context.Background(), input)
			require.ErrorIs(t, err, pgx.ErrNoRows, "the actual service must reject an absent exact tenant/session/lane tuple without inferring the primary lane or disclosing another scope's receipt")
			require.NotErrorIs(t, err, ErrCommandOutcomeUnknown, "a confirmed rollback of the scope refusal is not ambiguous input acceptance")
			require.Nil(t, result, "a mismatched scope must expose no message, inbox, admission receipt or historical accepted contents")
			require.Equal(t, before, sendPostgresReadGraph(t, f), "scope refusal must leave the original active parent/lane input graph, claims, counters and projections unchanged")
			require.Equal(t, otherBefore, sendPostgresReadGraph(t, other), "scope refusal must leave the independently known comparison parent/lane input graph, claims, counters and projections unchanged")
			require.Equal(t, []int{0, 0, 0, 0, 0, 0}, []int{len(effects.lifecycle), len(effects.jobs), effects.claims, effects.pending, effects.threadStatus, effects.parentStatus}, "neither fresh scope refusal nor a retained-key scope mismatch may invoke any lifecycle, job or legacy publisher")
		})
	}
}
