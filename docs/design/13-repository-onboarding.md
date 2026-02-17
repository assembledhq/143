# Design: Repository Onboarding & GitHub Authentication

This document describes how 143.dev connects to GitHub repositories, authenticates users, and manages repo access so that coding agents can clone, read, and push code.

## Overview

Before any agent can fix an issue, the system needs authenticated access to the target repository. 143.dev uses a **dual-auth model**:

1. **GitHub OAuth** — for user identity. Users sign in with their GitHub account so the system knows who they are.
2. **GitHub App installation** — for repo access. Organizations install the 143.dev GitHub App on their repos, granting fine-grained, scoped permissions.

This is the same pattern used by Codex (web), Claude Code (web/cloud), and most modern AI coding platforms. The GitHub App provides short-lived installation tokens for API calls, avoiding long-lived PATs. The OAuth flow provides user identity without needing to store user passwords.

## Why This Model

| Approach | Pros | Cons |
|----------|------|------|
| **Personal Access Tokens (PATs)** | Simple setup | Long-lived, broad access, tied to one user, security risk if leaked |
| **OAuth App** | User-level auth | Can access all user's repos (no granular control), lower rate limits |
| **GitHub App** | Granular permissions, short-lived tokens, org-level control, higher rate limits, webhook support | More setup steps |
| **Fine-grained PATs** | Scoped to repos, expiring | Still user-tied, no webhook support, manual rotation |

**Decision**: GitHub App + OAuth. This gives:
- **Org admins** control over exactly which repos 143.dev can access
- **Short-lived tokens** (1 hour max) that reduce blast radius if compromised
- **Webhook delivery** for PR events, reviews, deploys
- **Higher rate limits** that scale with org size
- **User identity** via OAuth without storing GitHub passwords

## Authentication Architecture

```
                                  ┌──────────────┐
                                  │   GitHub.com  │
                                  └──────┬───────┘
                                         │
                    ┌────────────────────┼────────────────────┐
                    │                    │                    │
              ┌─────▼─────┐      ┌──────▼──────┐     ┌──────▼──────┐
              │   OAuth    │      │  GitHub App │     │  Webhooks   │
              │   Flow     │      │ Installation│     │  (push)     │
              │ (user ID)  │      │ (repo access)│    │             │
              └─────┬─────┘      └──────┬──────┘     └──────┬──────┘
                    │                    │                    │
                    ▼                    ▼                    ▼
              ┌──────────────────────────────────────────────────┐
              │                   143.dev Server                 │
              │                                                  │
              │  users table ◀── OAuth    integrations table ◀── │
              │  (github_id,     tokens   (github app ID,        │
              │   avatar, etc.)           installation ID,       │
              │                           private key)           │
              └──────────────────────────────────────────────────┘
```

## GitHub OAuth Flow (User Identity)

Users sign into 143.dev using their GitHub account. This identifies the user and links their GitHub identity to their 143.dev account.

### Setup

Register a GitHub OAuth App (not the GitHub App — these are separate):

- **Application name**: `143.dev`
- **Homepage URL**: `https://your-instance.example.com`
- **Callback URL**: `https://your-instance.example.com/api/v1/auth/github/callback`

Store the OAuth App credentials:

```
GITHUB_OAUTH_CLIENT_ID=Iv1.abc123
GITHUB_OAUTH_CLIENT_SECRET=secret123
```

### Flow

```
User clicks "Sign in with GitHub"
        │
        ▼
  Redirect to GitHub:
  GET https://github.com/login/oauth/authorize
    ?client_id={GITHUB_OAUTH_CLIENT_ID}
    &redirect_uri={callback_url}
    &scope=read:user,user:email
    &state={csrf_token}
        │
        ▼
  User authorizes on GitHub
        │
        ▼
  GitHub redirects to callback:
  GET /api/v1/auth/github/callback?code={code}&state={csrf_token}
        │
        ▼
  Server exchanges code for access token:
  POST https://github.com/login/oauth/access_token
    client_id, client_secret, code
        │
        ▼
  Server fetches user profile:
  GET https://api.github.com/user (with access token)
        │
        ▼
  Create or update user record in DB
  Create session, set cookie
```

### OAuth Scopes

Minimal scopes for user identity only:

| Scope | Purpose |
|-------|---------|
| `read:user` | Read user profile (name, avatar, GitHub username) |
| `user:email` | Read user email addresses |

No repo access scopes — that's handled entirely by the GitHub App installation.

