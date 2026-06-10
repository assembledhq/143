INSERT INTO eval_bootstrap_candidates (
    org_id,
    bootstrap_run_id,
    session_id,
    thread_id,
    repo_id,
    candidate_index,
    pr_number,
    pr_title,
    base_commit_sha,
    solution_commit_sha,
    solution_diff,
    issue_description,
    scoring_criteria,
    complexity,
    fitness_score,
    fitness_reasoning,
    evidence,
    warnings,
    payload,
    status,
    created_by_tool,
    created_at
)
SELECT
    r.org_id,
    r.id,
    r.session_id,
    r.thread_id,
    r.repo_id,
    candidate.ordinality - 1,
    CASE
        WHEN candidate.payload->>'pr_number' ~ '^[0-9]+$' THEN (candidate.payload->>'pr_number')::integer
        ELSE 0
    END,
    COALESCE(candidate.payload->>'pr_title', ''),
    COALESCE(candidate.payload->>'base_commit_sha', ''),
    COALESCE(candidate.payload->>'solution_commit_sha', ''),
    COALESCE(candidate.payload->>'solution_diff', ''),
    COALESCE(candidate.payload->>'issue_description', ''),
    COALESCE(candidate.payload->'scoring_criteria', '[]'::jsonb),
    CASE
        WHEN candidate.payload->>'complexity' IN ('trivial', 'simple', 'moderate', 'complex') THEN candidate.payload->>'complexity'
        ELSE 'moderate'
    END,
    CASE
        WHEN candidate.payload->>'fitness_score' ~ '^-?[0-9]+(\.[0-9]+)?$' THEN (candidate.payload->>'fitness_score')::double precision
        ELSE 0
    END,
    COALESCE(candidate.payload->>'fitness_reasoning', ''),
    COALESCE(candidate.payload->'evidence', '{}'::jsonb),
    CASE
        WHEN jsonb_typeof(candidate.payload->'warnings') = 'array' THEN
            ARRAY(SELECT jsonb_array_elements_text(candidate.payload->'warnings'))
        ELSE ARRAY[]::text[]
    END,
    candidate.payload,
    'proposed',
    'legacy_candidates_backfill',
    r.created_at
FROM eval_bootstrap_runs r
CROSS JOIN LATERAL jsonb_array_elements(COALESCE(r.candidates, '[]'::jsonb)) WITH ORDINALITY AS candidate(payload, ordinality)
WHERE r.session_id IS NOT NULL
  AND jsonb_typeof(COALESCE(r.candidates, '[]'::jsonb)) = 'array'
  AND NOT EXISTS (
      SELECT 1
      FROM eval_bootstrap_candidates existing
      WHERE existing.org_id = r.org_id
        AND existing.bootstrap_run_id = r.id
  );
