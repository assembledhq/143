# Secrets Management

143 uses environment variables for all configuration. This guide covers how to manage them securely across different environments.

## Tiers

| Tier | When to use | How |
|------|-------------|-----|
| **`.env` file** | Local development | Edit `.env` directly (gitignored) |
| **SOPS + age** | Shared/staging, small teams | Encrypted `.env.enc` in a **private** secrets repo |
| **Cloud secret manager** | Production with rotation needs | AWS Secrets Manager, GCP Secret Manager, etc. |

Most contributors only need Tier 1. Tier 2 is useful when multiple people need the same secrets without a shared secret manager.

## Tier 1: Local `.env`

The setup script copies `.env.example` to `.env`. Edit it with your values:

```bash
cp .env.example .env
# edit .env with your API keys
```

You can also create `.env.local` for personal overrides that take precedence over `.env`. Loading order (highest priority wins):

1. Real environment variables (CI, Docker, shell exports)
2. `.env.local` (personal overrides, gitignored)
3. `.env` (shared defaults, gitignored)

## Tier 2: SOPS + age

[SOPS](https://github.com/getsops/sops) encrypts your `.env` file using [age](https://age-encryption.org/) keys. Only people with a matching private key can decrypt the result.

### The private secrets repo

Encrypted bundles (`.env*.enc`) and `.sops.yaml` are **never committed to this repo** — it is public, and ciphertext published in a public repo can't be unpublished if a decryption key ever leaks. They live in a private sibling repo instead:

```
github.com/<your-org>/143         # this repo (public)
github.com/<your-org>/143-infra   # private: .sops.yaml + .env*.enc
```

All `make secrets-*` targets, the deploy scripts, and `setup.sh` resolve the bundles from `SECRETS_DIR`. The default is a `143-infra` clone next to the **main** checkout — resolved via the shared git dir (`git rev-parse --git-common-dir`), so it works identically from linked worktrees (Claude Code, Codex, Conductor) without any per-worktree setup. Override with `make secrets-edit SECRETS_DIR=/path/to/checkout` if you keep it elsewhere.

Unlike the code repo, `143-infra` never needs worktrees or branches: agent sessions only read (decrypt) from it, concurrent reads are safe, and the occasional secret edit lands on its `main`. Just remember to push it after edits — CI deploys read the GitHub copy while local make targets read your clone.

Bootstrap the private repo once:

```bash
gh repo create <your-org>/143-infra --private --description "Private secrets for 143 deploys"
git clone git@github.com:<your-org>/143-infra.git ../143-infra
cp .sops.yaml.example ../143-infra/.sops.yaml   # or write .sops.yaml by hand
make secrets-encrypt                             # .env → ../143-infra/.env.enc
cd ../143-infra && git add -A && git commit -m "Seed secrets" && git push
```

CI deploys check the private repo out via the `INFRA_REPO_TOKEN` repository secret — a fine-grained PAT with read-only contents access to `143-infra` only (see `.github/workflows/deploy.yml`).

### Initial setup (one-time per developer)

```bash
# 1. Install dependencies
brew install sops age    # macOS
# sudo apt install sops age   # Debian/Ubuntu

# 2. Generate your age keypair
make secrets-setup

# 3. Add the SOPS key file to your shell profile so decryption works
#    automatically in every terminal session:
echo 'export SOPS_AGE_KEY_FILE="$HOME/.config/sops/age/keys.txt"' >> ~/.bash_profile
source ~/.bash_profile
#    (Use ~/.zshrc instead if you use zsh)

# 4. Copy the public key from the output and paste it into
#    ../143-infra/.sops.yaml (replace the age1TODO... placeholder, or
#    add it comma-separated with existing keys on the age: line)

# 5. Fill in .env with your real secrets, then encrypt
make secrets-encrypt

# 6. Commit the encrypted file in the PRIVATE repo
cd ../143-infra
git add .env.enc .sops.yaml
git commit -m "Add encrypted dev secrets"
```

This creates a keypair at `~/.config/sops/age/keys.txt`. The private key stays on your machine. The public key goes into the private repo's `.sops.yaml` so SOPS knows who can decrypt.

> **Important**: Without the `SOPS_AGE_KEY_FILE` export in your shell profile, SOPS won't find your private key and decryption will fail with "no master key was able to decrypt the file".

### New machine setup

If the private secrets repo exists and you have the age private key on the new machine:

```bash
# 1. Copy your age private key to the new machine
#    (from ~/.config/sops/age/keys.txt on your old machine)
mkdir -p ~/.config/sops/age
cp /path/to/keys.txt ~/.config/sops/age/keys.txt
chmod 600 ~/.config/sops/age/keys.txt

# 2. Add the export to your shell profile
echo 'export SOPS_AGE_KEY_FILE="$HOME/.config/sops/age/keys.txt"' >> ~/.bash_profile
source ~/.bash_profile

# 3. Clone the private secrets repo next to this one
git clone git@github.com:<your-org>/143-infra.git ../143-infra

# 4. setup.sh auto-detects ../143-infra/.env.enc and decrypts it
./setup.sh

# Or decrypt manually
make secrets-decrypt
```

The setup script checks for `$SECRETS_DIR/.env.enc` + an age key before falling back to `.env.example`. This means cloning both repos and running `./setup.sh` gives returning devs a fully configured environment automatically.

### Per-environment files

Use the `ENV` variable to manage staging or production secrets separately:

| Command | Plaintext file (repo root) | Encrypted file (in `SECRETS_DIR`) |
|---------|---------------|----------------|
| `make secrets-encrypt` | `.env` | `.env.enc` |
| `make secrets-encrypt ENV=staging` | `.env.staging` | `.env.staging.enc` |
| `make secrets-encrypt ENV=production` | `.env.production` | `.env.production.enc` |

Decryption and editing follow the same pattern:

```bash
make secrets-decrypt ENV=staging     # .env.staging.enc → .env.staging
make secrets-edit ENV=staging        # edit .env.staging.enc in-place
```

To use staging secrets locally, copy or symlink:

```bash
cp .env.staging .env
# or: ln -sf .env.staging .env
```

### Adding a team member

```bash
# 1. New member runs: make secrets-setup
#    They send you their public key (starts with age1...)

# 2. Add their key to ../143-infra/.sops.yaml (comma-separated on the age: line)
#    age: >-
#      age1YOUR_KEY,age1THEIR_KEY

# 3. Re-encrypt all files with the updated key list
make secrets-rotate

# 4. Commit in the private repo (and grant them access to it on GitHub)
cd ../143-infra
git add .sops.yaml .env*.enc
git commit -m "Add <name> to secrets access"
```

### Removing a team member

1. Remove their public key from `.sops.yaml`
2. Run `make secrets-rotate`
3. **Rotate the actual secret values** (API keys, tokens, etc.) since the removed member had access
4. Re-encrypt: `make secrets-encrypt` (and `ENV=staging`, etc.)
5. Commit everything

### Daily workflow

```bash
# Decrypt secrets (new machine or after pulling changes)
make secrets-decrypt

# Edit encrypted secrets in-place (opens $EDITOR)
make secrets-edit

# After changing .env, re-encrypt before committing
make secrets-encrypt
cd ../143-infra && git add .env.enc && git commit -m "Update secrets"
```

### File layout

```
143/ (this repo, public)
  .sops.yaml.example    # template for the private repo's .sops.yaml
  .env.example          # template with empty values (committed)
  .env                  # local dev secrets (gitignored)
  .env.staging          # staging secrets (gitignored)
  .env.production       # production secrets (gitignored)
  .env.local            # personal overrides (gitignored, never encrypted)

143-infra/ (private repo, default SECRETS_DIR)
  .sops.yaml            # which age public keys can decrypt
  .env.enc              # encrypted dev secrets
  .env.staging.enc      # encrypted staging secrets
  .env.production.enc   # encrypted production secrets
```

### How it works

```
.env (plaintext, gitignored, repo root)
  ↕  make secrets-encrypt / secrets-decrypt
$SECRETS_DIR/.env.enc (encrypted, committed in the private repo)
  ↕  sops + age private key
decrypted at runtime
```

### Troubleshooting

**"No .env file to encrypt"** — Create one first: `cp .env.example .env` and fill in values.

**"Could not decrypt (wrong key?)"** — Your age private key doesn't match any public key in `.sops.yaml`. Ask a team member to add your key and run `make secrets-rotate`.

**"MAC mismatch"** — The `.enc` file was edited outside of SOPS. Re-encrypt from the plaintext source.

**Forgot to update .sops.yaml** — The TODO placeholder key causes encryption to fail. Run `make secrets-setup`, copy your public key into `.sops.yaml`, then retry.

**"no master key was able to decrypt the file"** — SOPS can't find your age private key. Make sure `SOPS_AGE_KEY_FILE` is exported in your shell profile and points to `~/.config/sops/age/keys.txt`. Run `source ~/.bash_profile` (or `~/.zshrc`) to pick it up in the current session.

## Tier 3: Production via SOPS at boot

Production secrets are encrypted in `.env.production.enc` using the same SOPS + age workflow. This lets you version-control production config and deploy it through your CI/CD pipeline instead of managing env vars in a dashboard.

> **Note**: the bundle is no longer baked into the server image (the image is public on GHCR), so this flow requires a deploy target that can stage the file next to the container — the fleet deploy does this by bind-mounting `/opt/143/.env.production.enc` into the workdir. On image-only platforms (e.g. Render via `render.yaml`), set env vars individually in the platform dashboard instead, or bake the bundle into a private image build of your own.

### How it works

1. Production secrets live in `.env.production.enc` (committed in the private secrets repo)
2. The deploy stages the bundle on the host and bind-mounts it into the container; at boot, `docker-entrypoint.sh` decrypts it using an age private key supplied as the single `SOPS_AGE_KEY` env var
3. The decrypted values are exported as environment variables before the server starts

### Setup (one-time)

```bash
# 1. Generate a dedicated deploy key (separate from your personal key)
age-keygen -o /tmp/deploy-key.txt
# Note the public key from the output (age1...)

# 2. Add the deploy public key to .sops.yaml alongside your personal key
#    on the production rule's age: line (comma-separated):
#    age: >-
#      age1YOUR_KEY,age1DEPLOY_KEY

# 3. Re-encrypt production secrets so the deploy key can decrypt
make secrets-rotate

# 4. In Render, set a single env var:
#    SOPS_AGE_KEY = <paste the full contents of /tmp/deploy-key.txt>
#    (includes the private key line starting with AGE-SECRET-KEY-...)

# 5. Delete /tmp/deploy-key.txt from your local machine
rm /tmp/deploy-key.txt

# 6. Commit .sops.yaml and .env.production.enc
cd ../143-infra
git add .sops.yaml .env.production.enc
git commit -m "Add deploy key for Render production"
```

### Deploy command

Update your Render start command (or Dockerfile entrypoint) to decrypt at boot:

```bash
# Install sops + age in your Docker image, then:
export SOPS_AGE_KEY_FILE=/tmp/age-key.txt
echo "$SOPS_AGE_KEY" > "$SOPS_AGE_KEY_FILE"
sops --decrypt .env.production.enc > .env.production
set -a && source .env.production && set +a
exec ./server   # or whatever your start command is
```

This means the only secret stored in Render is the age private key (`SOPS_AGE_KEY`). All other secrets are managed in the private secrets repo via `.env.production.enc`.

### Updating production secrets

```bash
# Decrypt, edit, re-encrypt, commit
make secrets-decrypt ENV=production
# edit .env.production
make secrets-encrypt ENV=production
cd ../143-infra && git add .env.production.enc && git commit -m "Update production secrets"
git push    # Render redeploys automatically
```

Or use the in-place editor:

```bash
make secrets-edit ENV=production    # opens $EDITOR, re-encrypts on save
cd ../143-infra && git add .env.production.enc && git commit -m "Update production secrets"
```

### Why this is better than Render's env var UI

- **Version controlled**: every secret change is a git commit with full history
- **Reviewable**: secret changes can go through PRs like code changes
- **Reproducible**: `git clone && decrypt` gives you the full production config
- **Single source of truth**: no drift between what's in git and what's in Render
- **Easy rollback**: `git revert` rolls back secret changes instantly

## Tier 3 (alternative): Cloud secret managers

For production deployments that need automatic rotation, use your cloud provider's secret manager and inject values as environment variables at deploy time:

- **AWS**: Secrets Manager or SSM Parameter Store
- **GCP**: Secret Manager
- **Railway/Render/Fly**: Built-in secret management

No code changes needed — 143 reads from environment variables regardless of how they're set.

## What each variable does

See `.env.example` for the full list. Key groups:

| Group | Required for | Variables |
|-------|-------------|-----------|
| Database | Everything | `DATABASE_URL` |
| GitHub OAuth | User login | `GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET` |
| GitHub App | Webhooks, PRs | `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY`, `GITHUB_WEBHOOK_SECRET` |
| LLM | AI features | `LLM_MODEL` + at least one provider key |
| Sentry | Issue ingestion | `SENTRY_WEBHOOK_SECRET` |
| Linear | Issue ingestion | `LINEAR_WEBHOOK_SECRET` |

### LLM providers

Set `LLM_MODEL` to a model name (e.g., `claude-sonnet-4-6`, `gpt-5.4-mini`) and provide at least one provider API key. The system automatically falls back through configured providers:

| Provider | API key variable | Notes |
|----------|-----------------|-------|
| Anthropic | `ANTHROPIC_API_KEY` | Claude models natively |
| OpenAI | `OPENAI_API_KEY` | Chat Completions or Responses API (set `OPENAI_API_TYPE`) |
| OpenRouter | `OPENROUTER_API_KEY` | Universal fallback, routes to any model |

Configure multiple providers for automatic fallback. If the primary provider returns a rate limit or server error, the next provider in the chain is tried.
