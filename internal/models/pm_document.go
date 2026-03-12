package models

import (
	"time"

	"github.com/google/uuid"
)

// PMDocument is an org-level document (roadmap, product philosophy, etc.)
// that provides context to the PM agent during analysis.
type PMDocument struct {
	ID        uuid.UUID  `db:"id" json:"id"`
	OrgID     uuid.UUID  `db:"org_id" json:"org_id"`
	Title     string     `db:"title" json:"title"`
	Content   string     `db:"content" json:"content"`
	DocType   string     `db:"doc_type" json:"doc_type"`
	SortOrder int        `db:"sort_order" json:"sort_order"`
	CreatedBy *uuid.UUID `db:"created_by" json:"created_by,omitempty"`
	CreatedAt time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt time.Time  `db:"updated_at" json:"updated_at"`
}
