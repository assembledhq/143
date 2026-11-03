package models

import (
	"github.com/google/uuid"
	"time"
)

type PackageRegistryCredentialSummary struct {
	ID           uuid.UUID `json:"id" db:"id"`
	RepositoryID uuid.UUID `json:"repository_id" db:"repository_id"`
	RegistryURL  string    `json:"registry_url" db:"registry_url"`
	PackageScope string    `json:"package_scope" db:"package_scope"`
	Revision     uuid.UUID `json:"revision" db:"revision"`
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" db:"updated_at"`
}

type ResolvedPackageRegistryCredential struct {
	Metadata PackageRegistryCredentialSummary `json:"-"`
	Token    string                           `json:"-"`
}

type PackageRegistryCredential struct {
	PackageRegistryCredentialSummary
	OrgID          uuid.UUID `json:"-" db:"org_id"`
	TokenEncrypted []byte    `json:"-" db:"token_encrypted"`
}
