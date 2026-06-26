UPDATE repository_preview_policies
SET github_pr_comment_enabled = true,
    github_commit_status_enabled = true
WHERE github_pr_comment_enabled = false
   OR github_commit_status_enabled = false;
