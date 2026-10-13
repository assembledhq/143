# Product and documentation screenshots

These assets are captures of an isolated local demo populated from the canonical `.143/seed/` fragments. The identity, sessions, PRs, integration state, and preview records are synthetic. Two browser fixtures supply a synthetic GitHub account and the seeded PR's health presentation without provider credentials. These captures are not production measurements or customer evidence.

To regenerate:

1. Create a disposable PostgreSQL database, apply the repository migrations, and apply the seed fragments in lexical order. Run the seed safety checks before capture.
2. Start the API against that database with `DEMO_MODE=true` and `MODE=api`, using no production credentials or env bundles.
3. Install the locked frontend dependencies with Node 24. Build and serve the frontend against the demo API. The existing preview build path (`ONEFORTYTHREE_ENV=preview`) is available for local screenshot generation; run ordinary type/lint/build verification separately.
4. Install the matching Playwright Chromium browser if it is not already present, then run from `frontend`:

   ```bash
   DEMO_CAPTURE_URL=http://127.0.0.1:3000 node scripts/capture-product-screenshots.mjs
   ```

The capture script accepts only loopback targets, verifies demo mode and the canonical synthetic identity/session, and blocks browser requests to external origins. It keeps authentication cookies in memory and writes only screenshot assets plus `captures.json`. `DEMO_CAPTURE_OUTPUT` can direct provisional captures to a temporary directory for inspection before publication.

Inspect every regenerated frame, including identity, session lists, conversations, commands, integration accounts, and preview panels. Keep the asset names stable so their landing-page and public-doc consumers update together. Preview captures show the seeded preview-management workspace rather than a live remote browser.
