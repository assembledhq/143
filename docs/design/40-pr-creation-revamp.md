# Design: PR Creation Revamp — User-Authored PRs, Template Support, and Issueless Sessions

> **Status:** Partially Implemented | **Last reviewed:** 2026-04-21
>
> **Implementation notes:** PR template detection and caching implemented (`pr_templates` store). Missing: user GitHub auth OAuth expansion, user-authored PR creation flow, issueless session support.

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

To create PRs as the user, we need a GitHub token with repo-scoped write permissions for that user.

**Approach: Expand OAuth scopes.** Add `repo` scope to the existing OAuth flow. This grants read/write access to repos the user can access.

**Why this approach:**
- Single auth flow — no extra setup step for users
- Users already sign in with GitHub; they just see an updated scope consent screen
- Matches what Codex (web), Claude Code (web), and Devin do
- OAuth App tokens don't expire, so no refresh token complexity

**Trade-off:** The `repo` scope is broad (all repos the user can access, not just 143-connected ones). This is acceptable because (a) we only use the token to create PRs in repos where the GitHub App is already installed, and (b) this is the standard scope every comparable tool requests.

**Scope change:**
```
Before: read:user, user:email
After:  read:user, user:email, repo
```

Existing users will need to re-authorize on next login to grant the new scope. Users who signed up before the scope change will not have a stored token until they re-auth — the system falls back to the GitHub App for these users (see 3.5).

### 3.3 Token Storage

Store the user's GitHub token in the existing `user_credentials` table (from design doc 34):

```sql
-- No new table needed. Use user_credentials with provider = 'github'
INSERT INTO user_credentials (user_id, org_id, provider, config, status)
VALUES ($1, $2, 'github', encrypt({
    "access_token": "ghu_xxxx",
    "token_type": "bearer",
    "scope": "repo,read:user,user:email"
}), 'active');
```

OAuth App tokens don't expire — store once at login, done. No refresh logic needed.

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

PR authorship policy is stored in the existing `organizations.settings` JSONB column, parsed via `models.OrgSettings`. This is the same settings object that holds `autonomy_level`, `max_concurrent_runs`, `default_agent_type`, etc.

**Model change** — add to `OrgSettings` in `internal/models/org_settings.go`:

```go
type OrgSettings struct {
    // ... existing fields ...
    PRAuthorship   string `json:"pr_authorship,omitempty"`    // "user_preferred" | "app_only" | "user_required"
    PRDraftDefault bool   `json:"pr_draft_default,omitempty"` // create PRs as draft by default
}
```

**Values:**

| Mode | Behavior |
|------|----------|
| `user_preferred` (default) | Use user token if available, fall back to app |
| `app_only` | Always use the GitHub App (current behavior) |
| `user_required` | Require user GitHub auth; block PR creation if not connected |

The default is `user_preferred` (zero-value treated as `user_preferred` in `resolveToken`). This means existing orgs see no behavior change — since no users have stored GitHub tokens yet, all PRs continue using the app until users re-auth with the expanded scope.

**Settings UI** — The `pr_authorship` field is exposed on the existing Settings > Autopilot page (`frontend/src/app/(dashboard)/settings/autopilot/page.tsx`), which already manages org-level agent configuration. It fits naturally alongside the existing autonomy and agent config controls. Alternatively, it could live under a new "Pull Requests" subsection on the settings page if the autopilot page becomes too crowded.

The setting is persisted via the existing `PATCH /api/v1/orgs/{id}/settings` endpoint, which updates the `organizations.settings` JSONB column. No new API endpoint is needed — the handler at `internal/api/handlers/settings.go` already accepts partial `OrgSettings` updates and merges them.

### 3.8 PR Authorship UX

The user experience around PR authorship should feel invisible when working, and clear when it matters. Three touchpoints:

#### Session Detail — "Create PR" Button

The existing "Create PR" button on the session detail page gets a small addition: a subtle author indicator below/beside it showing who the PR will be created as.

**When the user has a GitHub token stored:**

```
┌─────────────────────────────────────────┐
│                                         │
│   [ Create PR ]                         │
│                                         │
│   PR will be opened as @janedoe         │
│                                         │
└─────────────────────────────────────────┘
```

The `@janedoe` text is a muted secondary color (e.g., `text-muted-foreground`). It uses the `github_login` from the `users` table — already available on every authenticated request. No tooltip needed; the information is glanceable and unobtrusive.

**When the user does NOT have a GitHub token stored:**

