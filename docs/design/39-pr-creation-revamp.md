# Design: PR Creation Revamp — User-Authored PRs, Template Support, and Issueless Sessions

> **Status:** Proposal | **Last reviewed:** 2026-03-29

**Depends on**: [08-pr-and-ship.md](implemented/08-pr-and-ship.md), [13-repository-onboarding.md](implemented/13-repository-onboarding.md), [34-personal-team-coding-agents.md](implemented/34-personal-team-coding-agents.md)

---

## 1. Problem Statement

The current PR creation system has three limitations:

1. **Bot-authored PRs** — All PRs are created as the 143 GitHub App. Reviewers see a bot as the author, which reduces trust and breaks workflows that rely on CODEOWNERS, required reviewers, or "who wrote this" attribution. Every top AI coding tool (Codex, Claude Code, Cursor) creates PRs as the user.

2. **No repo PR template support** — We generate a hardcoded markdown body with issue metadata and validation tables. Repos that have `.github/pull_request_template.md` expect PRs to follow that structure. Our PRs look foreign.

3. **Issue-required assumption** — `CreatePR` requires `run.IssueID` to fetch title, severity, source, etc. Manually created sessions (the growing majority of usage) may not have a meaningful issue attached, causing the PR body to be filled with empty/irrelevant fields.

---

## 2. Goals

- Let users create PRs **as themselves** via their GitHub OAuth token
- Fall back to the GitHub App when a user hasn't connected GitHub or for automated/unattended sessions
- Detect and use the repo's existing PR template, filling it in with session context
- Fall back to a minimal default template when no repo template exists
- Support sessions with and without attached issues
- Keep the change backward-compatible — existing orgs see no disruption

---

## 3. User GitHub Auth for PR Creation

### 3.1 Current State

Today we have two GitHub auth mechanisms:

| Mechanism | Purpose | Scopes |
|-----------|---------|--------|
| **GitHub OAuth App** | User identity (sign-in) | `read:user`, `user:email` |
| **GitHub App installation** | Repo access (clone, push, create PRs) | Contents R/W, PRs R/W, Checks R, Deployments R |

The OAuth token is used only during login to fetch the user profile. It is **not stored** after the session is created — we have no long-lived user GitHub token.

### 3.2 What Needs to Change

To create PRs as the user, we need a GitHub token with repo-scoped write permissions for that user. Two approaches:

#### Option A: Expand OAuth Scopes (Recommended)

Add `repo` scope to the existing OAuth flow. This grants read/write access to repos the user can access.

**Pros:**
- Single auth flow — no extra setup step for users
- Users already sign in with GitHub; they just see an updated scope consent screen
- Matches what Codex (web), Claude Code (web), and Devin do

**Cons:**
- `repo` scope is broad (all repos the user can access, not just 143-connected ones)
- Existing users need to re-authorize to grant the new scope

**Scope change:**
```
Before: read:user, user:email
After:  read:user, user:email, repo
```

#### Option B: Separate GitHub App OAuth Flow (Fine-Grained)

Use the GitHub App's built-in OAuth flow (`user-to-server tokens`) instead of the standalone OAuth App. This scopes the token to only repos where the GitHub App is installed.

**Pros:**
- Token is automatically scoped to only repos with the 143 App installed
- No broad `repo` scope
- GitHub App user-to-server tokens are the modern recommended approach

**Cons:**
- More complex to implement (different token exchange endpoint)
- Token expires every 8 hours, requires refresh token handling

**Recommendation:** Option B is the better long-term choice. The token is inherently scoped to repos the org has granted 143 access to, which is the principle of least privilege. However, Option A is simpler to ship first and can be migrated later.

### 3.3 Token Storage

Store the user's GitHub token in the existing `user_credentials` table (from design doc 34):

```sql
-- No new table needed. Use user_credentials with provider = 'github'
INSERT INTO user_credentials (user_id, org_id, provider, config, status)
VALUES ($1, $2, 'github', encrypt({
    "access_token": "ghu_xxxx",
    "refresh_token": "ghr_xxxx",  -- only for Option B
    "token_type": "bearer",
    "scope": "repo,read:user,user:email",
    "expires_at": "2026-03-29T12:00:00Z"  -- only for Option B
}), 'active');
```

