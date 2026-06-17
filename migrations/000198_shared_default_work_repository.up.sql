UPDATE organizations
SET settings = jsonb_set(
	COALESCE(settings, '{}'::jsonb),
	'{default_work_repository_id}',
	settings #> '{linear_agent,default_repo_id}',
	true
)
WHERE settings #> '{default_work_repository_id}' IS NULL
  AND settings #> '{linear_agent,default_repo_id}' IS NOT NULL;
