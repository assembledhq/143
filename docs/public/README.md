# Public Docs Source

> **Status:** Planned, not yet wired to the website

This directory is reserved for public documentation that should be rendered on `143.dev/docs`.

The implementation plan lives in [`../design/future/85-public-docs-fumadocs/`](../design/future/85-public-docs-fumadocs/). Until that design is implemented, public-facing guide source remains in [`../guides/`](../guides/) and self-hosting source remains in [`../self-hosting/`](../self-hosting/).

Target structure:

```text
docs/public/
├── index.mdx
├── meta.json
├── getting-started/
├── guides/
├── self-hosting/
├── reference/
└── images/
```

Only content in this directory should be routed publicly by the future Fumadocs integration. Internal design docs, production-debugging notes, and implementation records should not be exposed automatically.
