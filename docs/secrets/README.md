# Secrets Management

143 uses environment variables for all configuration. This guide covers how to manage them securely across different environments.

## Tiers

| Tier | When to use | How |
|------|-------------|-----|
| **`.env` file** | Local development | Edit `.env` directly (gitignored) |
| **SOPS + age** | Shared/staging, small teams | Encrypted `.env.enc` committed to git |
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

[SOPS](https://github.com/getsops/sops) encrypts your `.env` file using [age](https://age-encryption.org/) keys. The encrypted file (`.env.enc`) is safe to commit — only people with the matching private key can decrypt it.

### Initial setup (one-time per developer)

```bash
# 1. Install dependencies
brew install sops age    # macOS
# sudo apt install sops age   # Debian/Ubuntu

# 2. Generate your age keypair
make secrets-setup

# 3. Copy the public key from the output and paste it into .sops.yaml
#    (replace the age1TODO_REPLACE_WITH_YOUR_PUBLIC_KEY placeholder)

# 4. Fill in .env with your real secrets, then encrypt
make secrets-encrypt

# 5. Commit the encrypted file
git add .env.enc .sops.yaml
git commit -m "Add encrypted dev secrets"
```

This creates a keypair at `~/.config/sops/age/keys.txt`. The private key stays on your machine. The public key goes into `.sops.yaml` so SOPS knows who can decrypt.

### New machine setup

If `.env.enc` is already committed and you have the age private key on the new machine:

```bash
# setup.sh auto-detects .env.enc and decrypts it
./setup.sh

# Or decrypt manually
make secrets-decrypt
```

The setup script checks for `.env.enc` + an age key before falling back to `.env.example`. This means `git clone && ./setup.sh` gives returning devs a fully configured environment automatically.

### Per-environment files

Use the `ENV` variable to manage staging or production secrets separately:

| Command | Plaintext file | Encrypted file |
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

# 2. Add their key to .sops.yaml (comma-separated on the age: line)
#    age: >-
#      age1YOUR_KEY,age1THEIR_KEY

# 3. Re-encrypt all files with the updated key list
make secrets-rotate

# 4. Commit
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
git add .env.enc && git commit -m "Update secrets"
```

### File layout

```
.sops.yaml              # which age public keys can decrypt (committed)
.env.example            # template with empty values (committed)
.env                    # local dev secrets (gitignored)
.env.enc                # encrypted dev secrets (committed, safe)
.env.staging            # staging secrets (gitignored)
.env.staging.enc        # encrypted staging secrets (committed, safe)
.env.local              # personal overrides (gitignored, never encrypted)
```

### How it works

```
.env (plaintext, gitignored)
  ↕  make secrets-encrypt / secrets-decrypt
.env.enc (encrypted, committed)
  ↕  sops + age private key
decrypted at runtime
```

### Troubleshooting

**"No .env file to encrypt"** — Create one first: `cp .env.example .env` and fill in values.

**"Could not decrypt (wrong key?)"** — Your age private key doesn't match any public key in `.sops.yaml`. Ask a team member to add your key and run `make secrets-rotate`.

**"MAC mismatch"** — The `.enc` file was edited outside of SOPS. Re-encrypt from the plaintext source.

**Forgot to update .sops.yaml** — The TODO placeholder key causes encryption to fail. Run `make secrets-setup`, copy your public key into `.sops.yaml`, then retry.

## Tier 3: Cloud secret managers

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

Set `LLM_MODEL` to a model name (e.g., `claude-sonnet-4-5`, `gpt-4o`) and provide at least one provider API key. The system automatically falls back through configured providers:

| Provider | API key variable | Notes |
|----------|-----------------|-------|
| Anthropic | `ANTHROPIC_API_KEY` | Claude models natively |
| OpenAI | `OPENAI_API_KEY` | Chat Completions or Responses API (set `OPENAI_API_TYPE`) |
| OpenRouter | `OPENROUTER_API_KEY` | Universal fallback, routes to any model |

Configure multiple providers for automatic fallback. If the primary provider returns a rate limit or server error, the next provider in the chain is tried.
