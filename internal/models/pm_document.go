package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// PMDocument is an org-level document (roadmap, product philosophy, etc.)
// that provides context to the PM agent during analysis.
//
// Documents always store a local copy of the text content so the PM agent
// can read them without network access. The Source* fields track where the
// document originally came from so it can be re-synced or linked back.
type PMDocument struct {
	ID        uuid.UUID  `db:"id" json:"id"`
	OrgID     uuid.UUID  `db:"org_id" json:"org_id"`
	Title     string     `db:"title" json:"title"`
	Content   string     `db:"content" json:"content"`
	DocType   string     `db:"doc_type" json:"doc_type"`
	SortOrder int        `db:"sort_order" json:"sort_order"`

	// Source provenance fields.
	SourceType   string          `db:"source_type" json:"source_type"`
	SourceURL    *string         `db:"source_url" json:"source_url,omitempty"`
	SourceID     *string         `db:"source_id" json:"source_id,omitempty"`
	SourceMeta   json.RawMessage `db:"source_meta" json:"source_meta,omitempty"`
	LastSyncedAt *time.Time      `db:"last_synced_at" json:"last_synced_at,omitempty"`

	CreatedBy *uuid.UUID `db:"created_by" json:"created_by,omitempty"`
	CreatedAt time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt time.Time  `db:"updated_at" json:"updated_at"`
}

// Common source types for PM documents.
const (
	PMDocSourceManual     = "manual"      // Pasted directly in the UI
	PMDocSourceURL        = "url"         // Linked from a URL
	PMDocSourceNotion     = "notion"      // Synced from Notion
	PMDocSourceGoogleDocs = "google_docs" // Synced from Google Docs
	PMDocSourceConfluence = "confluence"  // Synced from Confluence
	PMDocSourceFileUpload = "file_upload" // Uploaded as a file (Word, PDF, etc.)
)
