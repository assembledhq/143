package db

import (
	"bytes"
	"context"
	"encoding/json"
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
	var ids []uuid.UUID
	for _, v := range evidence {
		if v.jobID != nil && v.foundJobID != nil && *v.jobID == *v.foundJobID {
			ids = append(ids, *v.jobID)
		}
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	ids = slices.Compact(ids)
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
	defer rows.Close()
	repairs := make(map[uuid.UUID]bool)
	seen := make(map[uuid.UUID]bool, len(ids))
	var count, payloadBytes int
	for rows.Next() {
		var id uuid.UUID
		var size int
		var payload json.RawMessage
		if err := rows.Scan(&id, &size, &payload); err != nil {
			return nil, 0, fmt.Errorf("decode retained lifecycle job route: %w", err)
		}
		count++
		if count > limits.structuredRows || size < 0 || size > legacyJobRouteMaxBytes || size > limits.payloadBytes-payloadBytes {
			return nil, 0, &lifecycleConsumerError{cause: ErrLifecycleConsumerDiscoveryIncomplete, source: lifecycleConsumerLegacyJob, id: id}
		}
		payloadBytes += size
		if !slices.Contains(ids, id) || seen[id] {
			return nil, 0, reconcileLifecycleConsumer(lifecycleConsumerLegacyJob, id)
		}
		seen[id] = true
		route := decodeLegacyJobRoute(orgID, payload)
		if route.incomplete() {
			return nil, 0, &lifecycleConsumerError{cause: ErrLifecycleConsumerDiscoveryIncomplete, source: lifecycleConsumerLegacyJob, id: id}
		}
		if route.repairScopeUnproved {
			repairs[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("collect retained lifecycle job routes: %w", err)
	}
	for _, id := range ids {
		if !seen[id] {
			return nil, 0, reconcileLifecycleConsumer(lifecycleConsumerLegacyJob, id)
		}
	}
	return repairs, payloadBytes, nil
}
