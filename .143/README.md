# `.143/` — Repo-level 143 config

This directory holds configuration that 143 reads directly from your repository. Commit it like any other source file.

## Files

- **`preview.json`** — declares how to run a live preview of this app inside a 143 session. See [`docs/previews.md`](../docs/previews.md) for the full reference.

## Minimal preview example

```json
{
  "name": "my-app",
  "primary": "app",
  "services": {
    "app": {
      "command": ["npm", "run", "dev"],
      "port": 3000,
      "ready": { "http_path": "/" }
    }
  },
  "credentials": { "mode": "none" },
  "network": { "mode": "managed" }
}
```

Drop this in as `.143/preview.json`, open a session on the repo, and click **Start Preview**.

For multi-service setups (backend + frontend + platform Postgres), secrets, or trust-split details, see [`docs/previews.md`](../docs/previews.md).
