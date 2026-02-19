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

[SOPS](https://github.com/getsecrets/sops) encrypts your `.env` file using [age](https://age-encryption.org/) keys. The encrypted file (`.env.enc`) is safe to commit — only people with the matching private key can decrypt it.

### Initial setup (one-time per contributor)

```bash
# Install dependencies
brew install sops age    # macOS
# apt install sops age   # Linux

# Generate your age keypair
make secrets-setup
```

This creates a keypair at `~/.config/sops/age/keys.txt`. The command prints your **public key** — share it with the team so they can add it to `.sops.yaml`.

### Adding a team member

Add their public key to `.sops.yaml`:

```yaml
creation_rules:
  - path_regex: \.env\.enc$
    age: >-
      age1abc...,age1def...
```

Comma-separate multiple keys. Then re-encrypt:

```bash
make secrets-encrypt
```

### Daily workflow

```bash
# Decrypt secrets (new machine or after pulling changes)
make secrets-decrypt

# Edit encrypted secrets in-place
make secrets-edit

# After changing .env, re-encrypt before committing
make secrets-encrypt
git add .env.enc .sops.yaml && git commit -m "update secrets"
```

### How it works

```
.env (plaintext, gitignored)
  ↕  make secrets-encrypt / secrets-decrypt
.env.enc (encrypted, committed)
  ↕  sops + age private key
decrypted at runtime
```

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
