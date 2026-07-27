package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// ReferenceDocumentStore manages reference documents using the insert-only versioning pattern
// (same as MemoryStore). Updates deactivate the current row and insert a new
// active row within a transaction. The logical_id links all versions of a
// document together.
type ReferenceDocumentStore struct {
	db TxStarter
}

func NewReferenceDocumentStore(db TxStarter) *ReferenceDocumentStore {
	return &ReferenceDocumentStore{db: db}
}

// Begin starts a transaction using the underlying DB handle.
// lint:allow-no-orgid reason="transaction helper; org scoping is enforced by the wrapped queries"
func (s *ReferenceDocumentStore) Begin(ctx context.Context) (pgx.Tx, error) {
	return s.db.Begin(ctx)
}

// WithTx returns a new ReferenceDocumentStore that uses the given transaction.
// lint:allow-no-orgid reason="transaction helper; org scoping is enforced by the wrapped queries"
func (s *ReferenceDocumentStore) WithTx(tx pgx.Tx) *ReferenceDocumentStore {
	return &ReferenceDocumentStore{db: tx}
}

const referenceDocumentColumns = `id, org_id, title, content, doc_type, sort_order,
	source_type, source_url, source_id, source_meta, last_synced_at,
	active, logical_id, content_hash,
	created_by, created_at, updated_at`

func scanReferenceDoc(row pgx.Row) (models.ReferenceDocument, error) {
	var d models.ReferenceDocument
	err := row.Scan(
		&d.ID, &d.OrgID, &d.Title, &d.Content, &d.DocType,
		&d.SortOrder, &d.SourceType, &d.SourceURL, &d.SourceID,
		&d.SourceMeta, &d.LastSyncedAt,
		&d.Active, &d.LogicalID, &d.ContentHash,
		&d.CreatedBy, &d.CreatedAt, &d.UpdatedAt,
	)
	return d, err
}

func scanReferenceDocs(rows pgx.Rows) ([]models.ReferenceDocument, error) {
	var docs []models.ReferenceDocument
	for rows.Next() {
		var d models.ReferenceDocument
		err := rows.Scan(
			&d.ID, &d.OrgID, &d.Title, &d.Content, &d.DocType,
			&d.SortOrder, &d.SourceType, &d.SourceURL, &d.SourceID,
			&d.SourceMeta, &d.LastSyncedAt,
			&d.Active, &d.LogicalID, &d.ContentHash,
			&d.CreatedBy, &d.CreatedAt, &d.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

// contentHash computes a SHA-256 hex digest of the document content.
func contentHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// Create inserts a new reference document with active=true and a fresh logical_id.
func (s *ReferenceDocumentStore) Create(ctx context.Context, doc *models.ReferenceDocument) error {
	if doc.SourceType == "" {
		doc.SourceType = models.ReferenceDocSourceManual
	}
	doc.ContentHash = contentHash(doc.Content)

	query := `
		INSERT INTO reference_documents (
			org_id, title, content, doc_type, sort_order,
			source_type, source_url, source_id, source_meta, last_synced_at,
			active, content_hash,
			created_by
		) VALUES (
			@org_id, @title, @content, @doc_type, @sort_order,
			@source_type, @source_url, @source_id, @source_meta, @last_synced_at,
			true, @content_hash,
			@created_by
		) RETURNING id, logical_id, created_at, updated_at`

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":         doc.OrgID,
		"title":          doc.Title,
		"content":        doc.Content,
		"doc_type":       doc.DocType,
		"sort_order":     doc.SortOrder,
		"source_type":    doc.SourceType,
		"source_url":     doc.SourceURL,
		"source_id":      doc.SourceID,
		"source_meta":    doc.SourceMeta,
		"last_synced_at": doc.LastSyncedAt,
		"content_hash":   doc.ContentHash,
		"created_by":     doc.CreatedBy,
	})
	doc.Active = true
	return row.Scan(&doc.ID, &doc.LogicalID, &doc.CreatedAt, &doc.UpdatedAt)
}

// ListByOrg returns all active documents for an org.
func (s *ReferenceDocumentStore) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.ReferenceDocument, error) {
	query := fmt.Sprintf(`SELECT %s FROM reference_documents
		WHERE org_id = @org_id AND active = true
		ORDER BY sort_order ASC, created_at ASC`, referenceDocumentColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id": orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("query reference documents: %w", err)
	}
	defer rows.Close()
	return scanReferenceDocs(rows)
}

