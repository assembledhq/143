package db

import (
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// activityPhaseWritableCTE is prepended to every phase-associated transcript
// insert. The row locks serialize the insert with concurrent phase closure and
// runtime loss: whichever statement obtains the locks first commits first, and
// the waiter rechecks these predicates against the updated rows. Runtime is
// locked before phase to match inbox-acknowledgment transactions and avoid a
// lock-order inversion. This prevents a stale worker from committing output
// after its phase or lease was replaced.
const activityPhaseWritableCTE = `
		WITH locked_activity_runtime AS MATERIALIZED (
		  SELECT r.id
		  FROM thread_runtimes r
		  WHERE r.org_id = @org_id
		    AND r.session_id = @session_id
		    AND r.thread_id = @thread_id
		    AND r.id = CAST(@activity_phase_runtime_id AS uuid)
		    AND r.lease_token = CAST(@activity_phase_lease_token AS uuid)
		    AND r.status IN ('starting', 'live', 'paused', 'draining')
		    AND r.lease_expires_at IS NOT NULL
		    AND r.lease_expires_at > now()
		  FOR UPDATE OF r
		), writable_activity_phase AS MATERIALIZED (
		  SELECT p.id
		  FROM session_activity_phases p
		  WHERE p.id = @activity_phase_id AND p.org_id = @org_id
		    AND p.session_id = @session_id
		    AND p.thread_id IS NOT DISTINCT FROM @thread_id
		    AND p.turn_number = @turn_number
		    AND p.status = 'running'
		    AND ((
		      p.runtime_id IS NULL
		      AND CAST(@activity_phase_runtime_id AS uuid) IS NULL
		      AND CAST(@activity_phase_lease_token AS uuid) IS NULL
		    ) OR EXISTS (
		      SELECT 1 FROM locked_activity_runtime r WHERE r.id = p.runtime_id
		    ))
		  FOR UPDATE OF p
		)`

func addActivityPhaseWriteGuardArgs(args pgx.NamedArgs, guard *models.ActivityPhaseWriteGuard) {
	if guard == nil {
		args["activity_phase_runtime_id"] = nil
		args["activity_phase_lease_token"] = nil
		return
	}
	args["activity_phase_runtime_id"] = guard.RuntimeID
	args["activity_phase_lease_token"] = guard.LeaseToken
}