For Option A (OAuth App tokens), tokens don't expire — store once, done. For Option B (user-to-server tokens), implement refresh logic similar to what we already do for OpenAI ChatGPT tokens in `credentials.go`.

### 3.4 Auth Flow Changes

During GitHub OAuth callback, after fetching the user profile:

```go
func (h *AuthHandler) GitHubCallback(w http.ResponseWriter, r *http.Request) {
    // ... existing code: validate CSRF, exchange code for token, fetch profile ...

    // NEW: Store the GitHub token for PR creation
    if err := h.userCredentials.Upsert(ctx, user.ID, user.OrgID, "github", &GitHubOAuthConfig{
        AccessToken: token.AccessToken,
        TokenType:   token.TokenType,
        Scope:       token.Scope,
    }); err != nil {
        logger.Warn().Err(err).Msg("failed to store GitHub token for user")
        // Non-fatal — user can still sign in, just can't create PRs as themselves
    }
}
```

### 3.5 Token Resolution Order

When creating a PR, resolve the token in this order:

```
1. Triggering user's personal GitHub token (user_credentials where provider='github')
2. GitHub App installation token (current behavior)
```

```go
func (s *PRService) resolveToken(ctx context.Context, run *models.Session, repo *models.Repository) (string, bool, error) {
    // Try user token first
    if run.TriggeredByUserID != nil {
        cred, err := s.userCredentials.Get(ctx, *run.TriggeredByUserID, run.OrgID, "github")
        if err == nil {
            cfg, _ := ParseGitHubOAuthConfig(cred.Config)
            if cfg.AccessToken != "" {
                return cfg.AccessToken, true /* isUserToken */, nil
            }
        }
    }

    // Fall back to app installation token
    token, err := s.tokenProvider.GetInstallationToken(ctx, repo.InstallationID)
    return token, false, err
}
```

The `isUserToken` flag is returned so the caller knows whether to set git author info (see 3.6).

### 3.6 Commit Author Attribution

When using a user token, set the commit author to the user:

```go
// When creating the commit via the Git Data API
author := &commitAuthor{
    Name:  user.Name,
    Email: user.Email,
    Date:  time.Now().UTC(),
}
```

When using the app token (fallback), add a `Co-authored-by` trailer:

```
fix: resolve null pointer in user API

Co-authored-by: Jane Smith <jane@example.com>
```

### 3.7 Org-Level Configuration

Orgs can configure PR authorship policy in org settings:

```json
{
  "pr_authorship": "user_preferred"  // "user_preferred" | "app_only" | "user_required"
}
```

| Mode | Behavior |
|------|----------|
| `user_preferred` (default) | Use user token if available, fall back to app |
| `app_only` | Always use the GitHub App (current behavior) |
| `user_required` | Require user GitHub auth; block PR creation if not connected |

---

## 4. PR Template Support

### 4.1 Detecting Repo Templates

GitHub repos can have PR templates at several conventional paths:

```
.github/pull_request_template.md
.github/PULL_REQUEST_TEMPLATE.md
docs/pull_request_template.md
pull_request_template.md
PULL_REQUEST_TEMPLATE.md
.github/PULL_REQUEST_TEMPLATE/default.md
```

Fetch the template via the GitHub Contents API during PR creation:

```go
func (s *PRService) fetchPRTemplate(ctx context.Context, token, owner, repo, defaultBranch string) (string, error) {
    paths := []string{
        ".github/pull_request_template.md",
        ".github/PULL_REQUEST_TEMPLATE.md",
        "docs/pull_request_template.md",
        "pull_request_template.md",
    }
    for _, path := range paths {
        content, err := s.getFileContents(ctx, token, owner, repo, path, defaultBranch)
        if err == nil && content != "" {
            return content, nil
        }
    }
    return "", nil // no template found
}
```

### 4.2 Filling In Repo Templates

When a repo template is found, use the LLM to fill it in based on session context. This ensures the PR description matches the team's expected format regardless of what sections they have.

