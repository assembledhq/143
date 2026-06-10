# Installing the 143-tools CLI

`143-tools` is the laptop-side companion to 143: one binary that logs into
your 143 server with GitHub, exposes every integration your org has
connected to your local coding agents (via MCP or shell commands), and
drives previews from your checkout.

## Install

Ask an org admin for the install link (Settings → Team → CLI install links),
or use the tokenless form if you already have a 143 account:

```bash
# With a join link (also creates your account + org membership on first login):
curl -fsSL https://YOUR-SERVER/install/143j_XXXXXXXX | sh

# Tokenless (existing users, new laptop):
curl -fsSL https://YOUR-SERVER/install.sh | sh
```

The installer detects your OS/arch (darwin/linux × amd64/arm64), verifies
the binary checksum, installs to `~/.local/bin/143-tools`, writes
`~/.config/143-tools/config.json` with the server URL baked in, and chains
straight into `143-tools login` — your browser opens for GitHub sign-in as
the install finishes.

## Day-2 commands

```bash
143-tools whoami                  # user, org, role, token prefix, server
143-tools logout                  # revokes this device's token server-side
143-tools update                  # re-download the binary from the server
143-tools preview create --wait   # repo/branch inferred from the cwd → prints the preview URL
```

## Local agent gateway

Register the CLI as an MCP server with your coding agent:

```bash
claude mcp add 143 -- 143-tools mcp serve
```

Every integration tool the org has connected (Sentry, Linear, Notion,
CircleCI, Mezmo, previews, ...) becomes available to the agent. Tool calls
execute on the 143 server with org credentials — no provider tokens ever
land on your machine — and each call is audited per-user. Agents that
prefer shell tools can also invoke `143-tools <namespace> <action>`
directly; both paths share the same backend.

## Self-hosting note: reverse-proxy rules

The install and download routes are served by the Go API server but live
outside `/api`. The bundled production Caddyfile already routes them; if
you run your own proxy, send these paths to the API upstream (before any
frontend fallthrough), HTTPS-only:

```text
/install.sh   → api:8080
/install/*    → api:8080
/download/*   → api:8080
```

Also make sure your proxy/access logs redact the `/install/{token}` path
segment — the join token deliberately rides in the URL.

## Token hygiene

- Join tokens (`143j_...`) grant org membership only — never API access.
  They are multi-use, revocable one-click from the Team settings page, and
  support optional expiry / max-uses.
- CLI tokens (`143u_...`) are per-user, per-device, stored hashed, and have
  a 90-day sliding expiry. Revoke a device from Settings → My settings →
  CLI sessions.
- If you run secret scanning (gitleaks, GitHub custom patterns), register
  the patterns `143u_[A-Za-z0-9_-]{8,}` and `143j_[A-Za-z0-9]{12,64}`.
