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

func newReferenceDocRow(id, orgID, logicalID uuid.UUID, title, content string, active bool, now time.Time) []any {
	return []any{
		id, orgID, title, content, "roadmap", 0,
		"manual", nil, nil, nil, nil,
		active, logicalID, contentHash(content),
		nil, now, now,
	}
}

func TestReferenceDocumentStore_Create(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewReferenceDocumentStore(mock)
	orgID := uuid.New()
	generatedID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("INSERT INTO reference_documents").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "logical_id", "created_at", "updated_at"}).
				AddRow(generatedID, logicalID, now, now),
		)

	doc := &models.ReferenceDocument{
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

func TestReferenceDocumentStore_Update_NoChange(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewReferenceDocumentStore(mock)
	orgID := uuid.New()
	docID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	doc := &models.ReferenceDocument{
		ID: docID, OrgID: orgID,
		Title: "Roadmap", Content: "content", DocType: "roadmap",
		SourceType: "manual", LogicalID: logicalID,
	}

	// Transaction: begin, fetch current (identical), commit (no-op).
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM reference_documents WHERE id .+ AND org_id .+ AND active = true FOR UPDATE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newReferenceDocRow(docID, orgID, logicalID, "Roadmap", "content", true, now)...),
		)
	mock.ExpectCommit()

	err = store.Update(context.Background(), doc)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReferenceDocumentStore_Update_WithChanges(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewReferenceDocumentStore(mock)
	orgID := uuid.New()
	docID := uuid.New()
	newDocID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	doc := &models.ReferenceDocument{
		ID: docID, OrgID: orgID,
		Title: "Updated Roadmap", Content: "new content", DocType: "roadmap",
		SourceType: "manual", LogicalID: logicalID,
	}

	mock.ExpectBegin()
	// Fetch current (different content).
	mock.ExpectQuery("SELECT .+ FROM reference_documents WHERE id .+ AND org_id .+ AND active = true FOR UPDATE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newReferenceDocRow(docID, orgID, logicalID, "Roadmap", "old content", true, now)...),
		)
	// Deactivate.
	mock.ExpectExec("UPDATE reference_documents SET active = false").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// Insert new version.
	mock.ExpectQuery("INSERT INTO reference_documents").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newReferenceDocRow(newDocID, orgID, logicalID, "Updated Roadmap", "new content", true, now)...),
		)
	mock.ExpectCommit()

	err = store.Update(context.Background(), doc)
	require.NoError(t, err)
	require.Equal(t, newDocID, doc.ID, "doc should be updated in-place with new row ID")
	require.Equal(t, logicalID, doc.LogicalID, "logical_id should be preserved")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReferenceDocumentStore_Restore(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewReferenceDocumentStore(mock)
	orgID := uuid.New()
	currentID := uuid.New()
	oldVersionID := uuid.New()
	restoredID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	mock.ExpectBegin()
	// Fetch old version.
	mock.ExpectQuery("SELECT .+ FROM reference_documents WHERE id .+ AND org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newReferenceDocRow(oldVersionID, orgID, logicalID, "Old Title", "old content", false, now)...),
		)
	// Deactivate current.
	mock.ExpectQuery("UPDATE reference_documents SET active = false").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"logical_id"}).AddRow(logicalID))
	// Insert restored version.
	mock.ExpectQuery("INSERT INTO reference_documents").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newReferenceDocRow(restoredID, orgID, logicalID, "Old Title", "old content", true, now)...),
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