```go
func (s *PRService) fillRepoTemplate(ctx context.Context, template string, run *models.Session, issue *models.Issue) (string, error) {
    prompt := buildTemplateFillPrompt(template, run, issue)
    filled, err := s.llm.Complete(ctx, prompt)
    if err != nil {
        // Fallback: return template with a summary prepended
        return fmt.Sprintf("## Summary\n\n%s\n\n---\n\n%s", summaryText(run), template), nil
    }
    return filled, nil
}
```

The prompt provides:
- The raw template text
- The session's `ResultSummary` (what the agent did)
- The diff stat summary (files changed, lines added/removed)
- Issue context if available (title, source, severity)
- Instructions to be concise and fill in only what's relevant, leaving optional sections empty or removing them

Key principle: **the LLM fills in the team's template, it does not invent new sections.** The prompt explicitly instructs: "Fill in the sections of this PR template. Be concise. If a section is not applicable, write 'N/A' or remove it. Do not add sections that aren't in the template."

### 4.3 Default Template (Fallback)

When no repo template exists, use a minimal default. The current template is too verbose — validation tables, occurrence counts, and agent metadata are noise for reviewers. Replace with:

```markdown
## Summary

{1-3 sentence description of what changed and why}

## Test plan

{How this was validated — tests run, manual verification, etc.}

---
*Generated by [143.dev](https://143.dev)*
```

That's it. Short, scannable, useful. The session link in the 143 dashboard provides full detail for anyone who wants it.

When an issue is attached, append a single line:

```markdown
## Summary

{description}

**Issue**: {source} — {title} ({severity})

## Test plan

{validation summary}

---
*Generated by [143.dev](https://143.dev)*
```

### 4.4 Generating the Summary and Test Plan

Use the session's `ResultSummary` as the basis for the summary. For the test plan, pull from validation results if they exist, otherwise summarize from the agent's output.

```go
func (s *PRService) generatePRBody(ctx context.Context, token, owner, repoName, defaultBranch string, run *models.Session, issue *models.Issue) string {
    // 1. Try repo template
    template, _ := s.fetchPRTemplate(ctx, token, owner, repoName, defaultBranch)
    if template != "" {
        filled, err := s.fillRepoTemplate(ctx, template, run, issue)
        if err == nil {
            return filled + "\n\n---\n*Generated by [143.dev](https://143.dev)*\n"
        }
    }

    // 2. Fall back to minimal default
    return s.buildDefaultBody(ctx, run, issue)
}
```

---

## 5. Issueless Sessions

### 5.1 Problem

`CreatePR` currently requires `run.IssueID` to be a valid UUID and fetches the issue for title, severity, source, customer count, etc. Manually created sessions often have no meaningful issue — the user just started a session with a prompt.

### 5.2 Changes

Make `IssueID` optional in the PR creation path. The session itself has enough context:

| Field | Source |
|-------|--------|
| PR title | `session.Title` (set by user or agent), falling back to first line of `ResultSummary`, falling back to `"Session {id[:8]}"` |
| PR body summary | `session.ResultSummary` |
| Branch name | `143/{id[:8]}/{slugified-title}` — drop the `fix/` prefix for non-issue sessions |
| Commit message | `session.Title` or `ResultSummary` first line |
| Labels | `143-generated` only (no severity/source labels without an issue) |

### 5.3 Updated Title and Branch Logic

```go
func formatPRTitle(session *models.Session, issue *models.Issue) string {
    // Issue-based sessions: keep current behavior
    if issue != nil {
        switch issue.Source {
        case models.IssueSourceLinear:
            return fmt.Sprintf("%s: %s", issue.ExternalID, issue.Title)
        default:
            return fmt.Sprintf("fix: %s", issue.Title)
        }
    }

    // Issueless sessions: use session title
    if session.Title != nil && *session.Title != "" {
        return *session.Title
    }
    if session.ResultSummary != nil && *session.ResultSummary != "" {
        return firstLine(*session.ResultSummary)
    }
    return fmt.Sprintf("Session %s", session.ID.String()[:8])
}

func formatBranchName(session *models.Session, issue *models.Issue) string {
    short := session.ID.String()[:8]
    var title string
    if issue != nil {
        title = issue.Title
    } else if session.Title != nil {
        title = *session.Title
    }
    slug := slugify(title)
    if slug == "" {
        slug = "changes"
    }
    return fmt.Sprintf("143/%s/%s", short, slug)
}
```

