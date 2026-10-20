package db

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// exportStatus always rechecks tenant/repository ownership. The optional suffix
// is a constant chosen by the caller, never client SQL.
func exportStatus(ctx context.Context, database DBTX, orgID, repositoryID, id uuid.UUID, lock string) (models.CodeReviewExportStatus, error) {
	var status models.CodeReviewExportStatus
	a := &status.Acquisition
	var filters, counts, receipt []byte
	var releasedAt *time.Time
	var now time.Time
	err := database.QueryRow(ctx, `SELECT e.id,e.schema_version,e.filters,e.acquired_at,e.expires_at,e.counts,e.released_at,e.release_receipt,clock_timestamp()
 FROM code_review_exports e JOIN repositories r ON r.org_id=e.org_id AND r.id=e.repository_id
 WHERE e.org_id=$1 AND e.id=$2 AND ($3::uuid='00000000-0000-0000-0000-000000000000'::uuid OR e.repository_id=$3)`+lock, orgID, id, repositoryID).Scan(&a.ID, &a.SchemaVersion, &filters, &a.AcquiredAt, &a.ExpiresAt, &counts, &releasedAt, &receipt, &now)
	if errors.Is(err, pgx.ErrNoRows) {
		return status, ErrReviewExportExpired
	}
	if err != nil {
		return status, err
	}
	if err = json.Unmarshal(filters, &a.Filters); err != nil {
		return status, err
	}
	if err = json.Unmarshal(counts, &a.Counts); err != nil {
		return status, err
	}
	a.Consistency = models.ReviewExportFrozenPayloads
	status.State = models.ReviewExportActive
	if !a.ExpiresAt.After(now) {
		status.State = models.ReviewExportExpired
	}
	if releasedAt != nil {
		status.State = models.ReviewExportReleased
		if err = json.Unmarshal(receipt, &status.ReleaseReceipt); err != nil {
			return status, err
		}
	}
	return status, nil
}

func exportReadable(status models.CodeReviewExportStatus) error {
	switch status.State {
	case models.ReviewExportReleased:
		return ErrReviewExportReleased
	case models.ReviewExportExpired:
		return ErrReviewExportExpired
	}
	return nil
}

func (s *CodeReviewExportStore) Status(ctx context.Context, orgID, repositoryID, id uuid.UUID) (models.CodeReviewExportStatus, error) {
	if orgID == uuid.Nil || repositoryID == uuid.Nil || id == uuid.Nil {
		return models.CodeReviewExportStatus{}, ErrReviewExportExpired
	}
	return exportStatus(ctx, s.db, orgID, repositoryID, id, "")
}

// Release frees only the named acquisition's cache. Durable metadata and the
// original archive attestation survive retries, expiry and future acquisitions.
func (s *CodeReviewExportStore) Release(ctx context.Context, orgID, repositoryID, id uuid.UUID, proof models.CodeReviewExportArchiveProof) (models.CodeReviewExportStatus, error) {
	if orgID == uuid.Nil || repositoryID == uuid.Nil || id == uuid.Nil {
		return models.CodeReviewExportStatus{}, ErrReviewExportExpired
	}
	if err := proof.Validate(); err != nil {
		return models.CodeReviewExportStatus{}, ErrReviewExportArchive
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return models.CodeReviewExportStatus{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout = '30s'`); err != nil {
		return models.CodeReviewExportStatus{}, err
	}
	status, err := exportStatus(ctx, tx, orgID, repositoryID, id, " FOR UPDATE OF e")
	if err != nil {
		return status, err
	}
	if status.ReleaseReceipt != nil {
		if !reflect.DeepEqual(status.ReleaseReceipt.Archive, proof) {
			return status, ErrReviewExportReleaseConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return status, err
		}
		return status, nil
	}
	if *proof.ArchivedRecords != status.Acquisition.Counts.Records {
		return status, ErrReviewExportArchive
	}
	tag, err := tx.Exec(ctx, `DELETE FROM code_review_export_rows WHERE org_id=$1 AND export_id=$2`, orgID, id)
	if err != nil {
		return status, err
	}
	var releasedAt time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&releasedAt); err != nil {
		return status, err
	}
	receipt := models.CodeReviewExportReleaseReceipt{AcquisitionID: id, ReleasedAt: releasedAt, PayloadRowsDeleted: tag.RowsAffected(), Archive: proof}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return status, err
	}
	_, err = tx.Exec(ctx, `UPDATE code_review_exports SET slot=NULL,released_at=$4,release_receipt=$5 WHERE org_id=$1 AND repository_id=$2 AND id=$3`, orgID, repositoryID, id, releasedAt, raw)
	if err != nil {
		return status, err
	}
	if err = tx.Commit(ctx); err != nil {
		return status, err
	}
	status.State, status.ReleaseReceipt = models.ReviewExportReleased, &receipt
	return status, nil
}