### Implementation

```go
func (h *AuthHandler) GitHubLogin(w http.ResponseWriter, r *http.Request) {
    state := generateCSRFToken()
    h.sessions.SetCSRF(r, w, state)

    url := fmt.Sprintf(
        "https://github.com/login/oauth/authorize?client_id=%s&redirect_uri=%s&scope=read:user,user:email&state=%s",
        h.config.GitHubOAuthClientID,
        h.config.GitHubOAuthCallbackURL,
        state,
    )
    http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

func (h *AuthHandler) GitHubCallback(w http.ResponseWriter, r *http.Request) {
    // 1. Validate CSRF state
    if r.URL.Query().Get("state") != h.sessions.GetCSRF(r) {
        http.Error(w, "invalid state", http.StatusForbidden)
        return
    }

    // 2. Exchange code for token
    code := r.URL.Query().Get("code")
    token, err := h.exchangeCodeForToken(code)
    if err != nil {
        http.Error(w, "oauth exchange failed", http.StatusInternalServerError)
        return
    }

    // 3. Fetch GitHub user profile
    ghUser, err := h.fetchGitHubUser(token)
    if err != nil {
        http.Error(w, "failed to fetch user", http.StatusInternalServerError)
        return
    }

    // 4. Find or create 143.dev user
    user, err := h.db.UpsertUserFromGitHub(r.Context(), &models.GitHubUser{
        GitHubID:  ghUser.ID,
        Login:     ghUser.Login,
        Name:      ghUser.Name,
        Email:     ghUser.Email,
        AvatarURL: ghUser.AvatarURL,
    })
    if err != nil {
        http.Error(w, "user creation failed", http.StatusInternalServerError)
        return
    }

    // 5. Create session
    h.sessions.Create(r, w, user.ID)
    http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}
```

## GitHub App Installation (Repo Access)

The GitHub App grants 143.dev access to specific repositories within an organization. Org admins install the app and choose which repos to expose.

### GitHub App Configuration

Create a GitHub App with these settings:

- **App name**: `143.dev`
- **Homepage URL**: `https://143.dev`
- **Setup URL**: `https://your-instance.example.com/settings/integrations/github/setup` (called after installation)
- **Webhook URL**: `https://your-instance.example.com/api/v1/webhooks/github`
- **Webhook secret**: Generated per instance

### Required Permissions

| Permission | Access | Purpose |
|------------|--------|---------|
| **Contents** | Read & Write | Clone repos, create branches, push commits |
| **Pull requests** | Read & Write | Create PRs, read PR reviews and comments |
| **Issues** | Read | Read issue references from commits/PRs |
| **Checks** | Read | Monitor CI status on PRs |
| **Deployments** | Read | Detect deploys after PR merge |
| **Metadata** | Read | Required for all GitHub Apps (repo name, default branch, etc.) |

### Subscribed Events

| Event | Purpose |
|-------|---------|
| `push` | Detect code changes for context updates |
| `pull_request` | Track PR lifecycle (open, merge, close) |
| `pull_request_review` | Capture review feedback |
| `pull_request_review_comment` | Inline review comments |
| `deployment_status` | Deploy detection |
| `installation` | App install/uninstall |
| `installation_repositories` | Repo add/remove from installation |

### Installation Flow

```
Admin clicks "Connect GitHub" in 143.dev settings
        │
        ▼
  Redirect to GitHub App install page:
  GET https://github.com/apps/143-dev/installations/new
    ?state={org_id}
        │
        ▼
  Admin selects org and repos on GitHub
  (can choose "All repositories" or specific repos)
        │
        ▼
  GitHub sends POST to webhook URL:
  Event: installation, Action: created
  Payload includes installation_id, account, repositories
        │
        ▼
  GitHub redirects to Setup URL:
  GET /settings/integrations/github/setup
    ?installation_id={id}&setup_action=install&state={org_id}
        │
        ▼
  Server stores installation in DB:
  - Create/update integration record
  - Create repository records for each selected repo
  - Trigger initial repo sync
```

### Installation Token Management

For each GitHub API call, the system generates a short-lived installation token:

```go
type GitHubTokenManager struct {
    appID      int64
    privateKey *rsa.PrivateKey
    cache      map[int64]*CachedToken // installation_id -> token
    mu         sync.RWMutex
}

type CachedToken struct {
    Token     string
    ExpiresAt time.Time
}

func (m *GitHubTokenManager) GetInstallationToken(ctx context.Context, installationID int64) (string, error) {
    // 1. Check cache
    m.mu.RLock()
    cached, ok := m.cache[installationID]
    m.mu.RUnlock()
    if ok && time.Now().Add(5*time.Minute).Before(cached.ExpiresAt) {
        return cached.Token, nil
    }

    // 2. Generate JWT from App private key
    jwt, err := m.generateJWT()
    if err != nil {
        return "", fmt.Errorf("generate JWT: %w", err)
    }

    // 3. Exchange JWT for installation token
    // POST /app/installations/{installation_id}/access_tokens
    token, expiresAt, err := m.exchangeForInstallationToken(ctx, jwt, installationID)
    if err != nil {
        return "", fmt.Errorf("get installation token: %w", err)
    }

    // 4. Cache the token
    m.mu.Lock()
    m.cache[installationID] = &CachedToken{Token: token, ExpiresAt: expiresAt}
    m.mu.Unlock()

    return token, nil
}

func (m *GitHubTokenManager) generateJWT() (string, error) {
    now := time.Now()
    claims := jwt.MapClaims{
        "iat": now.Add(-60 * time.Second).Unix(), // issued at (60s in the past for clock drift)
        "exp": now.Add(10 * time.Minute).Unix(),  // expires in 10 minutes (max)
        "iss": m.appID,                            // GitHub App ID
    }
    token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
    return token.SignedString(m.privateKey)
}
```

### Scoped Tokens

When possible, installation tokens are scoped to specific repos for least-privilege:

```go
func (m *GitHubTokenManager) GetScopedToken(ctx context.Context, installationID int64, repoIDs []int64) (string, error) {
    jwt, _ := m.generateJWT()

    // POST /app/installations/{installation_id}/access_tokens
    // Body: { "repository_ids": [123, 456], "permissions": { "contents": "write", "pull_requests": "write" } }
    body := map[string]interface{}{
        "repository_ids": repoIDs,
        "permissions": map[string]string{
            "contents":      "write",
            "pull_requests": "write",
        },
    }
    // ... exchange for scoped token
}
```

## Repository Management

### Data Model

New `repositories` table to track connected repos:

```sql
CREATE TABLE repositories (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid NOT NULL REFERENCES organizations(id),
    integration_id    uuid NOT NULL REFERENCES integrations(id),
    github_id         bigint NOT NULL,         -- GitHub's numeric repo ID
    full_name         text NOT NULL,            -- "owner/repo"
    default_branch    text NOT NULL DEFAULT 'main',
    private           boolean NOT NULL DEFAULT false,
    language          text,                     -- primary language
    description       text,
    clone_url         text NOT NULL,            -- HTTPS clone URL
    installation_id   bigint NOT NULL,          -- GitHub App installation ID
    status            text NOT NULL DEFAULT 'active', -- active, paused, disconnected
    last_synced_at    timestamptz,             -- last full context sync
    context_quality   float,                   -- 0-100 quality score (see doc 14)
    settings          jsonb NOT NULL DEFAULT '{}', -- per-repo overrides
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_repositories_github ON repositories (org_id, github_id);
CREATE INDEX idx_repositories_org ON repositories (org_id, status);
CREATE INDEX idx_repositories_fullname ON repositories (org_id, full_name);
```

### Linking Issues to Repos

Issues need to know which repository they belong to. Add a `repository_id` column to the `issues` table:

```sql
ALTER TABLE issues ADD COLUMN repository_id uuid REFERENCES repositories(id);
CREATE INDEX idx_issues_repo ON issues (repository_id);
```

This is populated during ingestion:
- **Sentry**: Extract repo from the stack trace file paths or Sentry's code mapping config
- **Linear**: Extract from Linear project -> repo mapping (configured in integration settings)
- **Support**: Manual mapping or inferred from error references in ticket text

### Webhook Handling for Installation Events

