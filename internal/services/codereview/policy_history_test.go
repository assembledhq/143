package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type policyHistoryStoreStub struct {
	versions      []models.CodeReviewPolicyRecord
	byID          map[uuid.UUID]models.CodeReviewPolicyRecord
	resolved      models.CodeReviewResolvedPolicy
	listErr       error
	getErr        error
	saveErr       error
	saved         models.CodeReviewPolicyRecord
	savedConfig   models.CodeReviewPolicyConfig
	savedExpected int
	savedByUserID *uuid.UUID
	listBefore    *int
	listLimit     int
}

func (s *policyHistoryStoreStub) ListPolicyVersions(_ context.Context, _ uuid.UUID, before *int, limit int) ([]models.CodeReviewPolicyRecord, error) {
	s.listBefore, s.listLimit = before, limit
	return s.versions, s.listErr
}

func (s *policyHistoryStoreStub) GetPolicyByID(_ context.Context, _ uuid.UUID, policyID uuid.UUID) (models.CodeReviewPolicyRecord, error) {
	if s.getErr != nil {
		return models.CodeReviewPolicyRecord{}, s.getErr
	}
	policy, ok := s.byID[policyID]
	if !ok {
		return models.CodeReviewPolicyRecord{}, pgx.ErrNoRows
	}
	return policy, nil
}

func (s *policyHistoryStoreStub) ResolvePolicy(context.Context, uuid.UUID) (models.CodeReviewResolvedPolicy, error) {
	return s.resolved, nil
}

func (s *policyHistoryStoreStub) SavePolicyExpectingVersion(_ context.Context, _ uuid.UUID, config models.CodeReviewPolicyConfig, expected int, userID *uuid.UUID) (models.CodeReviewPolicyRecord, error) {
	s.savedConfig, s.savedExpected, s.savedByUserID = config, expected, userID
	return s.saved, s.saveErr
}

type policyHistoryAuditStoreStub struct {
	entries     []models.AuditLogWithActorName
	err         error
	resourceIDs []string
}

func (s *policyHistoryAuditStoreStub) ListLatestByResourceIDs(_ context.Context, _ uuid.UUID, _ models.AuditResourceType, resourceIDs []string) ([]models.AuditLogWithActorName, error) {
	s.resourceIDs = append([]string(nil), resourceIDs...)
	return s.entries, s.err
}

func TestPolicyHistoryServiceListSummarizesExactChanges(t *testing.T) {
	t.Parallel()

	orgID, userID := uuid.New(), uuid.New()
	older := policyHistoryRecord(orgID, 7, false)
	newer := policyHistoryRecord(orgID, 8, true)
	newer.ReviewInstructions = "Require focused tests."
	newer.InlineCommentLimit = 6
	now := time.Date(2026, 8, 18, 18, 0, 0, 0, time.UTC)
	resourceID := newer.ID.String()
	actorName := "Alice Smith"
	store := &policyHistoryStoreStub{versions: []models.CodeReviewPolicyRecord{newer, older}}
	audits := &policyHistoryAuditStoreStub{entries: []models.AuditLogWithActorName{{
		AuditLog: models.AuditLog{
			ID: 42, OrgID: orgID, ActorType: models.AuditActorUser, ActorID: userID.String(), UserID: &userID,
			Action: models.AuditActionCodeReviewPolicyUpdated, ResourceType: models.AuditResourceCodeReviewPolicy,
			ResourceID: &resourceID, Details: json.RawMessage(`{"source":"manual","reason":"Tighten reviews"}`), CreatedAt: now,
		},
		ActorName: &actorName,
	}}}

	page, err := NewPolicyHistoryService(store, audits, zerolog.Nop()).List(context.Background(), orgID, nil, 1)

	require.NoError(t, err, "listing policy history should succeed")
	require.Len(t, page.Versions, 1, "the requested page should contain one version")
	require.Equal(t, "8", page.NextCursor, "the last visible policy version should be the next cursor")
	require.Equal(t, "2 policy fields changed", page.Versions[0].Summary, "the version summary should describe the number of effective changes")
	require.Equal(t, []models.CodeReviewPolicyChangedField{
		{Path: "review_instructions", Label: "Review instructions", Kind: models.CodeReviewPolicyChangeKindText},
		{Path: "inline_comment_limit", Label: "Inline comment limit", Kind: models.CodeReviewPolicyChangeKindValue},
	}, page.Versions[0].ChangedFields, "the summary should expose the exact changed fields in display order")
	require.Equal(t, "Tighten reviews", page.Versions[0].Audit.Reason, "the summary should include the audit reason")
	require.Equal(t, actorName, page.Versions[0].Audit.ActorName, "the summary should include the resolved actor name")
	require.Equal(t, []string{newer.ID.String()}, audits.resourceIDs, "audit metadata should be fetched in one batch for visible versions")
	require.Equal(t, 2, store.listLimit, "the store should fetch one lookahead record for pagination and diffs")
}