// GetByID returns a document by ID (any version, active or not).
// This is needed for version history inspection and pin member lookups.
func (s *ReferenceDocumentStore) GetByID(ctx context.Context, orgID, docID uuid.UUID) (models.ReferenceDocument, error) {
	query := fmt.Sprintf(`SELECT %s FROM reference_documents WHERE id = @id AND org_id = @org_id`, referenceDocumentColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     docID,
		"org_id": orgID,
	})
	return scanReferenceDoc(row)
}

// Update replaces the current active version with a new row using the
// insert-only versioning pattern. The old row is deactivated and a new row
// is inserted in a single transaction. If no fields changed, this is a no-op.
//
// The doc parameter is updated in-place with the new row's ID, timestamps,
// and logical_id (carried forward from the previous version).
func (s *ReferenceDocumentStore) Update(ctx context.Context, doc *models.ReferenceDocument) error {
	newHash := contentHash(doc.Content)

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Fetch the current active row (with row lock) to check for changes.
	// The handler reads the doc outside this transaction, so we verify the
	// row the handler read is still the active one (guards against concurrent updates).
	current, err := scanReferenceDoc(tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT %s FROM reference_documents WHERE id = @id AND org_id = @org_id AND active = true FOR UPDATE`, referenceDocumentColumns),
		pgx.NamedArgs{"id": doc.ID, "org_id": doc.OrgID},
	))
	if err != nil {
		return fmt.Errorf("fetch current version (row may have been updated concurrently): %w", err)
	}

	// Skip insert if nothing changed across all mutable fields.
	// NOTE: If you add a new mutable field to ReferenceDocument, add it to this check.
	if newHash == current.ContentHash &&
		doc.Title == current.Title &&
		doc.DocType == current.DocType &&
		doc.SortOrder == current.SortOrder &&
		doc.SourceType == current.SourceType &&
		stringPtrEq(doc.SourceURL, current.SourceURL) &&
		stringPtrEq(doc.SourceID, current.SourceID) &&
		bytes.Equal(doc.SourceMeta, current.SourceMeta) &&
		timePtrEq(doc.LastSyncedAt, current.LastSyncedAt) {
		return tx.Commit(ctx)
	}

	// 2. Deactivate the current active row.
	_, err = tx.Exec(ctx,
		`UPDATE reference_documents SET active = false WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": doc.ID, "org_id": doc.OrgID},
	)
	if err != nil {
		return fmt.Errorf("deactivate current version: %w", err)
	}

	// 3. Insert new active version with the same logical_id.
	row := tx.QueryRow(ctx, fmt.Sprintf(`
		INSERT INTO reference_documents (
			org_id, title, content, doc_type, sort_order,
			source_type, source_url, source_id, source_meta, last_synced_at,
			active, logical_id, content_hash,
			created_by
		) VALUES (
			@org_id, @title, @content, @doc_type, @sort_order,
			@source_type, @source_url, @source_id, @source_meta, @last_synced_at,
			true, @logical_id, @content_hash,
			@created_by
		) RETURNING %s`, referenceDocumentColumns),
		pgx.NamedArgs{
			"org_id":         doc.OrgID,
			"title":          doc.Title,
			"content":        doc.Content,
			"doc_type":       doc.DocType,
			"sort_order":     doc.SortOrder,
			"source_type":    doc.SourceType,
			"source_url":     doc.SourceURL,
			"source_id":      doc.SourceID,
			"source_meta":    doc.SourceMeta,
			"last_synced_at": doc.LastSyncedAt,
			"logical_id":     current.LogicalID,
			"content_hash":   newHash,
			"created_by":     doc.CreatedBy,
		},
	)

	updated, err := scanReferenceDoc(row)
	if err != nil {
		return fmt.Errorf("insert new version: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	// Update the doc in-place with the new row's fields.
	*doc = updated
	return nil
}

// stringPtrEq compares two *string values for equality.
func stringPtrEq(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// timePtrEq compares two *time.Time values for equality.
func timePtrEq(a, b *time.Time) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Equal(*b)
}

// Delete soft-deletes a document by setting active=false. The row is preserved
// for version history and pin reference integrity.
func (s *ReferenceDocumentStore) Delete(ctx context.Context, orgID, docID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE reference_documents SET active = false WHERE id = @id AND org_id = @org_id AND active = true`,
		pgx.NamedArgs{"id": docID, "org_id": orgID},
	)
	return err
}

// GetActiveByLogicalID returns the current active version for a given logical_id.
func (s *ReferenceDocumentStore) GetActiveByLogicalID(ctx context.Context, orgID, logicalID uuid.UUID) (models.ReferenceDocument, error) {
	query := fmt.Sprintf(`SELECT %s FROM reference_documents WHERE org_id = @org_id AND logical_id = @logical_id AND active = true`, referenceDocumentColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"logical_id": logicalID,
	})
	return scanReferenceDoc(row)
}

// ListVersions returns all versions of a document (active and inactive),
// ordered newest first. The logical_id is resolved from the provided doc ID.
// Limit caps the number of results (0 defaults to 100).
func (s *ReferenceDocumentStore) ListVersions(ctx context.Context, orgID, docID uuid.UUID, limit int) ([]models.ReferenceDocument, error) {
	if limit <= 0 {
		limit = 100
	}
	query := fmt.Sprintf(`SELECT %s FROM reference_documents
		WHERE org_id = @org_id AND logical_id = (
			SELECT logical_id FROM reference_documents WHERE id = @id AND org_id = @org_id
		)
		ORDER BY created_at DESC
		LIMIT @limit`, referenceDocumentColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id": orgID,
		"id":     docID,
		"limit":  limit,
	})
	if err != nil {
		return nil, fmt.Errorf("query reference document versions: %w", err)
	}
	defer rows.Close()
	return scanReferenceDocs(rows)
}

// Restore creates a new active version with the content from an old version.
// This follows the standard insert-only pattern: deactivate current + insert new.
func (s *ReferenceDocumentStore) Restore(ctx context.Context, orgID, currentDocID, restoreFromID uuid.UUID) (models.ReferenceDocument, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return models.ReferenceDocument{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Fetch the version to restore from.
	oldDoc, err := scanReferenceDoc(tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT %s FROM reference_documents WHERE id = @id AND org_id = @org_id`, referenceDocumentColumns),
		pgx.NamedArgs{"id": restoreFromID, "org_id": orgID},
	))
	if err != nil {
		return models.ReferenceDocument{}, fmt.Errorf("fetch restore source: %w", err)
	}

	// Deactivate the current active version.
	var logicalID uuid.UUID
	err = tx.QueryRow(ctx,
		`UPDATE reference_documents SET active = false
		 WHERE id = @id AND org_id = @org_id AND active = true
		 RETURNING logical_id`,
		pgx.NamedArgs{"id": currentDocID, "org_id": orgID},
	).Scan(&logicalID)
	if err != nil {
		return models.ReferenceDocument{}, fmt.Errorf("deactivate current version: %w", err)
	}

	// Verify both docs belong to the same logical document.
	if oldDoc.LogicalID != logicalID {
		return models.ReferenceDocument{}, fmt.Errorf("restore source belongs to a different logical document")
	}

	// Insert new active version with old content.
	row := tx.QueryRow(ctx, fmt.Sprintf(`
		INSERT INTO reference_documents (
			org_id, title, content, doc_type, sort_order,
			source_type, source_url, source_id, source_meta, last_synced_at,
			active, logical_id, content_hash,
			created_by
		) VALUES (
			@org_id, @title, @content, @doc_type, @sort_order,
			@source_type, @source_url, @source_id, @source_meta, @last_synced_at,
			true, @logical_id, @content_hash,
			@created_by
		) RETURNING %s`, referenceDocumentColumns),
		pgx.NamedArgs{
			"org_id":         oldDoc.OrgID,
			"title":          oldDoc.Title,
			"content":        oldDoc.Content,
			"doc_type":       oldDoc.DocType,
			"sort_order":     oldDoc.SortOrder,
			"source_type":    oldDoc.SourceType,
			"source_url":     oldDoc.SourceURL,
			"source_id":      oldDoc.SourceID,
			"source_meta":    oldDoc.SourceMeta,
			"last_synced_at": oldDoc.LastSyncedAt,
			"logical_id":     logicalID,
			"content_hash":   contentHash(oldDoc.Content),
			"created_by":     oldDoc.CreatedBy,
		},
	)

	restored, err := scanReferenceDoc(row)
	if err != nil {
		return models.ReferenceDocument{}, fmt.Errorf("insert restored version: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return models.ReferenceDocument{}, fmt.Errorf("commit transaction: %w", err)
	}

	return restored, nil
}