```go
func (h *WebhookHandler) HandleGitHubInstallation(ctx context.Context, event *github.InstallationEvent) error {
    switch event.GetAction() {
    case "created":
        // New installation — store integration and repos
        integration, err := h.db.CreateIntegration(ctx, &models.Integration{
            OrgID:    orgIDFromState(event),
            Provider: "github",
            Config: jsonb{
                "installation_id": event.GetInstallation().GetID(),
                "account_login":   event.GetInstallation().GetAccount().GetLogin(),
                "account_type":    event.GetInstallation().GetAccount().GetType(), // "User" or "Organization"
            },
            Status: "active",
        })
        // Create repository records for each repo in the installation
        for _, repo := range event.Repositories {
            h.db.CreateRepository(ctx, &models.Repository{
                OrgID:          integration.OrgID,
                IntegrationID:  integration.ID,
                GitHubID:       repo.GetID(),
                FullName:       repo.GetFullName(),
                Private:        repo.GetPrivate(),
                CloneURL:       fmt.Sprintf("https://github.com/%s.git", repo.GetFullName()),
                InstallationID: event.GetInstallation().GetID(),
            })
        }
        // Trigger initial context build for each repo
        for _, repo := range event.Repositories {
            h.jobs.Enqueue(ctx, "build_repo_context", map[string]interface{}{"repository_id": repo.ID})
        }

    case "deleted":
        // Installation removed — mark repos as disconnected
        h.db.DisconnectInstallationRepos(ctx, event.GetInstallation().GetID())

    case "suspend":
        h.db.PauseInstallationRepos(ctx, event.GetInstallation().GetID())

    case "unsuspend":
        h.db.ActivateInstallationRepos(ctx, event.GetInstallation().GetID())
    }
    return nil
}

func (h *WebhookHandler) HandleInstallationRepositories(ctx context.Context, event *github.InstallationRepositoriesEvent) error {
    installationID := event.GetInstallation().GetID()

    // Repos added to the installation
    for _, repo := range event.RepositoriesAdded {
        h.db.CreateRepository(ctx, &models.Repository{
            // ...same as above
        })
        h.jobs.Enqueue(ctx, "build_repo_context", map[string]interface{}{"repository_id": repo.ID})
    }

    // Repos removed from the installation
    for _, repo := range event.RepositoriesRemoved {
        h.db.DisconnectRepository(ctx, installationID, repo.GetID())
    }
    return nil
}
```

## Repository Cloning

When an agent run starts, the orchestrator needs to clone the repo into the sandbox. The clone uses the installation token for HTTPS auth.

### Clone Strategy

```go
func (o *Orchestrator) CloneRepo(ctx context.Context, sandbox *Sandbox, repo *models.Repository) error {
    // 1. Get a scoped installation token for this repo
    token, err := o.tokenManager.GetScopedToken(ctx, repo.InstallationID, []int64{repo.GitHubID})
    if err != nil {
        return fmt.Errorf("get token: %w", err)
    }

    // 2. Clone using HTTPS with token auth
    cloneURL := fmt.Sprintf("https://x-access-token:%s@github.com/%s.git", token, repo.FullName)

    // 3. Execute clone in sandbox
    _, err = sandbox.Exec(ctx, "git", "clone", "--depth=1", cloneURL, "/workspace")
    if err != nil {
        return fmt.Errorf("clone: %w", err)
    }

    // 4. Checkout the target branch
    _, err = sandbox.Exec(ctx, "git", "-C", "/workspace", "checkout", repo.DefaultBranch)
    return err
}
```

### Shallow vs Full Clone

| Strategy | When | Why |
|----------|------|-----|
| `--depth=1` (shallow) | Default for agent runs | Fast, minimal disk. Agent only needs current state. |
| Full clone | When agent needs git history (blame, log) | Rare. Only for complex investigations. |
| `--filter=blob:none` (treeless) | Large repos with deep history needs | Downloads tree structure but fetches blobs on demand. |

The clone strategy is configurable per repo in `repositories.settings`:

```json
{
  "clone_strategy": "shallow",
  "clone_depth": 1,
  "submodules": false
}
```

## API Endpoints

New routes for repository management:

```
/api/v1/
├── /auth
│   ├── GET    /github/login         # initiate GitHub OAuth
│   └── GET    /github/callback      # OAuth callback
│
├── /repositories
│   ├── GET    /                      # list connected repos (with context quality scores)
│   ├── GET    /:id                   # get repo details + context stats
│   ├── PATCH  /:id                   # update repo settings (clone strategy, etc.)
│   ├── POST   /:id/sync             # trigger manual context rebuild
│   ├── DELETE /:id                   # disconnect repo (does not delete on GitHub)
│   └── GET    /:id/context           # get repo context package summary
│
├── /integrations/github
│   ├── GET    /install-url           # get GitHub App installation URL
│   └── GET    /setup                 # setup callback after installation
```

## Self-Hosted Configuration

For self-hosted instances, users create their own GitHub App:

### Setup Script

The `setup.sh` script guides through GitHub App creation:

