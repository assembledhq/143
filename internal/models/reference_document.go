package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ReferenceDocument is an org-level document (roadmap, product philosophy, etc.)
// that provides reproducible reference context.
//
// Documents always store a local copy of the text content so evaluations
// can read them without network access. The Source* fields track where the
// document originally came from so it can be re-synced or linked back.
type ReferenceDocument struct {
	ID        uuid.UUID `db:"id" json:"id"`
	OrgID     uuid.UUID `db:"org_id" json:"org_id"`
	Title     string    `db:"title" json:"title"`
	Content   string    `db:"content" json:"content"`
	DocType   string    `db:"doc_type" json:"doc_type"`
	SortOrder int       `db:"sort_order" json:"sort_order"`

	// Source provenance fields.
	SourceType   string          `db:"source_type" json:"source_type"`
	SourceURL    *string         `db:"source_url" json:"source_url,omitempty"`
	SourceID     *string         `db:"source_id" json:"source_id,omitempty"`
	SourceMeta   json.RawMessage `db:"source_meta" json:"source_meta,omitempty"`
	LastSyncedAt *time.Time      `db:"last_synced_at" json:"last_synced_at,omitempty"`

	// Insert-only versioning fields (same pattern as memories).
	Active      bool      `db:"active" json:"active"`
	LogicalID   uuid.UUID `db:"logical_id" json:"logical_id"`
	ContentHash string    `db:"content_hash" json:"content_hash"`

	CreatedBy *uuid.UUID `db:"created_by" json:"created_by,omitempty"`
	CreatedAt time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt time.Time  `db:"updated_at" json:"updated_at"`
}

// ReferenceContextSetPin is a lightweight snapshot of which document versions were
// active at a point in time. Referenced by archived plans and eval tasks.
type ReferenceContextSetPin struct {
	ID        uuid.UUID `db:"id" json:"id"`
	OrgID     uuid.UUID `db:"org_id" json:"org_id"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// ReferenceContextSetPinMember links a pin to a specific document version row.
type ReferenceContextSetPinMember struct {
	PinID               uuid.UUID `db:"pin_id" json:"pin_id"`
	ReferenceDocumentID uuid.UUID `db:"reference_document_id" json:"reference_document_id"`
}

// Common reference document types.
const (
	ReferenceDocTypeContext = "context"
)

// Common source types for reference documents.
const (
	ReferenceDocSourceManual     = "manual"      // Pasted directly in the UI
	ReferenceDocSourceURL        = "url"         // Linked from a URL
	ReferenceDocSourceNotion     = "notion"      // Synced from Notion
	ReferenceDocSourceGoogleDocs = "google_docs" // Synced from Google Docs
	ReferenceDocSourceConfluence = "confluence"  // Synced from Confluence
	ReferenceDocSourceFileUpload = "file_upload" // Uploaded as a file (Word, PDF, etc.)
)
