UPDATE organizations
SET settings = settings - 'default_work_repository_id'
WHERE settings ? 'default_work_repository_id';