// --- Document Set Pins ---

// CreateDocumentSetPin captures the current active document row IDs as a pin.
func (s *ReferenceDocumentStore) CreateDocumentSetPin(ctx context.Context, orgID uuid.UUID) (models.ReferenceContextSetPin, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return models.ReferenceContextSetPin{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Create the pin.
	var pin models.ReferenceContextSetPin
	err = tx.QueryRow(ctx,
		`INSERT INTO reference_context_set_pins (org_id) VALUES (@org_id) RETURNING id, org_id, created_at`,
		pgx.NamedArgs{"org_id": orgID},
	).Scan(&pin.ID, &pin.OrgID, &pin.CreatedAt)
	if err != nil {
		return models.ReferenceContextSetPin{}, fmt.Errorf("create pin: %w", err)
	}

	// Capture all currently active document IDs as pin members.
	_, err = tx.Exec(ctx,
		`INSERT INTO reference_context_set_pin_members (pin_id, reference_document_id)
		 SELECT @pin_id, id FROM reference_documents WHERE org_id = @org_id AND active = true`,
		pgx.NamedArgs{"pin_id": pin.ID, "org_id": orgID},
	)
	if err != nil {
		return models.ReferenceContextSetPin{}, fmt.Errorf("create pin members: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return models.ReferenceContextSetPin{}, fmt.Errorf("commit transaction: %w", err)
	}

	return pin, nil
}

// GetDocumentSetPin returns a pin by ID.
func (s *ReferenceDocumentStore) GetDocumentSetPin(ctx context.Context, orgID, pinID uuid.UUID) (models.ReferenceContextSetPin, error) {
	var pin models.ReferenceContextSetPin
	err := s.db.QueryRow(ctx,
		`SELECT id, org_id, created_at FROM reference_context_set_pins WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": pinID, "org_id": orgID},
	).Scan(&pin.ID, &pin.OrgID, &pin.CreatedAt)
	return pin, err
}

// ListDocumentSetPins returns all pins for an org, newest first.
// Limit caps the number of results (0 defaults to 100).
func (s *ReferenceDocumentStore) ListDocumentSetPins(ctx context.Context, orgID uuid.UUID, limit int) ([]models.ReferenceContextSetPin, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(ctx,
		`SELECT id, org_id, created_at FROM reference_context_set_pins WHERE org_id = @org_id ORDER BY created_at DESC LIMIT @limit`,
		pgx.NamedArgs{"org_id": orgID, "limit": limit},
	)
	if err != nil {
		return nil, fmt.Errorf("query document set pins: %w", err)
	}
	defer rows.Close()

	var pins []models.ReferenceContextSetPin
	for rows.Next() {
		var p models.ReferenceContextSetPin
		if err := rows.Scan(&p.ID, &p.OrgID, &p.CreatedAt); err != nil {
			return nil, err
		}
		pins = append(pins, p)
	}
	return pins, rows.Err()
}

// GetPinMembers returns all document versions that belong to a pin.
func (s *ReferenceDocumentStore) GetPinMembers(ctx context.Context, orgID, pinID uuid.UUID) ([]models.ReferenceDocument, error) {
	query := fmt.Sprintf(`SELECT %s FROM reference_documents d
		INNER JOIN reference_context_set_pin_members m ON d.id = m.reference_document_id
		WHERE m.pin_id = @pin_id AND d.org_id = @org_id
		ORDER BY d.sort_order ASC, d.created_at ASC`, referenceDocumentColumnsQualified())
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"pin_id": pinID,
		"org_id": orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("query pin members: %w", err)
	}
	defer rows.Close()
	return scanReferenceDocs(rows)
}

// referenceDocumentColumnsQualified returns the column list with "d." table alias prefix.
func referenceDocumentColumnsQualified() string {
	return `d.id, d.org_id, d.title, d.content, d.doc_type, d.sort_order,
	d.source_type, d.source_url, d.source_id, d.source_meta, d.last_synced_at,
	d.active, d.logical_id, d.content_hash,
	d.created_by, d.created_at, d.updated_at`
}
