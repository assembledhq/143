package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Repair reservations do not yet carry durable scope or owning-attempt proof.
// Keep this distinguishable for the ordinary admission's waiting reason while
// retaining the canonical consumer-reconciliation classification for callers.
var errLifecycleRepairConsumerReconciliation = fmt.Errorf("%w: repair scope requires typed ownership proof", ErrLifecycleConsumerReconciliation)

const lifecycleConsumerRetainedJobRoutesQuery = `SELECT id,octet_length(payload::text),
 CASE WHEN octet_length(payload::text)<=$3 THEN payload ELSE NULL END
 FROM jobs WHERE org_id=$1 AND id=ANY($2::uuid[]) ORDER BY id`

// readRetainedLifecycleRepairJobsInTx observes only exact job references in the
// already bounded structured evidence set. It does not scan repair history or
// restrict the jobs by status: a terminal job cannot prove that its retained
// runtime/executor/physical operation drained. Released history without a live
// consumer reference supplies no candidate here. No receipt/claim is granted.
func readRetainedLifecycleRepairJobsInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, evidence []lifecycleConsumerEvidence, limits lifecycleConsumerLimits) (map[uuid.UUID]bool, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	var ids []uuid.UUID
	for _, v := range evidence {
		if v.jobID != nil && v.foundJobID != nil && *v.jobID == *v.foundJobID {
			ids = append(ids, *v.jobID)
		}
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	ids = slices.Compact(ids)
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if len(ids) == 0 {
		return nil, 0, nil
	}
	if len(ids) > limits.structuredRows {
		return nil, 0, &lifecycleConsumerError{cause: ErrLifecycleConsumerDiscoveryIncomplete, source: lifecycleConsumerLegacyJob}
	}
	rows, err := tx.Query(ctx, lifecycleConsumerRetainedJobRoutesQuery, pgx.QueryExecModeExec, orgID, lifecycleConsumerUUIDArray(ids), legacyJobRouteMaxBytes)
	if err != nil {
		return nil, 0, fmt.Errorf("read exact retained lifecycle job routes: %w", err)
	}
	// Keep route parsing out of the active statement, just as the ordinary
	// legacy reader does. pgx copies *[]byte directly instead of validating
	// json.RawMessage while PostgreSQL is waiting for the client to consume it.
	var payloads []lifecycleLegacyConsumerPayload
	var scanErr error
	var incomplete *lifecycleConsumerError
	var payloadBytes int
	for rows.Next() {
		var id uuid.UUID
		var size int
		var payload []byte
		if err := rows.Scan(&id, &size, &payload); err != nil {
			scanErr = fmt.Errorf("decode retained lifecycle job route: %w", err)
			break
		}
		if len(payloads) >= limits.structuredRows || size < 0 || size > legacyJobRouteMaxBytes || size > limits.payloadBytes-payloadBytes {
			incomplete = &lifecycleConsumerError{cause: ErrLifecycleConsumerDiscoveryIncomplete, source: lifecycleConsumerLegacyJob, id: id}
			break
		}
		payloadBytes += size
		payloads = append(payloads, lifecycleLegacyConsumerPayload{id, payload})
	}
	streamErr := rows.Err()
	rows.Close()
	if err := errors.Join(scanErr, streamErr, rows.Err(), ctx.Err()); err != nil {
		return nil, 0, fmt.Errorf("collect retained lifecycle job routes: %w", err)
	}
	if incomplete != nil {
		return nil, 0, incomplete
	}
	repairs := make(map[uuid.UUID]bool)
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, raw := range payloads {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if !slices.Contains(ids, raw.id) || seen[raw.id] {
			return nil, 0, reconcileLifecycleConsumer(lifecycleConsumerLegacyJob, raw.id)
		}
		seen[raw.id] = true
		route := decodeLegacyJobRoute(orgID, raw.payload)
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if route.incomplete() {
			return nil, 0, &lifecycleConsumerError{cause: ErrLifecycleConsumerDiscoveryIncomplete, source: lifecycleConsumerLegacyJob, id: raw.id}
		}
		if route.repairScopeUnproved {
			repairs[raw.id] = true
		}
	}
	for _, id := range ids {
		if !seen[id] {
			return nil, 0, reconcileLifecycleConsumer(lifecycleConsumerLegacyJob, id)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	return repairs, payloadBytes, nil
}
