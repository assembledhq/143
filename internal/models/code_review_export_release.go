package models

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ReviewExportState string

const (
	ReviewExportActive   ReviewExportState = "active"
	ReviewExportReleased ReviewExportState = "released"
	ReviewExportExpired  ReviewExportState = "expired"
)

func (v ReviewExportState) Validate() error {
	switch v {
	case ReviewExportActive, ReviewExportReleased, ReviewExportExpired:
		return nil
	}
	return fmt.Errorf("invalid export state %q", v)
}

// CodeReviewExportArchiveProof is an operator attestation, not server verification
// of local storage. The manifest binds the downloaded bytes and cursor chain.
type CodeReviewExportArchiveProof struct {
	ManifestSHA256  string `json:"manifest_sha256"`
	ArchivedRecords *int64 `json:"archived_records"`
	FinalPosition   *int64 `json:"final_position"`
	Verified        bool   `json:"verified"`
}

func (p CodeReviewExportArchiveProof) Validate() error {
	digest, err := hex.DecodeString(p.ManifestSHA256)
	if err != nil || len(digest) != 32 || strings.ToLower(p.ManifestSHA256) != p.ManifestSHA256 {
		return fmt.Errorf("manifest_sha256 must be a lowercase SHA-256 hex digest")
	}
	if !p.Verified || p.ArchivedRecords == nil || p.FinalPosition == nil || *p.ArchivedRecords < 0 || *p.ArchivedRecords != *p.FinalPosition {
		return fmt.Errorf("verified archive record count and final cursor position are required and must agree")
	}
	return nil
}

type CodeReviewExportReleaseRequest struct {
	RepositoryID uuid.UUID                    `json:"repository_id"`
	Archive      CodeReviewExportArchiveProof `json:"archive"`
}

type CodeReviewExportReleaseReceipt struct {
	AcquisitionID      uuid.UUID                    `json:"acquisition_id"`
	ReleasedAt         time.Time                    `json:"released_at"`
	PayloadRowsDeleted int64                        `json:"payload_rows_deleted"`
	Archive            CodeReviewExportArchiveProof `json:"archive"`
}

type CodeReviewExportStatus struct {
	Acquisition    CodeReviewExportAcquisition     `json:"acquisition"`
	State          ReviewExportState               `json:"state"`
	ReleaseReceipt *CodeReviewExportReleaseReceipt `json:"release_receipt"`
}
