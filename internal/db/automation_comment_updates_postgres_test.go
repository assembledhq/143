package db

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func commentUpdateRequest(sourceID uuid.UUID, key string) models.AutomationActionRequest {
	return models.AutomationActionRequest{
		AutomationActionKey: models.AutomationActionKey{OperationKey: "workflow", ActionKey: key},
		Kind:                models.AutomationActionCommentUpdate,
		PRNumber:            42,
		HeadSHA:             strings.Repeat("a", 40),
		Text:                "Replacement " + key,
		SourceActionID:      sourceID.String(),
	}
}

func commentUpdatePayload(t *testing.T, req models.AutomationActionRequest, target models.AutomationActionCommentTarget) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(struct {
		Request       models.AutomationActionRequest       `json:"request"`
		CommentTarget models.AutomationActionCommentTarget `json:"comment_target"`
	}{req, target})
	require.NoError(t, err, "encode the frozen update request and target")
	return payload
}

func TestAutomationCommentUpdatePostgresSourceBinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		receiptSQL string
		change     func(*models.AutomationActionScope, *models.AutomationActionRequest)
	}{
		{name: "original successful creation"},
		{name: "missing receipt", change: func(_ *models.AutomationActionScope, req *models.AutomationActionRequest) {
			req.SourceActionID = uuid.NewString()
		}},
		{name: "missing UUID", change: func(_ *models.AutomationActionScope, req *models.AutomationActionRequest) {
			req.SourceActionID = ""
		}},
		{name: "invalid UUID", change: func(_ *models.AutomationActionScope, req *models.AutomationActionRequest) {
			req.SourceActionID = "123"
		}},
		{name: "noncanonical UUID", change: func(_ *models.AutomationActionScope, req *models.AutomationActionRequest) {
			req.SourceActionID = strings.ReplaceAll(req.SourceActionID, "-", "")
		}},
		{name: "creation grant alone", change: func(scope *models.AutomationActionScope, _ *models.AutomationActionRequest) {
			scope.Config.Actions = []models.AutomationActionKind{models.AutomationActionComment}
		}},
		{name: "different org", change: func(scope *models.AutomationActionScope, _ *models.AutomationActionRequest) {
			scope.Actor.OrgID = uuid.New()
		}},
		{name: "different automation", change: func(scope *models.AutomationActionScope, _ *models.AutomationActionRequest) {
			scope.AutomationID = uuid.New()
		}},
		{name: "different repository", change: func(scope *models.AutomationActionScope, _ *models.AutomationActionRequest) {
			scope.Actor.RepositoryID = uuid.New()
		}},
		{name: "different scope", change: func(scope *models.AutomationActionScope, _ *models.AutomationActionRequest) {
			scope.ScopeKey = "another-scope"
		}},
		{name: "different PR", change: func(_ *models.AutomationActionScope, req *models.AutomationActionRequest) {
			req.PRNumber++
		}},
		{name: "update receipt", receiptSQL: "kind='github_issue_comment_update'"},
		{name: "non-comment receipt", receiptSQL: "kind='slack_notification'"},
		{name: "unfinished creation", receiptSQL: "status='unknown'"},
		{name: "missing provider ID", receiptSQL: "provider_object_id=NULL"},
		{name: "invalid provider ID", receiptSQL: "provider_object_id='not-a-number'"},
		{name: "noncanonical provider ID", receiptSQL: "provider_object_id='00123'"},
		{name: "nonpositive provider ID", receiptSQL: "provider_object_id='0'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, actor := actionPostgresWithGrant(t, false, true)
			ctx, store := context.Background(), NewAutomationActionStore(pool)
			scope, err := store.Resolve(ctx, actor.OrgID, actor)
			require.NoError(t, err, "resolve the automation with the update grant")
			created := reserveAction(t, store, actor, "create", models.AutomationActionComment)
			sending, err := store.Claim(ctx, actor.OrgID, actor, created.ID)
			require.NoError(t, err, "claim the original creation")
			require.NoError(t, store.Finish(ctx, actor.OrgID, created.ID, *sending.SendToken, models.AutomationActionSucceeded, "123", "", ""), "record the successful creation")
			req := commentUpdateRequest(created.ID, "edit")
			if tt.change != nil {
				tt.change(&scope, &req)
			}
			if tt.receiptSQL != "" {
				_, err = pool.Exec(ctx, "UPDATE automation_actions SET "+tt.receiptSQL+" WHERE org_id=$1 AND id=$2", actor.OrgID, created.ID)
				require.NoError(t, err, "alter only the isolated source receipt")
			}
			binding, err := store.ResolveCommentTarget(ctx, scope.Actor.OrgID, scope, req)
			if tt.change != nil || tt.receiptSQL != "" {
				require.ErrorIs(t, err, ErrAutomationActionUnauthorized, "reject an unusable source receipt")
				return
			}
			require.NoError(t, err, "bind to the scoped creation receipt")
			require.Equal(t, models.AutomationActionCommentTarget{
				SourceActionID: created.ID, CommentID: "123", PreviousActionID: created.ID, PreviousText: "A notification",
			}, binding, "recover the original provider comment ID and expected body")
		})
	}
}

func TestAutomationCommentUpdatePostgresFencesCompetingEdits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		continuous bool
	}{
		{name: "per-run"},
		{name: "continuous target", continuous: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			testAutomationCommentUpdateFences(t, tt.continuous)
		})
	}
}