```bash
echo "=== GitHub App Setup ==="
echo ""
echo "1. Go to https://github.com/settings/apps/new"
echo "2. Fill in:"
echo "   - App name: 143-dev-{your-org}"
echo "   - Homepage URL: ${BASE_URL}"
echo "   - Webhook URL: ${BASE_URL}/api/v1/webhooks/github"
echo "   - Setup URL: ${BASE_URL}/settings/integrations/github/setup"
echo ""
echo "3. Set permissions:"
echo "   - Contents: Read & Write"
echo "   - Pull requests: Read & Write"
echo "   - Issues: Read"
echo "   - Checks: Read"
echo "   - Deployments: Read"
echo "   - Metadata: Read"
echo ""
echo "4. Subscribe to events:"
echo "   - Push, Pull request, Pull request review,"
echo "   - Pull request review comment, Deployment status,"
echo "   - Installation, Installation repositories"
echo ""
echo "5. Generate a private key and download it"
echo ""
read -p "Enter GitHub App ID: " GITHUB_APP_ID
read -p "Enter GitHub OAuth Client ID: " GITHUB_OAUTH_CLIENT_ID
read -p "Enter GitHub OAuth Client Secret: " GITHUB_OAUTH_CLIENT_SECRET
read -p "Path to private key file: " GITHUB_APP_PRIVATE_KEY_PATH
GITHUB_APP_PRIVATE_KEY=$(cat "$GITHUB_APP_PRIVATE_KEY_PATH")
GITHUB_WEBHOOK_SECRET=$(openssl rand -hex 32)
```

### Manifest-Based App Creation (Alternative)

For a smoother experience, 143.dev supports GitHub's [App Manifest flow](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest) which auto-creates the GitHub App with the correct settings:

```go
func (h *SetupHandler) CreateAppFromManifest(w http.ResponseWriter, r *http.Request) {
    manifest := map[string]interface{}{
        "name":        "143-dev",
        "url":         h.config.BaseURL,
        "hook_attributes": map[string]string{
            "url": h.config.BaseURL + "/api/v1/webhooks/github",
        },
        "setup_url":   h.config.BaseURL + "/settings/integrations/github/setup",
        "redirect_url": h.config.BaseURL + "/api/v1/auth/github/manifest-callback",
        "public":       false,
        "default_permissions": map[string]string{
            "contents":      "write",
            "pull_requests": "write",
            "issues":        "read",
            "checks":        "read",
            "deployments":   "read",
            "metadata":      "read",
        },
        "default_events": []string{
            "push", "pull_request", "pull_request_review",
            "pull_request_review_comment", "deployment_status",
            "installation", "installation_repositories",
        },
    }
    // Redirect to https://github.com/settings/apps/new?manifest={json}
}
```

This creates the GitHub App in one click, and GitHub returns the App ID, client secret, private key, and webhook secret in the callback. The system stores these automatically.

## Security Considerations

1. **Private key storage**: The GitHub App private key is stored encrypted at rest in the `integrations.config` column (which is already encrypted).
2. **Token lifetime**: Installation tokens expire after 1 hour. The token manager caches them and refreshes 5 minutes before expiry.
3. **Scoped tokens**: When possible, tokens are scoped to specific repositories and minimal permissions.
4. **Webhook validation**: All GitHub webhooks are validated via HMAC-SHA256 using the webhook secret.
5. **OAuth state**: The OAuth flow uses CSRF tokens to prevent state fixation attacks.
6. **No PATs**: The system never stores or requires personal access tokens. All repo access goes through the GitHub App.

## Connection with Other Design Docs

**Agent Orchestrator (doc 06)**:
- `CloneRepo` now uses the `GitHubTokenManager` for auth instead of deploy keys
- The `AgentInput` struct gains a `Repository` field with clone URL, default branch, etc.

**PR & Ship (doc 08)**:
- `GitHubService` uses the same `GitHubTokenManager` for creating branches, pushing commits, and creating PRs
- The existing `getInstallationToken` method is replaced by the centralized token manager

**Ingestion (doc 04)**:
- Issues are linked to repositories via `repository_id`
- Repo mapping is configured per integration

**Database Schema (doc 01)**:
- New `repositories` table
- New `repository_id` column on `issues`
- `users` table gains GitHub OAuth fields (`github_id`, `github_login`, `avatar_url`)

**Codebase Context (doc 14)**:
- Repository onboarding triggers the initial context build
- Context is stored per-repository and updated on push events