```
┌─────────────────────────────────────────┐
│                                         │
│   [ Create PR ]                         │
│                                         │
│   PR will be opened by 143              │
│   Connect GitHub to open PRs as you ›   │
│                                         │
└─────────────────────────────────────────┘
```

The "Connect GitHub to open PRs as you" line is a text link (`text-sm text-muted-foreground hover:underline`) that triggers the GitHub OAuth re-auth flow with the expanded `repo` scope. After auth completes, the user is redirected back to the session page, and the indicator updates to show their username.

**When the org has `pr_authorship: "app_only"`:**

```
┌─────────────────────────────────────────┐
│                                         │
│   [ Create PR ]                         │
│                                         │
│   PR will be opened by 143              │
│                                         │
└─────────────────────────────────────────┘
```

No connect prompt. The org has explicitly chosen app-only mode.

**When the org has `pr_authorship: "user_required"` and user hasn't connected:**

```
┌─────────────────────────────────────────┐
│                                         │
│   [ Create PR ]  (disabled)             │
│                                         │
│   Connect GitHub to create PRs ›        │
│                                         │
└─────────────────────────────────────────┘
```

The button is disabled. The connect link is the only call to action. This makes it unambiguous that the org requires personal auth.

#### User Profile / Account Page

The user's account page (or a section within Settings) shows GitHub connection status:

```
GitHub

  Connected as @janedoe                 [ Disconnect ]

  Your pull requests will be opened under your GitHub account.
```

Or, when not connected:

```
GitHub

  Not connected                         [ Connect GitHub ]

  Pull requests are currently opened by the 143 app.
  Connect your GitHub account to open PRs as yourself.
```

This uses the same visual language as the existing integration cards on the Settings > Integrations page — a provider name, status indicator, and action button. The difference is that this is per-user, not per-org.

#### API Endpoint for Frontend

The frontend needs to know the user's GitHub connection status to render the correct indicator. Add:

```
GET /api/v1/users/me/github-status
```

Response:

```json
{
  "connected": true,
  "github_login": "janedoe",
  "pr_authorship_mode": "user_preferred"
}
```

This returns the user's stored credential status and the org's `pr_authorship` setting so the frontend can determine which variant to render in a single call.

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
| PR title | First-line `ResultSummary` when available, normalized into a concise review-ready title; otherwise a normalized `session.Title`, falling back to `"Session {id[:8]}"` |
| PR body summary | `session.ResultSummary` |
| Branch name | `143/{id[:8]}/{slugified-title}` — drop the `fix/` prefix for non-issue sessions |
| Commit message | `session.Title` or `ResultSummary` first line |
| Labels | `143-generated` only (no severity/source labels without an issue) |

### 5.3 Updated Title and Branch Logic

```go
func formatPRTitle(session *models.Session, issue *models.Issue) string {
    if issue != nil {
        switch issue.Source {
        case models.IssueSourceLinear:
            return fmt.Sprintf("%s: %s", issue.ExternalID, normalizePRTitleCandidate(issue.Title))
        default:
            return fmt.Sprintf("fix: %s", bestPRTitleSubject(session, issue.Title))
        }
    }

    if session.ResultSummary != nil && *session.ResultSummary != "" {
        return normalizePRTitleCandidate(firstLine(*session.ResultSummary))
    }
    if session.Title != nil && *session.Title != "" {
        return normalizePRTitleCandidate(*session.Title)
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

`normalizePRTitleCandidate` is responsible for collapsing whitespace, trimming prompt-like framing such as "please make sure...", rewriting common past-tense summary openings into imperative PR titles, and capping the final title length. The goal is that PR titles describe the shipped change, not the raw support ticket phrasing or the original agent prompt.

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

1. Expand OAuth scopes to include `repo`
2. Store user GitHub token in `user_credentials` on login
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
- **Scope note**: The `repo` scope is broad but standard. We only use the token to create PRs in repos where the GitHub App is already installed — we never enumerate or access repos outside the 143 installation
- **Token revocation**: If a user revokes 143's GitHub access, API calls fail with 401. The system catches this, falls back to app token, and marks the user credential as `revoked`
- **Audit trail**: PR creation already logs `AuditActionSessionPRRequested`. Add a field indicating whether the PR was created as the user or the app

---

## 9. Migration

- **Existing users**: No disruption. `pr_authorship` defaults to `user_preferred`, but no users have GitHub tokens stored yet, so all PRs continue using the app
- **New OAuth scope**: On next login, users see the updated scope consent. Token is stored automatically
- **Forced re-auth**: Optionally, orgs can set `pr_authorship: "user_required"` which prompts users to re-authorize with the new scope before they can create PRs