func testAutomationCommentUpdateFences(t *testing.T, continuous bool) {
	t.Helper()
	pool, actor := actionPostgresWithGrant(t, continuous, true)
	ctx, store := context.Background(), NewAutomationActionStore(pool)
	scope, err := store.Resolve(ctx, actor.OrgID, actor)
	require.NoError(t, err, "resolve the automation with the update grant")
	created := reserveAction(t, store, actor, "create", models.AutomationActionComment)
	sending, err := store.Claim(ctx, actor.OrgID, actor, created.ID)
	require.NoError(t, err, "claim the original creation")
	require.NoError(t, store.Finish(ctx, actor.OrgID, created.ID, *sending.SendToken, models.AutomationActionSucceeded, "123", "", ""), "record the original comment ID")
	req := commentUpdateRequest(created.ID, "revision-one")
	binding, err := store.ResolveCommentTarget(ctx, actor.OrgID, scope, req)
	require.NoError(t, err, "resolve the body before the first edit")
	tampered := binding
	tampered.CommentID = "456"
	_, err = store.Reserve(ctx, actor.OrgID, actor, req, strings.Repeat("c", 64), scope.Config, commentUpdatePayload(t, req, tampered))
	require.ErrorIs(t, err, ErrAutomationActionConflict, "reservation rejects a forged provider target")

	digest := strings.Repeat("c", 64)
	requests := []models.AutomationActionRequest{req, commentUpdateRequest(created.ID, "revision-two")}
	reserved := make([]models.AutomationAction, len(requests))
	for i, request := range requests {
		payload := commentUpdatePayload(t, request, binding)
		reserved[i], err = store.Reserve(ctx, actor.OrgID, actor, request, digest, scope.Config, payload)
		require.NoError(t, err, "reserve each edit before any claim")
		require.JSONEq(t, string(payload), string(reserved[i].Payload), "reservation freezes the expected body and provider target")
	}
	claims := make([]models.AutomationAction, len(reserved))
	errs := make([]error, len(reserved))
	var group sync.WaitGroup
	for i, action := range reserved {
		group.Go(func() {
			claims[i], errs[i] = store.Claim(ctx, actor.OrgID, actor, action.ID)
		})
	}
	group.Wait()
	winner, loser := 0, 1
	if errs[winner] != nil {
		winner, loser = loser, winner
	}
	require.NoError(t, errs[winner], "one action can claim the comment")
	require.ErrorIs(t, errs[loser], ErrAutomationActionBusy, "a different key cannot claim an in-flight comment")
	require.Equal(t, models.AutomationActionSending, claims[winner].Status, "the winner holds a send lease")

	next := commentUpdateRequest(created.ID, "revision-three")
	_, err = store.ResolveCommentTarget(ctx, actor.OrgID, scope, next)
	require.ErrorIs(t, err, ErrAutomationActionBusy, "new keys cannot bind during an active send")
	_, err = pool.Exec(ctx, `UPDATE automation_actions SET send_deadline_at=now()-interval '1 second' WHERE org_id=$1 AND id=$2`, actor.OrgID, claims[winner].ID)
	require.NoError(t, err, "simulate an interrupted edit")
	_, err = store.Claim(ctx, actor.OrgID, actor, reserved[loser].ID)
	require.ErrorIs(t, err, ErrAutomationActionBusy, "an expired sending lease still fences competing keys")
	require.NoError(t, store.Expire(ctx, actor.OrgID, scope, req.OperationKey), "mark the expired edit uncertain")
	_, err = store.Claim(ctx, actor.OrgID, actor, reserved[loser].ID)
	require.ErrorIs(t, err, ErrAutomationActionBusy, "an unknown edit still fences competing keys")
	_, err = store.ResolveCommentTarget(ctx, actor.OrgID, scope, next)
	require.ErrorIs(t, err, ErrAutomationActionBusy, "unknown effects block new target bindings")

	require.NoError(t, store.Finish(ctx, actor.OrgID, claims[winner].ID, *claims[winner].SendToken, models.AutomationActionSucceeded, "123", "", ""), "accept the winner's late provider receipt")
	_, err = store.Claim(ctx, actor.OrgID, actor, reserved[loser].ID)
	require.ErrorIs(t, err, ErrAutomationActionConflict, "a newer successful edit supersedes an older frozen body")
	latest, err := store.ResolveCommentTarget(ctx, actor.OrgID, scope, next)
	require.NoError(t, err, "resolve a fresh revision after reconciliation")
	require.Equal(t, models.AutomationActionCommentTarget{
		SourceActionID: created.ID, CommentID: "123", PreviousActionID: claims[winner].ID, PreviousText: requests[winner].Text,
	}, latest, "bind the newest successful body while keeping the creation receipt")
	fresh, err := store.Reserve(ctx, actor.OrgID, actor, next, digest, scope.Config, commentUpdatePayload(t, next, latest))
	require.NoError(t, err, "reserve a distinct revision against the latest body")
	_, err = store.Claim(ctx, actor.OrgID, actor, fresh.ID)
	require.NoError(t, err, "claim a fresh revision with the current expected body")

	replayed := reserveAction(t, store, actor, "create", models.AutomationActionComment)
	require.Equal(t, created.ID, replayed.ID, "creation replay reuses the original action")
	require.Equal(t, models.AutomationActionSucceeded, replayed.Status, "creation remains succeeded after subsequent edits")
	require.Equal(t, created.Payload, replayed.Payload, "creation replay retains the original frozen body")
	require.NotNil(t, replayed.ProviderObjectID, "creation replay retains its provider receipt")
	require.Equal(t, "123", *replayed.ProviderObjectID, "creation replay retains the original comment ID")
}
