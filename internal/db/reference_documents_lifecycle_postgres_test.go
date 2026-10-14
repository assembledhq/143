package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// PostgreSQL may decode an instant using time.Local when the process runs in
// UTC. Canonicalize only time representations; retain every document field.
func referenceVersionUTCTimes(doc models.ReferenceDocument) models.ReferenceDocument {
	doc.CreatedAt, doc.UpdatedAt = doc.CreatedAt.UTC(), doc.UpdatedAt.UTC()
	if doc.LastSyncedAt != nil {
		synced := doc.LastSyncedAt.UTC()
		doc.LastSyncedAt = &synced
	}
	return doc
}

func referenceVersionsUTCTimes(docs []models.ReferenceDocument) []models.ReferenceDocument {
	result := make([]models.ReferenceDocument, len(docs))
	for i, doc := range docs {
		result[i] = referenceVersionUTCTimes(doc)
	}
	return result
}

func TestReferenceDocumentVersionLifecyclePostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name string
	}{
		{name: "version history and pinned context survive update restore and delete"},
		{name: "stale writer cannot replace a newer active version"},
		{name: "failed restore rolls back deactivation"},
		{name: "tenant cannot read or replace another tenant document"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := NewReferenceDocumentStore(pool)
			org := &models.Organization{Name: "document-test-" + uuid.NewString(), Settings: json.RawMessage(`{}`)}
			require.NoError(t, NewOrganizationStore(pool).Create(ctx, org), "document fixture should create its own tenant")
			original := models.ReferenceDocument{OrgID: org.ID, Title: "Roadmap", Content: "Release safely", DocType: "roadmap", SortOrder: 2}
			require.NoError(t, store.Create(ctx, &original), "create should persist the original active document")
			hash := sha256.Sum256([]byte("Release safely"))
			expectedOriginal := models.ReferenceDocument{ID: original.ID, LogicalID: original.LogicalID, OrgID: org.ID, Title: "Roadmap", Content: "Release safely", DocType: "roadmap", SortOrder: 2, SourceType: models.ReferenceDocSourceManual, Active: true, ContentHash: hex.EncodeToString(hash[:]), CreatedAt: original.CreatedAt, UpdatedAt: original.UpdatedAt}
			require.NotEqual(t, uuid.Nil, original.ID, "created document should have a durable row identity")
			require.NotEqual(t, uuid.Nil, original.LogicalID, "created document should have a stable version identity")
			loaded, err := store.GetByID(ctx, org.ID, original.ID)
			require.NoError(t, err, "owner should read the complete persisted document")
			require.Equal(t, expectedOriginal, loaded, "create should persist all defaults and the content digest")

			switch tt.name {
			case "tenant cannot read or replace another tenant document":
				foreignOrg := uuid.New()
				_, err := store.GetByID(ctx, foreignOrg, original.ID)
				require.ErrorIs(t, err, pgx.ErrNoRows, "foreign tenant must not read a document by known row id")
				_, err = store.GetActiveByLogicalID(ctx, foreignOrg, original.LogicalID)
				require.ErrorIs(t, err, pgx.ErrNoRows, "foreign tenant must not read a document by known logical id")
				versions, err := store.ListVersions(ctx, foreignOrg, original.ID, 100)
				require.NoError(t, err, "foreign tenant version listing should be empty")
				require.Equal(t, []models.ReferenceDocument(nil), versions, "version subquery must scope both document and history to the tenant")
				foreign := original
				foreign.OrgID, foreign.Content = foreignOrg, "Unauthorized replacement"
				require.ErrorIs(t, store.Update(ctx, &foreign), pgx.ErrNoRows, "foreign tenant must not deactivate or replace another tenant's version")
				require.NoError(t, store.Delete(ctx, foreignOrg, original.ID), "foreign tenant delete should be a scoped no-op")
				loaded, err = store.GetActiveByLogicalID(ctx, org.ID, original.LogicalID)
				require.NoError(t, err, "unauthorized operations must preserve the owner's active version")
				require.Equal(t, expectedOriginal, loaded, "unauthorized operations must preserve every field")
			case "failed restore rolls back deactivation":
				other := models.ReferenceDocument{OrgID: org.ID, Title: "Other", Content: "Separate document", DocType: "roadmap"}
				require.NoError(t, store.Create(ctx, &other), "create a different logical document in the same tenant")
				_, err := store.Restore(ctx, org.ID, original.ID, other.ID)
				require.ErrorContains(t, err, "different logical document", "restore must reject crossing logical document identities")
				loaded, err = store.GetActiveByLogicalID(ctx, org.ID, original.LogicalID)
				require.NoError(t, err, "failed restore must roll back the prior deactivation")
				require.Equal(t, expectedOriginal, loaded, "failed restore must preserve the complete original active version")
			case "stale writer cannot replace a newer active version":
				stale := original
				newer := original
				newer.Content = "Newer content"
				require.NoError(t, store.Update(ctx, &newer), "first writer should create a new active version")
				stale.Content = "Stale replacement"
				require.ErrorIs(t, store.Update(ctx, &stale), pgx.ErrNoRows, "stale row id must not overwrite the current active version")
				loaded, err = store.GetActiveByLogicalID(ctx, org.ID, original.LogicalID)
				require.NoError(t, err, "newer version should remain active after stale write rejection")
				require.Equal(t, newer, loaded, "stale write must leave the exact winning version unchanged")
			default:
				pin, err := store.CreateDocumentSetPin(ctx, org.ID)
				require.NoError(t, err, "pin should capture the current document version")
				unchanged := original
				require.NoError(t, store.Update(ctx, &unchanged), "unchanged update should commit as a no-op")
				require.Equal(t, original, unchanged, "no-op update must preserve row identity and timestamps")
				updated := original
				url, sourceID := "https://example.test/roadmap", "notion-page"
				synced := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
				updated.Title, updated.Content, updated.DocType, updated.SortOrder = "Strategy", "New direction", "context", 4
				updated.SourceType, updated.SourceURL, updated.SourceID = models.ReferenceDocSourceNotion, &url, &sourceID
				updated.SourceMeta, updated.LastSyncedAt = json.RawMessage(`{"author":"team"}`), &synced
				require.NoError(t, store.Update(ctx, &updated), "changed document should create a new active version atomically")
				require.NotEqual(t, original.ID, updated.ID, "update must insert a new row instead of rewriting pinned content")
				expectedUpdated := expectedOriginal
				expectedUpdated.ID, expectedUpdated.CreatedAt, expectedUpdated.UpdatedAt = updated.ID, updated.CreatedAt, updated.UpdatedAt
				expectedUpdated.Title, expectedUpdated.Content, expectedUpdated.DocType, expectedUpdated.SortOrder = "Strategy", "New direction", "context", 4
				expectedUpdated.SourceType, expectedUpdated.SourceURL, expectedUpdated.SourceID = models.ReferenceDocSourceNotion, &url, &sourceID
				expectedUpdated.SourceMeta, expectedUpdated.LastSyncedAt = json.RawMessage(`{"author": "team"}`), &synced
				newHash := sha256.Sum256([]byte("New direction"))
				expectedUpdated.ContentHash = hex.EncodeToString(newHash[:])
				require.Equal(t, referenceVersionUTCTimes(expectedUpdated), referenceVersionUTCTimes(updated), "new version should preserve identity and persist every changed field")
				expectedOriginal.Active = false
				members, err := store.GetPinMembers(ctx, org.ID, pin.ID)
				require.NoError(t, err, "pin should still resolve after document replacement")
				require.Equal(t, []models.ReferenceDocument{expectedOriginal}, members, "pin should retain the original content rather than follow the active version")
				restored, err := store.Restore(ctx, org.ID, updated.ID, original.ID)
				require.NoError(t, err, "restore should create another version from the pinned source")
				expectedRestored := expectedOriginal
				expectedRestored.ID, expectedRestored.Active = restored.ID, true
				expectedRestored.CreatedAt, expectedRestored.UpdatedAt = restored.CreatedAt, restored.UpdatedAt
				require.Equal(t, expectedRestored, restored, "restore should recover the complete source document with a new row identity")
				expectedUpdated.Active = false
				versions, err := store.ListVersions(ctx, org.ID, original.ID, 0)
				require.NoError(t, err, "history should resolve from any original row id")
				require.Equal(t, referenceVersionsUTCTimes([]models.ReferenceDocument{expectedRestored, expectedUpdated, expectedOriginal}), referenceVersionsUTCTimes(versions), "history should contain exactly three versions, proving the no-op did not add one")
				require.NoError(t, store.Delete(ctx, org.ID, restored.ID), "delete should retire only the active version")
				active, err := store.ListByOrg(ctx, org.ID)
				require.NoError(t, err, "deleted document should disappear from the active list")
				require.Equal(t, []models.ReferenceDocument(nil), active, "soft deletion should leave no active version")
				members, err = store.GetPinMembers(ctx, org.ID, pin.ID)
				require.NoError(t, err, "pin should remain readable after soft deletion")
				require.Equal(t, []models.ReferenceDocument{expectedOriginal}, members, "soft deletion must not destroy previously pinned context")
			}
		})
	}
}
