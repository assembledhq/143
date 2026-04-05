package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var pmDocTestCols = []string{
	"id", "org_id", "title", "content", "doc_type", "sort_order",
	"source_type", "source_url", "source_id", "source_meta", "last_synced_at",
	"active", "logical_id", "content_hash",
	"created_by", "created_at", "updated_at",
}

func newPMDocRow(id, orgID, logicalID uuid.UUID, title, content string, active bool, now time.Time) []any {
	return []any{
		id, orgID, title, content, "roadmap", 0,
		"manual", nil, nil, nil, nil,
		active, logicalID, contentHash(content),
		nil, now, now,
	}
}

func TestPMDocumentStore_Create(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPMDocumentStore(mock)
	orgID := uuid.New()
	generatedID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("INSERT INTO pm_documents").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "logical_id", "created_at", "updated_at"}).
				AddRow(generatedID, logicalID, now, now),
		)

	doc := &models.PMDocument{
		OrgID:   orgID,
		Title:   "Roadmap Q3",
		Content: "Ship versioning",
		DocType: "roadmap",
	}
	err = store.Create(context.Background(), doc)
	require.NoError(t, err)
	require.Equal(t, generatedID, doc.ID)
	require.Equal(t, logicalID, doc.LogicalID)
	require.True(t, doc.Active)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMDocumentStore_Update_NoChange(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPMDocumentStore(mock)
	orgID := uuid.New()
	docID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	doc := &models.PMDocument{
		ID: docID, OrgID: orgID,
		Title: "Roadmap", Content: "content", DocType: "roadmap",
		SourceType: "manual", LogicalID: logicalID,
	}

	// Transaction: begin, fetch current (identical), commit (no-op).
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM pm_documents WHERE id .+ AND org_id .+ AND active = true FOR UPDATE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newPMDocRow(docID, orgID, logicalID, "Roadmap", "content", true, now)...),
		)
	mock.ExpectCommit()

	err = store.Update(context.Background(), doc)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMDocumentStore_Update_WithChanges(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPMDocumentStore(mock)
	orgID := uuid.New()
	docID := uuid.New()
	newDocID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	doc := &models.PMDocument{
		ID: docID, OrgID: orgID,
		Title: "Updated Roadmap", Content: "new content", DocType: "roadmap",
		SourceType: "manual", LogicalID: logicalID,
	}

	mock.ExpectBegin()
	// Fetch current (different content).
	mock.ExpectQuery("SELECT .+ FROM pm_documents WHERE id .+ AND org_id .+ AND active = true FOR UPDATE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newPMDocRow(docID, orgID, logicalID, "Roadmap", "old content", true, now)...),
		)
	// Deactivate.
	mock.ExpectExec("UPDATE pm_documents SET active = false").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// Insert new version.
	mock.ExpectQuery("INSERT INTO pm_documents").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newPMDocRow(newDocID, orgID, logicalID, "Updated Roadmap", "new content", true, now)...),
		)
	mock.ExpectCommit()

	err = store.Update(context.Background(), doc)
	require.NoError(t, err)
	require.Equal(t, newDocID, doc.ID, "doc should be updated in-place with new row ID")
	require.Equal(t, logicalID, doc.LogicalID, "logical_id should be preserved")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMDocumentStore_Restore(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPMDocumentStore(mock)
	orgID := uuid.New()
	currentID := uuid.New()
	oldVersionID := uuid.New()
	restoredID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	mock.ExpectBegin()
	// Fetch old version.
	mock.ExpectQuery("SELECT .+ FROM pm_documents WHERE id .+ AND org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newPMDocRow(oldVersionID, orgID, logicalID, "Old Title", "old content", false, now)...),
		)
	// Deactivate current.
	mock.ExpectQuery("UPDATE pm_documents SET active = false").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"logical_id"}).AddRow(logicalID))
	// Insert restored version.
	mock.ExpectQuery("INSERT INTO pm_documents").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newPMDocRow(restoredID, orgID, logicalID, "Old Title", "old content", true, now)...),
		)
	mock.ExpectCommit()

	restored, err := store.Restore(context.Background(), orgID, currentID, oldVersionID)
	require.NoError(t, err)
	require.Equal(t, restoredID, restored.ID)
	require.Equal(t, logicalID, restored.LogicalID)
	require.True(t, restored.Active)
	require.Equal(t, "old content", restored.Content)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMDocumentStore_Restore_LogicalIDMismatch(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPMDocumentStore(mock)
	orgID := uuid.New()
	currentID := uuid.New()
	oldVersionID := uuid.New()
	logicalA := uuid.New()
	logicalB := uuid.New()
	now := time.Now()

	mock.ExpectBegin()
	// Fetch old version — belongs to logicalB.
	mock.ExpectQuery("SELECT .+ FROM pm_documents WHERE id .+ AND org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newPMDocRow(oldVersionID, orgID, logicalB, "Other Doc", "other content", false, now)...),
		)
	// Deactivate current — returns logicalA.
	mock.ExpectQuery("UPDATE pm_documents SET active = false").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"logical_id"}).AddRow(logicalA))
	mock.ExpectRollback()

	_, err = store.Restore(context.Background(), orgID, currentID, oldVersionID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "different logical document")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMDocumentStore_ListVersions(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPMDocumentStore(mock)
	orgID := uuid.New()
	docID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pm_documents").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newPMDocRow(uuid.New(), orgID, logicalID, "V2", "v2 content", true, now)...).
				AddRow(newPMDocRow(docID, orgID, logicalID, "V1", "v1 content", false, now.Add(-time.Hour))...),
		)

	versions, err := store.ListVersions(context.Background(), orgID, docID, 0)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	require.True(t, versions[0].Active, "first version should be active (newest)")
	require.False(t, versions[1].Active, "second version should be inactive")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMDocumentStore_CreateDocumentSetPin(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPMDocumentStore(mock)
	orgID := uuid.New()
	pinID := uuid.New()
	now := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO pm_document_set_pins").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "created_at"}).AddRow(pinID, orgID, now))
	mock.ExpectExec("INSERT INTO pm_document_set_pin_members").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 3))
	mock.ExpectCommit()

	pin, err := store.CreateDocumentSetPin(context.Background(), orgID)
	require.NoError(t, err)
	require.Equal(t, pinID, pin.ID)
	require.Equal(t, orgID, pin.OrgID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMDocumentStore_GetPinMembers(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPMDocumentStore(mock)
	orgID := uuid.New()
	pinID := uuid.New()
	docID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pm_documents d").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newPMDocRow(docID, orgID, logicalID, "Pinned Doc", "pinned content", true, now)...),
		)

	members, err := store.GetPinMembers(context.Background(), orgID, pinID)
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.Equal(t, docID, members[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMDocumentStore_DeleteByOrgAndSourceType_RefreshAllowed(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPMDocumentStore(mock)
	orgID := uuid.New()

	mock.ExpectExec("DELETE FROM pm_documents").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 2))

	err = store.DeleteByOrgAndSourceType(context.Background(), orgID, models.PMDocSourceRefresh)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMDocumentStore_DeleteByOrgAndSourceType_NonRefreshRejected(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPMDocumentStore(mock)
	orgID := uuid.New()

	err = store.DeleteByOrgAndSourceType(context.Background(), orgID, models.PMDocSourceManual)
	require.Error(t, err)
	require.Contains(t, err.Error(), "restricted to ephemeral source types")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMDocumentStore_Delete_SoftDelete(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPMDocumentStore(mock)
	orgID := uuid.New()
	docID := uuid.New()

	mock.ExpectExec("UPDATE pm_documents SET active = false").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.Delete(context.Background(), orgID, docID)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMDocumentStore_ListDocumentSetPins_DefaultLimit(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPMDocumentStore(mock)
	orgID := uuid.New()
	pinID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pm_document_set_pins").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "created_at"}).
				AddRow(pinID, orgID, now),
		)

	pins, err := store.ListDocumentSetPins(context.Background(), orgID, 0)
	require.NoError(t, err)
	require.Len(t, pins, 1)
	require.Equal(t, pinID, pins[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}
