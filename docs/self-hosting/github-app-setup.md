# GitHub App Setup Guide

143 uses two separate GitHub configurations to connect to your repositories:

1. **GitHub OAuth App** — handles "Sign in with GitHub" for user login
2. **GitHub App** — handles repository access, PR creation, and webhook events

If you're using the hosted version at 143.dev, both are pre-configured. This guide is for self-hosted deployments where you need to create your own.

## Prerequisites

- A GitHub account (personal or organization)
- A running 143 instance with a publicly reachable URL (for webhooks)
- For local development, a tunnel like [ngrok](https://ngrok.com/) or [cloudflared](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/) to expose your local server

Throughout this guide, replace `{BASE_URL}` with your instance's public URL (e.g. `https://143.example.com` or `https://abc123.ngrok.io` for local dev).

## Part 1: GitHub OAuth App

This lets users log into 143 with their GitHub account.

### Create the app

1. Go to **GitHub Settings > Developer settings > OAuth Apps > [New OAuth App](https://github.com/settings/applications/new)**
2. Fill in the form:

| Field | Value |
|-------|-------|
| Application name | `143` (or any name you like) |
| Homepage URL | `{BASE_URL}` |
| Authorization callback URL | `{BASE_URL}/api/v1/auth/github/callback` |

3. Click **Register application**
4. On the next page, note the **Client ID**
5. Click **Generate a new client secret** and copy the secret immediately (you won't see it again)

### Configure 143

Add to your `.env` (or `.env.local`):

```env
GITHUB_OAUTH_CLIENT_ID=your_client_id
GITHUB_OAUTH_CLIENT_SECRET=your_client_secret
```

If your `BASE_URL` doesn't match the callback URL registered in GitHub (e.g. you're behind a reverse proxy or using a tunnel), set the redirect URI explicitly:

```env
GITHUB_OAUTH_REDIRECT_URI=https://your-public-domain.com/api/v1/auth/github/callback
```

That's it for OAuth. This only requests `read:user` and `user:email` scopes — just enough to identify who's logging in.

## Part 2: GitHub App

This is the main integration that lets 143 create branches, push commits, open PRs, and receive webhook events.

### Step 1: Create the app

1. Go to **GitHub Settings > Developer settings > GitHub Apps > [New GitHub App](https://github.com/settings/apps/new)**
2. Fill in the basic info:

| Field | Value |
|-------|-------|
| GitHub App name | `143-dev` (must be globally unique on GitHub) |
| Homepage URL | `{BASE_URL}` |

### Step 2: Configure the callback and webhook URLs

| Field | Value |
|-------|-------|
| User authorization callback URL | `{BASE_URL}/api/v1/users/me/github/callback` |
| Setup URL | `{BASE_URL}/settings/integrations/github/setup` |
| Webhook URL | `{BASE_URL}/api/v1/webhooks/github` |
| Webhook secret | Generate a strong random string (save it — you'll need it for the env var) |

To generate a webhook secret:

```bash
openssl rand -hex 32
```

Make sure **Active** is checked under the Webhook section.

If you are upgrading an existing GitHub App, keep any existing callback URLs that your current install/connect flow still uses, and add the new user authorization callback URL alongside them. The PR-authorship flow uses the user callback URL above; the GitHub App install flow still uses the Setup URL.

### Step 3: Set permissions

Under **Permissions**, configure:

| Permission | Access | Why 143 needs it |
|---|---|---|
| **Repository > Contents** | Read & Write | Clone repos, create branches, push commits |
| **Repository > Pull requests** | Read & Write | Create and update PRs, read reviews |
| **Repository > Workflows** | Read & Write | Push commits that add or modify `.github/workflows/*.yml` files |
| **Repository > Issues** | Read | Reference issues from commits and PRs |
| **Repository > Checks** | Read | Monitor CI status on PRs |
| **Repository > Deployments** | Read | Detect when fixes are deployed |
| **Repository > Metadata** | Read | Required for all GitHub Apps (auto-selected) |
| **Organization > Members** | Read | Sync org membership rosters for GitHub organization auto-join |

### Step 4: Subscribe to events

Under **Subscribe to events**, check:

- [x] **Installation** — know when the app is installed/uninstalled
- [x] **Installation repositories** — know when repos are added/removed
- [x] **Pull request** — track PR lifecycle (merge, close)
- [x] **Pull request review** — capture review decisions (approved, changes requested)
- [x] **Pull request review comment** — capture inline review comments
- [x] **Issue comment** — trigger automations from PR conversation comments
- [x] **Deployment status** — detect deploys after merge
- [x] **Organization** — keep GitHub organization auto-join rosters current when members are added, removed, or the org is renamed

### Step 5: Choose where the app can be installed

- **Only on this account** — for personal use or a single org
- **Any account** — if you want others to install your app (you can change this later)

### Step 6: Create the app

Click **Create GitHub App**. You'll be taken to the app's settings page.

Note the **App ID** displayed near the top of the page.

### Step 7: Generate a private key and client secret

1. Scroll down to **Private keys**
2. Click **Generate a private key**
3. A `.pem` file will download — this is your RSA private key
4. In the app settings, note the **Client ID**
5. Under **Client secrets**, click **Generate a new client secret** and copy it immediately

Keep both the private key and client secret secure. You'll need them for the environment variables.

### Step 8: Configure 143

Add to your `.env` (or `.env.local`):

```env
GITHUB_APP_ID=123456
GITHUB_APP_CLIENT_ID=Iv1.0123456789abcdef
GITHUB_APP_CLIENT_SECRET=your_github_app_client_secret
GITHUB_APP_PRIVATE_KEY="-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBA...\n-----END RSA PRIVATE KEY-----"
GITHUB_WEBHOOK_SECRET=your_webhook_secret_from_step_2
```

The client ID and client secret are used for the on-demand "Create PR as me" flow. They are separate from the private key used to mint installation tokens.

**Private key formatting:** The key must be the full PEM contents. You have two options:

- **Inline with escaped newlines**: Replace each newline in the `.pem` file with `\n` and wrap in quotes (shown above)
- **Multiline in `.env`**: Some dotenv loaders support multiline values. Check your deployment platform's docs.

### Step 9: Install the app on your org/repos

1. Go to your GitHub App's settings page
2. In the left sidebar, click **Install App**
3. Choose the account (your user or an org)
4. Select **All repositories** or choose specific repos
5. Click **Install**

When the app is installed, GitHub sends an `installation` webhook to 143. This automatically registers the installation and creates repository records in the database.

## Verifying the setup

Restart 143 after setting the environment variables. The startup logs will show:

```
feature status  configured=true  feature="GitHub OAuth"  enables=login
feature status  configured=true  feature="GitHub App"    enables="webhooks, PRs"
```

If either shows `configured=false`, double-check that the corresponding env vars are set and non-empty.

## How it works

Once configured, the flow is:

1. **User logs in** via GitHub OAuth — 143 creates a user record with their GitHub profile
2. **Admin installs the GitHub App** on their org — 143 receives the `installation` webhook and stores the installation ID + repo list
3. **When 143 needs to act** (create a PR, push a commit), it signs a JWT with the App's private key, exchanges it for a short-lived installation token (valid 1 hour), and uses that token for API calls
4. **When a user wants a PR created as themselves**, 143 sends them through the GitHub App user authorization flow at `/api/v1/users/me/github/callback`, stores a GitHub App user token, and then reuses or refreshes that token for future PRs
5. **GitHub sends webhooks** when PRs are reviewed, merged, or closed — 143 updates its records and triggers follow-up actions (deploy detection, review feedback loops, etc.)

## Local development tips

### Webhook tunneling

GitHub needs to reach your webhook endpoint. Use a tunnel:

```bash
# ngrok
ngrok http 8080
# Use the https://xxx.ngrok.io URL as your BASE_URL

# cloudflared
cloudflared tunnel --url http://localhost:8080
```

Update the GitHub OAuth callback URL, the GitHub App user authorization callback URL, and the GitHub App webhook URL to use the tunnel URL. Remember to update these when the tunnel URL changes.

### Testing webhooks

GitHub App settings have a **Recent Deliveries** tab under Advanced. You can inspect payloads and redeliver events for debugging.

### Separate apps for dev and prod

Create separate GitHub OAuth Apps and GitHub Apps for development and production. This avoids webhook conflicts and lets you use different callback URLs.

## Troubleshooting

| Problem | Fix |
|---------|-----|
| `configured=false` for GitHub App at startup | Check that `GITHUB_APP_ID` is a number (not empty) and `GITHUB_APP_PRIVATE_KEY` is set |
| First-time `Create PR` fails with `GITHUB_APP_USER_AUTH_NOT_CONFIGURED` | Check that `GITHUB_APP_CLIENT_ID` and `GITHUB_APP_CLIENT_SECRET` are set and that the GitHub App has a user authorization callback URL pointing to `{BASE_URL}/api/v1/users/me/github/callback` |
| Webhook signature verification fails (401) | Make sure `GITHUB_WEBHOOK_SECRET` matches what you entered in the GitHub App settings |
| "Resource not accessible by integration" on API calls | The app is missing a required permission — check the permissions table above and update in GitHub App settings |
| `refusing to allow a GitHub App to ... workflow '.github/workflows/...' without 'workflows' permission` on push | Add **Workflows: Read & Write** in the GitHub App permissions, then accept the new permission on each installation (GitHub emails the org owner). Existing installation tokens are cached up to 1 hour — restart workers or wait for the cache to expire after accepting |
| PRs aren't being created | Verify the app is installed on the target repo and has Contents + Pull Requests write access |
| Webhooks not arriving | Check that the webhook URL is correct and reachable. Use the Recent Deliveries tab in GitHub App settings to debug |
| "redirect_uri is not associated with this application" | Your `BASE_URL` doesn't match the callback URL registered in GitHub. For login, update the GitHub OAuth App callback URL or set `GITHUB_OAUTH_REDIRECT_URI`. For PR authorship, update the GitHub App user authorization callback URL to `{BASE_URL}/api/v1/users/me/github/callback` |
| Private key parse error | Make sure the full PEM is included, including `-----BEGIN RSA PRIVATE KEY-----` and `-----END RSA PRIVATE KEY-----` |
