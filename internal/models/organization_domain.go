package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// OrgDomainStatus is the verification state of an organization's claimed
// email domain. There is no "failed" state: a failed DNS check leaves the
// row pending so the admin can fix the record and retry.
type OrgDomainStatus string

const (
	OrgDomainStatusPending  OrgDomainStatus = "pending"
	OrgDomainStatusVerified OrgDomainStatus = "verified"
)

func (s OrgDomainStatus) Validate() error {
	switch s {
	case OrgDomainStatusPending, OrgDomainStatusVerified:
		return nil
	default:
		return fmt.Errorf("invalid OrgDomainStatus: %q", s)
	}
}

// OrganizationDomain is a DB row for a domain an org has claimed (and
// possibly verified) for email-domain auto-join.
//
// VerificationToken is included in JSON because admins need it to construct
// the DNS TXT record; once published it is world-readable in DNS anyway, so
// it is not treated as a secret. The admin domain endpoints are themselves
// admin-gated.
type OrganizationDomain struct {
	ID                uuid.UUID       `db:"id"                 json:"id"`
	OrgID             uuid.UUID       `db:"org_id"             json:"org_id"`
	Domain            string          `db:"domain"             json:"domain"`
	VerificationToken string          `db:"verification_token" json:"verification_token"`
	Status            OrgDomainStatus `db:"status"             json:"status"`
	AutoJoinEnabled   bool            `db:"auto_join_enabled"  json:"auto_join_enabled"`
	CreatedBy         *uuid.UUID      `db:"created_by"         json:"-"`
	CreatedAt         time.Time       `db:"created_at"         json:"created_at"`
	VerifiedAt        *time.Time      `db:"verified_at"        json:"verified_at,omitempty"`
	LastCheckedAt     *time.Time      `db:"last_checked_at"    json:"last_checked_at,omitempty"`
	// FailedChecks counts consecutive failed daily TXT re-checks of a
	// verified domain. The sweep disables auto-join at
	// MaxDomainRecheckFailures; any successful check resets it.
	FailedChecks int `db:"failed_checks" json:"failed_checks"`
}

// MaxDomainRecheckFailures is how many consecutive daily re-check failures
// a verified domain survives before the sweep turns auto-join off. Three
// days tolerates DNS-provider blips and weekend zone migrations while
// bounding the window in which an expired/transferred domain keeps
// admitting new members.
const MaxDomainRecheckFailures = 3

// EmailVerificationToken is a single-use proof-of-address token mailed to
// password-signup users. Email is snapshotted at issue time so a token
// can't verify an address the account no longer holds.
type EmailVerificationToken struct {
	ID         uuid.UUID  `db:"id"          json:"id"`
	UserID     uuid.UUID  `db:"user_id"     json:"user_id"`
	Email      string     `db:"email"       json:"email"`
	Token      string     `db:"token"       json:"-"`
	ExpiresAt  time.Time  `db:"expires_at"  json:"expires_at"`
	ConsumedAt *time.Time `db:"consumed_at" json:"consumed_at,omitempty"`
	CreatedAt  time.Time  `db:"created_at"  json:"created_at"`
}

// JoinableOrganization is one entry in the "workspaces you can join" surface:
// an org whose verified, auto-join-enabled domain matches the requesting
// user's (provider-verified) email domain and where the user is not already
// a member.
type JoinableOrganization struct {
	OrgID   uuid.UUID `db:"org_id"   json:"org_id"`
	OrgName string    `db:"org_name" json:"org_name"`
	Domain  string    `db:"domain"   json:"domain"`
}