func TestPolicyHistoryServiceCompareReturnsBeforeAndAfterValues(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	older := policyHistoryRecord(orgID, 2, false)
	newer := policyHistoryRecord(orgID, 3, true)
	older.RiskPolicy.MaxFilesChanged = 5
	newer.RiskPolicy.MaxFilesChanged = 10
	store := &policyHistoryStoreStub{byID: map[uuid.UUID]models.CodeReviewPolicyRecord{older.ID: older, newer.ID: newer}}

	comparison, err := NewPolicyHistoryService(store, &policyHistoryAuditStoreStub{}, zerolog.Nop()).Compare(context.Background(), orgID, newer.ID, older.ID)

	require.NoError(t, err, "comparing policy versions should succeed")
	require.Equal(t, newer.ID, comparison.Newer.ID, "the comparison should identify the newer version")
	require.Equal(t, older.ID, comparison.Older.ID, "the comparison should identify the older version")
	require.Equal(t, []models.CodeReviewPolicyFieldChange{{
		CodeReviewPolicyChangedField: models.CodeReviewPolicyChangedField{Path: "risk_policy.max_files_changed", Label: "Maximum files changed", Kind: models.CodeReviewPolicyChangeKindValue},
		Before:                       json.Number("5"), After: json.Number("10"),
	}}, comparison.Changes, "the comparison should include exact before and after values")
}

func TestPolicyHistoryServiceRestoreCreatesNewVersionFromHistoricalConfig(t *testing.T) {
	t.Parallel()

	orgID, userID := uuid.New(), uuid.New()
	target := policyHistoryRecord(orgID, 4, false)
	target.ReviewInstructions = "Restore this guidance."
	saved := policyHistoryRecord(orgID, 9, true)
	store := &policyHistoryStoreStub{byID: map[uuid.UUID]models.CodeReviewPolicyRecord{target.ID: target}, saved: saved}

	result, err := NewPolicyHistoryService(store, &policyHistoryAuditStoreStub{}, zerolog.Nop()).Restore(context.Background(), orgID, target.ID, userID, 8)

	require.NoError(t, err, "restoring a historical policy should succeed")
	require.Equal(t, saved, result.Policy, "restore should return the newly-created active version")
	require.Equal(t, target, result.RestoredFrom, "restore should identify the historical source version")
	require.Equal(t, target.Config(), store.savedConfig, "restore should persist the historical configuration as a new version")
	require.Equal(t, 8, store.savedExpected, "restore should guard against a stale active policy")
	require.Equal(t, &userID, store.savedByUserID, "restore should attribute the new version to the acting user")
}

func TestPolicyHistoryServiceRestoreReportsCurrentVersionOnConflict(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	target := policyHistoryRecord(orgID, 4, false)
	current := policyHistoryRecord(orgID, 10, true)
	store := &policyHistoryStoreStub{
		byID:     map[uuid.UUID]models.CodeReviewPolicyRecord{target.ID: target},
		saveErr:  db.ErrCodeReviewPolicyVersionConflict,
		resolved: models.CodeReviewResolvedPolicy{Policy: &current},
	}

	_, err := NewPolicyHistoryService(store, &policyHistoryAuditStoreStub{}, zerolog.Nop()).Restore(context.Background(), orgID, target.ID, uuid.New(), 9)

	var conflict *CodeReviewPolicyRestoreConflictError
	require.ErrorAs(t, err, &conflict, "restore should return a typed version conflict")
	require.Equal(t, 10, conflict.CurrentVersion, "the conflict should report the latest active policy version")
}

func TestPolicyHistoryServiceRejectsInvalidComparisonsAndActiveRestore(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	active := policyHistoryRecord(orgID, 5, true)
	older := policyHistoryRecord(orgID, 4, false)
	store := &policyHistoryStoreStub{byID: map[uuid.UUID]models.CodeReviewPolicyRecord{active.ID: active, older.ID: older}}
	service := NewPolicyHistoryService(store, &policyHistoryAuditStoreStub{}, zerolog.Nop())

	_, compareErr := service.Compare(context.Background(), orgID, older.ID, active.ID)
	_, restoreErr := service.Restore(context.Background(), orgID, active.ID, uuid.New(), 5)

	require.ErrorIs(t, compareErr, ErrCodeReviewPolicyComparisonOrder, "comparison should require the newer version first")
	require.True(t, errors.Is(restoreErr, ErrCodeReviewPolicyAlreadyActive), "restore should reject the already-active policy")
}

func policyHistoryRecord(orgID uuid.UUID, version int, active bool) models.CodeReviewPolicyRecord {
	config := models.DefaultCodeReviewPolicyConfig()
	return models.CodeReviewPolicyRecord{
		ID: uuid.New(), OrgID: orgID, Active: active, Version: version,
		Enabled: config.Enabled, ApprovalMode: config.ApprovalMode,
		ReviewInstructions: config.ReviewInstructions, AutomatedApprovalPolicy: config.AutomatedApprovalPolicy,
		DescriptionPolicy: config.DescriptionPolicy, RiskPolicy: config.RiskPolicy,
		AgentRoster: config.AgentRoster, InlineCommentLimit: config.InlineCommentLimit,
		CreatedAt: time.Date(2026, 8, 18, version, 0, 0, 0, time.UTC),
	}
}
