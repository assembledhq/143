package thread

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type sendChangesetReaderFunc func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionChangeset, error)

func (f sendChangesetReaderFunc) GetByID(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) (models.SessionChangeset, error) {
	return f(ctx, orgID, sessionID, changesetID)
}

func TestLegacyChangesetPolicyValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		policy LegacyChangesetPolicy
		valid  bool
	}{
		{name: "unchanged direct caller", policy: LegacyChangesetUnchanged, valid: true},
		{name: "configured HTTP caller", policy: LegacyChangesetRequireMaterialized, valid: true},
		{name: "unconfigured HTTP caller", policy: LegacyChangesetUnconfigured, valid: true},
		{name: "invalid policy", policy: "provider_start"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.valid, tt.policy.Validate() == nil, "compatibility policy must remain finite and distinct from provider authority")
		})
	}
}

func TestSendMessageLegacyTargetCompatibilityPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		policy        LegacyChangesetPolicy
		reader        string
		edit          func(*models.SessionChangeset)
		readErr       error
		withoutTarget bool
		zeroTarget    bool
		want          error
		reads         int
	}{
		{name: "materialized HTTP target", policy: LegacyChangesetRequireMaterialized, reads: 1},
		{name: "unconfigured HTTP adapter", policy: LegacyChangesetUnconfigured, want: ErrSendChangesetsNotConfigured},
		{name: "requested policy missing reader", policy: LegacyChangesetRequireMaterialized, reader: "nil", want: ErrSendChangesetsNotConfigured},
		{name: "requested policy typed nil reader", policy: LegacyChangesetRequireMaterialized, reader: "typed_nil", want: ErrSendChangesetsNotConfigured},
		{name: "read failure has safe not-found outcome", policy: LegacyChangesetRequireMaterialized, readErr: errors.New("synthetic-private-reader-detail"), reads: 1, want: ErrSendChangesetNotFound},
		{name: "foreign tenant refuses", policy: LegacyChangesetRequireMaterialized, edit: func(v *models.SessionChangeset) { v.OrgID = uuid.New() }, reads: 1, want: ErrSendChangesetNotFound},
		{name: "different parent refuses", policy: LegacyChangesetRequireMaterialized, edit: func(v *models.SessionChangeset) { v.SessionID = uuid.New() }, reads: 1, want: ErrSendChangesetNotFound},
		{name: "different exact target refuses", policy: LegacyChangesetRequireMaterialized, edit: func(v *models.SessionChangeset) { v.ID = uuid.New() }, reads: 1, want: ErrSendChangesetNotFound},
		{name: "nil materialization path", policy: LegacyChangesetRequireMaterialized, edit: func(v *models.SessionChangeset) { v.WorktreePath = nil }, reads: 1, want: ErrSendChangesetNotMaterialized},
		{name: "blank materialization path", policy: LegacyChangesetRequireMaterialized, edit: func(v *models.SessionChangeset) { path := " \t "; v.WorktreePath = &path }, reads: 1, want: ErrSendChangesetNotMaterialized},
		{name: "default direct target caller never gains HTTP check", policy: LegacyChangesetUnchanged},
		{name: "no target has no target precheck", policy: LegacyChangesetRequireMaterialized, withoutTarget: true},
		{name: "invalid requested policy fails closed", policy: "unsupported", want: ErrSendChangesetsNotConfigured},
		{name: "configured HTTP zero target remains not found", policy: LegacyChangesetRequireMaterialized, zeroTarget: true, want: ErrSendChangesetNotFound},
		{name: "unconfigured HTTP zero target retains config precedence", policy: LegacyChangesetUnconfigured, zeroTarget: true, want: ErrSendChangesetsNotConfigured},
		{name: "default direct zero target preserves old shape", policy: LegacyChangesetUnchanged, zeroTarget: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service, deps := newTestService(t)
			changesetID := uuid.New()
			if tt.zeroTarget {
				changesetID = uuid.Nil
			}
			input := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), ChangesetID: &changesetID, LegacyChangesetPolicy: tt.policy, ClientMessageID: "synthetic-key", Message: "continue"}
			if tt.withoutTarget {
				input.ChangesetID = nil
			}
			runner := service.send.Transactions.(*legacyGuardFixtureRunner)
			reads := 0
			service.send.Changesets = sendChangesetReaderFunc(func(_ context.Context, orgID, sessionID, targetID uuid.UUID) (models.SessionChangeset, error) {
				reads++
				require.Equal(t, []string{"begin", "commit"}, runner.events, "target compatibility read must follow the known entry router unit and precede every legacy business mutation")
				require.True(t, runner.settled, "compatibility read must occur outside the completed entry transaction")
				require.Equal(t, []uuid.UUID{input.OrgID, input.SessionID, changesetID}, []uuid.UUID{orgID, sessionID, targetID}, "read must retain exact tenant, parent and original parsed target")
				path := "synthetic-materialized-tree"
				row := models.SessionChangeset{ID: targetID, OrgID: orgID, SessionID: sessionID, WorktreePath: &path}
				if tt.edit != nil {
					tt.edit(&row)
				}
				return row, tt.readErr
			})
			if tt.reader == "nil" {
				service.send.Changesets = nil
			}
			if tt.reader == "typed_nil" {
				var reader *dbChangesetReaderFixture
				service.send.Changesets = reader
			}
			if tt.want != nil {
				deps.sessionStore.codeReviewErr = errors.New("legacy owner business precheck must not precede requested target policy")
			}
			deps.threadStore.claimIdleFn = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
				return models.SessionThread{ID: input.ThreadID, OrgID: input.OrgID, SessionID: input.SessionID, Status: models.ThreadStatusRunning}, nil
			}
			deps.messageStore.createFn = func(_ context.Context, message *models.SessionMessage) error { message.ID = 42; return nil }
			result, err := service.SendMessage(context.Background(), input)
			if tt.want != nil {
				require.ErrorIs(t, err, tt.want, "legacy target compatibility failure must preserve its exact safe public category")
				require.Nil(t, result, "target refusal cannot fabricate an accepted message")
				require.Equal(t, []string{"begin", "commit"}, runner.events, "target failure must precede original legacy business units")
			} else {
				require.NoError(t, err, "legacy target success and unchanged direct callers should retain old message admission")
				require.NotNil(t, result, "successful old path must retain the existing message response")
				require.Nil(t, result.Admission, "legacy target preflight does not create a canonical receipt")
				require.Equal(t, int64(42), result.Message.ID, "legacy send should execute its original message mutation")
			}
			require.Equal(t, tt.reads, reads, "only configured HTTP legacy targeting may invoke the scoped reader")
		})
	}
}

type dbChangesetReaderFixture struct{}

func (*dbChangesetReaderFixture) GetByID(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionChangeset, error) {
	return models.SessionChangeset{}, errors.New("typed nil reader must not be invoked")
}

func TestLegacyTargetPolicyIsNotSemanticIntent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		policy LegacyChangesetPolicy
	}{
		{name: "unchanged", policy: LegacyChangesetUnchanged},
		{name: "HTTP configured", policy: LegacyChangesetRequireMaterialized},
		{name: "HTTP unconfigured", policy: LegacyChangesetUnconfigured},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			targetID := uuid.New()
			input := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), ChangesetID: &targetID, ClientMessageID: "synthetic-key", Message: "continue"}
			base, _, err := prepareSendMessage(input)
			require.NoError(t, err, "prepare original exact semantic identity")
			input.LegacyChangesetPolicy = tt.policy
			changed, _, err := prepareSendMessage(input)
			require.NoError(t, err, "adapter compatibility policy must not enter the canonical command shape")
			require.Equal(t, base.Ingress, changed.Ingress, "changing only server legacy target policy must preserve exact ingress identity and semantic digest")
		})
	}
}
