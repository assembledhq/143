package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type VerifiedDomainStatus string

const (
	VerifiedDomainStatusPending  VerifiedDomainStatus = "pending"
	VerifiedDomainStatusVerified VerifiedDomainStatus = "verified"
)

func (s VerifiedDomainStatus) Validate() error {
	switch s {
	case VerifiedDomainStatusPending, VerifiedDomainStatusVerified:
		return nil
	default:
		return fmt.Errorf("invalid verified domain status: %s", s)
	}
}

type VerifiedDomain struct {
	ID                 uuid.UUID            `db:"id" json:"id"`
	OrgID              uuid.UUID            `db:"org_id" json:"org_id"`
	Domain             string               `db:"domain" json:"domain"`
	Status             VerifiedDomainStatus `db:"status" json:"status"`
	VerificationToken  string               `db:"verification_token" json:"verification_token"`
	VerifiedAt         *time.Time           `db:"verified_at" json:"verified_at,omitempty"`
	AutoJoinEnabled    bool                 `db:"auto_join_enabled" json:"auto_join_enabled"`
	AutoJoinRole       Role                 `db:"auto_join_role" json:"auto_join_role"`
	CreatedBy          uuid.UUID            `db:"created_by" json:"created_by"`
	CreatedAt          time.Time            `db:"created_at" json:"created_at"`
	UpdatedAt          time.Time            `db:"updated_at" json:"updated_at"`
	VerificationHost   string               `db:"-" json:"verification_host"`
	VerificationRecord string               `db:"-" json:"verification_record"`
}