func TestReferenceDocumentStore_Restore_LogicalIDMismatch(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewReferenceDocumentStore(mock)
	orgID := uuid.New()
	currentID := uuid.New()
	oldVersionID := uuid.New()
	logicalA := uuid.New()
	logicalB := uuid.New()
	now := time.Now()

	mock.ExpectBegin()
	// Fetch old version — belongs to logicalB.
	mock.ExpectQuery("SELECT .+ FROM reference_documents WHERE id .+ AND org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newReferenceDocRow(oldVersionID, orgID, logicalB, "Other Doc", "other content", false, now)...),
		)
	// Deactivate current — returns logicalA.
	mock.ExpectQuery("UPDATE reference_documents SET active = false").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"logical_id"}).AddRow(logicalA))
	mock.ExpectRollback()

	_, err = store.Restore(context.Background(), orgID, currentID, oldVersionID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "different logical document")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReferenceDocumentStore_ListVersions(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewReferenceDocumentStore(mock)
	orgID := uuid.New()
	docID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM reference_documents").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newReferenceDocRow(uuid.New(), orgID, logicalID, "V2", "v2 content", true, now)...).
				AddRow(newReferenceDocRow(docID, orgID, logicalID, "V1", "v1 content", false, now.Add(-time.Hour))...),
		)

	versions, err := store.ListVersions(context.Background(), orgID, docID, 0)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	require.True(t, versions[0].Active, "first version should be active (newest)")
	require.False(t, versions[1].Active, "second version should be inactive")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReferenceDocumentStore_CreateDocumentSetPin(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewReferenceDocumentStore(mock)
	orgID := uuid.New()
	pinID := uuid.New()
	now := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO reference_context_set_pins").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "created_at"}).AddRow(pinID, orgID, now))
	mock.ExpectExec("INSERT INTO reference_context_set_pin_members \\(pin_id, reference_document_id\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 3))
	mock.ExpectCommit()

	pin, err := store.CreateDocumentSetPin(context.Background(), orgID)
	require.NoError(t, err)
	require.Equal(t, pinID, pin.ID)
	require.Equal(t, orgID, pin.OrgID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReferenceDocumentStore_GetPinMembers(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewReferenceDocumentStore(mock)
	orgID := uuid.New()
	pinID := uuid.New()
	docID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("INNER JOIN reference_context_set_pin_members m ON d.id = m.reference_document_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newReferenceDocRow(docID, orgID, logicalID, "Pinned Doc", "pinned content", true, now)...),
		)

	members, err := store.GetPinMembers(context.Background(), orgID, pinID)
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.Equal(t, docID, members[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReferenceDocumentStore_Delete_SoftDelete(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewReferenceDocumentStore(mock)
	orgID := uuid.New()
	docID := uuid.New()

	mock.ExpectExec("UPDATE reference_documents SET active = false").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.Delete(context.Background(), orgID, docID)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReferenceDocumentStore_ListByOrg(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewReferenceDocumentStore(mock)
	orgID := uuid.New()
	docID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM reference_documents WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newReferenceDocRow(docID, orgID, logicalID, "Doc 1", "content 1", true, now)...),
		)

	docs, err := store.ListByOrg(context.Background(), orgID)
	require.NoError(t, err)
	require.Len(t, docs, 1)
	require.Equal(t, "Doc 1", docs[0].Title)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReferenceDocumentStore_GetByID(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewReferenceDocumentStore(mock)
	orgID := uuid.New()
	docID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM reference_documents WHERE id .+ AND org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newReferenceDocRow(docID, orgID, logicalID, "Fetched", "content", true, now)...),
		)

	doc, err := store.GetByID(context.Background(), orgID, docID)
	require.NoError(t, err)
	require.Equal(t, docID, doc.ID)
	require.Equal(t, "Fetched", doc.Title)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReferenceDocumentStore_GetActiveByLogicalID(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewReferenceDocumentStore(mock)
	orgID := uuid.New()
	docID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM reference_documents WHERE org_id .+ logical_id .+ active = true").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestCols).
				AddRow(newReferenceDocRow(docID, orgID, logicalID, "Active Version", "content", true, now)...),
		)

	doc, err := store.GetActiveByLogicalID(context.Background(), orgID, logicalID)
	require.NoError(t, err)
	require.Equal(t, docID, doc.ID)
	require.True(t, doc.Active)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReferenceDocumentStore_GetDocumentSetPin(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewReferenceDocumentStore(mock)
	orgID := uuid.New()
	pinID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT id, org_id, created_at FROM reference_context_set_pins WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "created_at"}).
				AddRow(pinID, orgID, now),
		)

	pin, err := store.GetDocumentSetPin(context.Background(), orgID, pinID)
	require.NoError(t, err)
	require.Equal(t, pinID, pin.ID)
	require.Equal(t, orgID, pin.OrgID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReferenceDocumentStore_ListDocumentSetPins_DefaultLimit(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewReferenceDocumentStore(mock)
	orgID := uuid.New()
	pinID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM reference_context_set_pins").
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
