package db

import (
	"context"
	"fmt"
)

// CallerSendWakeStore repairs notifications from durable due owners. Existing
// pending/running jobs suppress duplicates; historical jobs grant no ownership.
type CallerSendWakeStore struct{ db DBTX }

func NewCallerSendWakeStore(q DBTX) *CallerSendWakeStore { return &CallerSendWakeStore{db: q} }

const repairReviewerSendWakesSQL = `WITH due AS MATERIALIZED (
 SELECT r.org_id,r.dispatch_wake FROM code_review_agent_results r
 WHERE r.dispatch_due_at IS NOT NULL AND r.dispatch_due_at<=statement_timestamp()
 AND r.dispatch_version=1 AND r.dispatch_wake->>'job_type'='run_code_review' AND r.dispatch_wake->>'queue'='agent'
 AND jsonb_typeof(r.dispatch_wake->'payload')='object' AND r.dispatch_wake->'payload'->>'org_id'=r.org_id::text
 AND r.dispatch_wake->'payload'->>'session_id'=r.session_id::text
 AND r.dispatch_wake->>'dedupe_key'='code_review:'||(r.dispatch_wake->'payload'->>'review_output_key')
 AND length(r.dispatch_wake->'payload'->>'review_output_key')>0
 AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.org_id=r.org_id AND j.queue='agent'
 AND j.dedupe_key=r.dispatch_wake->>'dedupe_key' AND j.status IN ('pending','running'))
 ORDER BY r.dispatch_due_at,r.id LIMIT 100
), wakes AS (
 SELECT DISTINCT ON (org_id,dispatch_wake->>'dedupe_key') org_id,dispatch_wake
 FROM due ORDER BY org_id,dispatch_wake->>'dedupe_key'
)
 INSERT INTO jobs(org_id,queue,job_type,payload,priority,dedupe_key,max_attempts)
 SELECT org_id,'agent','run_code_review',dispatch_wake->'payload',5,dispatch_wake->>'dedupe_key',8 FROM wakes
 ON CONFLICT DO NOTHING`

const repairLoopStartupWakesSQL = `WITH due AS MATERIALIZED (
 SELECT l.org_id,l.startup_send_thread_id AS thread_id FROM session_review_loops l
 WHERE l.startup_send_due_at IS NOT NULL AND l.startup_send_due_at<=statement_timestamp()
 AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.org_id=l.org_id AND j.queue='agent'
 AND j.dedupe_key='reconcile_session_review_loop:'||l.startup_send_thread_id::text AND j.status IN ('pending','running'))
 ORDER BY l.startup_send_due_at,l.id LIMIT 100
), wakes AS (SELECT DISTINCT ON(org_id,thread_id) org_id,thread_id FROM due ORDER BY org_id,thread_id)
 INSERT INTO jobs(org_id,queue,job_type,payload,priority,dedupe_key,max_attempts)
 SELECT org_id,'agent','reconcile_session_review_loop',jsonb_build_object('org_id',org_id,'limit',50),5,
 'reconcile_session_review_loop:'||thread_id::text,8 FROM wakes ON CONFLICT DO NOTHING`

const repairPreparedStartWakesSQL = `WITH due AS MATERIALIZED (
 SELECT s.org_id,s.thread_id FROM review_loop_start_intents s
 WHERE s.due_at IS NOT NULL AND s.due_at<=statement_timestamp()
 AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.org_id=s.org_id AND j.queue='agent'
 AND j.dedupe_key='reconcile_session_review_loop:'||s.thread_id::text AND j.status IN ('pending','running'))
 ORDER BY s.due_at,s.id LIMIT 100
), wakes AS (SELECT DISTINCT ON(org_id,thread_id) org_id,thread_id FROM due ORDER BY org_id,thread_id)
 INSERT INTO jobs(org_id,queue,job_type,payload,priority,dedupe_key,max_attempts)
 SELECT org_id,'agent','reconcile_session_review_loop',jsonb_build_object('org_id',org_id,'limit',50),5,
 'reconcile_session_review_loop:'||thread_id::text,8 FROM wakes ON CONFLICT DO NOTHING`

// RepairMissingWakes runs each fixed indexed due selector independently. It
// neither enables intents nor changes owner state or sends a message.
// lint:allow-no-orgid reason="system scheduler restores retained caller wakes across tenants in three bounded due batches"
func (s *CallerSendWakeStore) RepairMissingWakes(ctx context.Context) error {
	for _, query := range []struct{ name, sql string }{{"reviewer", repairReviewerSendWakesSQL}, {"startup", repairLoopStartupWakesSQL}, {"creation", repairPreparedStartWakesSQL}} {
		if _, err := s.db.Exec(ctx, query.sql); err != nil {
			return fmt.Errorf("repair retained %s wake: %w", query.name, err)
		}
	}
	return nil
}