### 5.4 Updated CreatePR Flow

```go
func (s *PRService) CreatePR(ctx context.Context, run *models.Session) (*models.PullRequest, error) {
    // Issue lookup is now optional
    var issue *models.Issue
    if run.IssueID != uuid.Nil {
        i, err := s.issues.GetByID(ctx, run.OrgID, run.IssueID)
        if err == nil {
            issue = &i
        }
    }

    // Resolve repo — must still exist
    repoID := run.RepositoryID
    if repoID == nil && issue != nil {
        repoID = issue.RepositoryID
    }
    if repoID == nil {
        return nil, fmt.Errorf("session %s has no repository", run.ID)
    }

    // ... rest of flow uses (run, issue) pair where issue may be nil ...
}
```

---

## 6. Implementation Plan

### Phase 1: Issueless PR Support + Minimal Default Template

Low-risk changes to unblock manually created sessions.

1. Make issue lookup optional in `PRService.CreatePR`
2. Update `formatPRTitle`, `formatBranchName`, `formatCommitMessage` to accept `nil` issue
3. Replace the verbose default PR body with the minimal template from section 4.3
4. Update tests

### Phase 2: Repo PR Template Detection and Fill

1. Add `fetchPRTemplate` to fetch repo templates via GitHub Contents API
2. Add LLM-based template fill with fallback to default
3. Cache template per repo (invalidate on push to default branch)

### Phase 3: User-Authored PRs

1. Expand OAuth scopes to include `repo` (Option A) or implement GitHub App user-to-server tokens (Option B)
2. Store user GitHub token in `user_credentials`
3. Add `resolveToken` with user → app fallback
4. Set commit author when using user token
5. Add `pr_authorship` org setting
6. Add UI indicator showing who the PR will be created as
7. Handle token expiry/revocation gracefully (fall back to app, notify user)

### Phase 4: Polish

1. Add `Co-authored-by` trailer when using app token for users who have GitHub connected
2. Support draft PR creation (org setting: `pr_draft_default: true`)
3. Add link to 143 session in PR body footer
4. Support multiple PR templates (`.github/PULL_REQUEST_TEMPLATE/` directory) — let user pick or use default

---

## 7. API Changes

### New/Modified Endpoints

| Method | Endpoint | Change |
|--------|----------|--------|
| `POST /api/v1/sessions/{id}/pr` | Add optional `draft` boolean in request body |
| `GET /api/v1/users/me/github-status` | New — returns whether user has a valid GitHub token for PR creation |
| `POST /api/v1/users/me/github/disconnect` | New — removes stored GitHub token |

### New Org Settings Fields

```json
{
  "pr_authorship": "user_preferred",
  "pr_draft_default": false
}
```

---

## 8. Security Considerations

- **Token storage**: User GitHub tokens are encrypted at rest using the same AES-GCM scheme as other credentials in `user_credentials`
- **Scope minimization**: Option B (user-to-server tokens) is preferred long-term as tokens are inherently scoped to installed repos
- **Token revocation**: If a user revokes 143's GitHub access, API calls fail with 401. The system catches this, falls back to app token, and marks the user credential as `revoked`
- **Audit trail**: PR creation already logs `AuditActionSessionPRRequested`. Add a field indicating whether the PR was created as the user or the app

---

## 9. Migration

- **Existing users**: No disruption. `pr_authorship` defaults to `user_preferred`, but no users have GitHub tokens stored yet, so all PRs continue using the app
- **New OAuth scope**: On next login, users see the updated scope consent. Token is stored automatically
- **Forced re-auth**: Optionally, orgs can set `pr_authorship: "user_required"` which prompts users to re-authorize with the new scope before they can create PRs
