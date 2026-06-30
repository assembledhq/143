# Self-Hosting 143

Most people don't need this — the hosted version at [143.dev](https://www.143.dev) is already running and pre-configured. These guides are for teams running their own instance of 143 (on their own infrastructure, with their own GitHub App, behind their own domain).

For general usage docs that apply regardless of who's hosting, see [`../guides/`](../guides/).

## Guides

- **[GitHub App setup](github-app-setup.md)** — create your own GitHub OAuth App + GitHub App and wire them into your deployment.
- **[Single-node deployment](single-node.md)** — run the production-shaped stack on one Linux host with Docker Compose.
- **[Public demo deployment](public-demo.md)** — run the read-only seeded demo on one separate host with local Postgres and Redis.
- **[Production deployment checklist](production-deployment-checklist.md)** — minimum steps to deploy the 143 backend + frontend in production.
- **[Platform LLM](platform-llm.md)** — configure the small background-features model (session titles, PR descriptions, validation, prioritization).
- **[CLI install](../guides/cli-install.md#self-hosting-note-reverse-proxy-rules)** — reverse-proxy rules for the `143-tools` installer/download routes (only needed if you replace the bundled Caddyfile).

For local development (running 143 on your laptop while working on the codebase), see [`../local-development.md`](../local-development.md) instead.
