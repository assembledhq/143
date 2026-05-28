# Design: Preview Settings Page

> **Status:** Implemented
> **Last reviewed:** 2026-05-27

The Preview settings page is the single admin-owned surface for preview runtime secrets and preview API tokens. Repository pages should link to preview actions, but they should not contain preview secret bundle create/edit controls.

## Goals

1. Rename the settings surface from **Preview API** to **Preview** because it now owns more than API tokens.
2. Make **Preview secrets** the primary use case on the page.
3. Keep **Preview API** visibly separate as the secondary use case.
4. Use the same inventory-table pattern already used for coding-agent auths, personal credentials, and usage breakdowns.
5. Preserve the repo-scoped backend model for preview secret bundles.
6. Avoid exposing secret values in list views, API responses, logs, audit events, or browser-visible config.

## Non-Goals

1. A general-purpose secret manager.
2. Moving preview secret bundle ownership out of repositories at the data-model level.
3. Supporting external secret sources beyond the existing bundle source model in this UI pass.
4. Making preview API tokens first-class personal API keys. They remain org-scoped admin controls for preview automation.

## Product Model

The page has two sections:

1. **Preview secrets**
   - Repo-scoped secret bundles used at preview runtime.
   - Primary workflow.
   - Admins choose a repository, scan bundle rows, and create/edit/test/delete bundles.

2. **Preview API**
   - Scoped preview API tokens for branch and pull request previews.
   - Secondary workflow.
   - Admins scan active tokens, create new tokens, and revoke existing tokens.

These two sections should live together on one page, but not compete visually. Secrets get the first section and the larger setup affordance. API tokens get a compact section below.

## Wireframe

Desktop:

```text
Preview
Configure preview secrets and API access.

Preview secrets                                      [New bundle]
Repository [ assembledhq/143                    v ]

┌─────────────────────────────────────────────────────────────────────────────┐
│ Bundle          Outputs                         Last changed       Actions │
├─────────────────────────────────────────────────────────────────────────────┤
│ assembled-dev   env DATABASE_URL, API_TOKEN      May 27, 2026      Test Edit Delete │
│ staging         file development.conf.json       May 22, 2026      Test Edit Delete │
└─────────────────────────────────────────────────────────────────────────────┘


Preview API                                         [Create token]

┌─────────────────────────────────────────────────────────────────────────────┐
│ Token           Scopes                         Repository access   Actions │
├─────────────────────────────────────────────────────────────────────────────┤
│ CI previews     create, read, stop              All repositories   Revoke  │
│ Docs preview    read                            assembledhq/docs   Revoke  │
└─────────────────────────────────────────────────────────────────────────────┘
```

Mobile:

```text
Preview

Preview secrets                     [New]
Repository
[ assembledhq/143              v ]

[ assembled-dev                         ]
  Outputs
  env DATABASE_URL, API_TOKEN
  Last changed
  May 27, 2026
  [Test] [Edit] [Delete]

[ staging                               ]
  Outputs
  file development.conf.json
  Last changed
  May 22, 2026
  [Test] [Edit] [Delete]


Preview API                         [Create]

[ CI previews                           ]
  Scopes
  create, read, stop
  Repository access
  All repositories
  [Revoke]
```

## Visual and Interaction Rules

- Use `PageContainer` and `PageHeader`.
- Sidebar label, page title, and browser title should say `Preview`, not `Preview API`.
- Keep the route as `/settings/previews` unless the implementation also adds a redirect from a new `/settings/preview` route. Do not break existing bookmarks during this pass.
- Use section headers outside cards:
  - `Preview secrets` with a `New bundle` button.
  - `Preview API` with a `Create token` button.
- Use the shared `Table` components for desktop inventories:
  - `Table`, `TableHeader`, `TableBody`, `TableRow`, `TableHead`, `TableCell`.
  - Follow the existing credential table pattern: entity columns first, metadata/status in the middle, actions right-aligned.
- Use stacked mobile rows instead of forcing the table layout on small screens. Account settings already uses this pattern for credential stacks.
- Use `Badge` for output summaries, scopes, statuses, and repository-access summaries.
- Use `Button` with lucide icons for actions (`Plus`, `KeyRound`, `TestTube2`, `Pencil`, `Trash2`).
- Use `EmptyState` inside the table/card area when there are no bundles or no tokens.
- Creation and editing should happen in a dialog or sheet, not as a permanently expanded inline form. The inventory table should stay scannable.

## Preview Secrets Section

### Repository Context

Preview secret bundles are currently repo-scoped. The settings page must include a repository selector inside the Preview secrets section. The selected repository applies only to secret bundles, not to preview API tokens.

Implementation guidance:

- Query repositories with the existing repository list API.
- Default selection:
  1. URL param such as `?repo=<repository_id>` when present and valid.
  2. First active repository when no param is present.
  3. Empty state when there are no repositories.
- Store the selected repository in URL state if practical so admins can share a direct link to a repo's preview secrets.
- If the selected repository is disconnected or not found, fall back to the first active repository and clear/replace the invalid URL param.

### Bundle Table

Columns:

| Column | Content |
|---|---|
| Bundle | Bundle name. Optionally include source type as subdued metadata. |
| Outputs | Badges such as `env DATABASE_URL`, `env API_TOKEN`, `json development.conf.json`, `raw .env`. |
| Last changed | `created_at` from the active version until a version-specific display timestamp exists. |
| Actions | `Test`, `Edit`, `Delete`. Keep destructive action visually secondary. |

Do not display secret values. Do not display decrypted source data. List APIs should expose only bundle metadata and output names.

### New/Edit Bundle Dialog or Sheet

Use the same form for creating and editing.

Fields:

1. Repository
   - Selectable for `New bundle`, defaulting to the currently filtered repository so admins can choose the target repo without leaving the dialog.
   - Read-only or disabled for `Edit`; moving bundles between repos is out of scope.
2. Bundle name
   - Required.
   - Trim whitespace.
3. Stored secrets
   - Structured key/value rows.
   - Used only for environment-variable delivery, where each key becomes a preview runtime env var.
   - Key input should normalize to a conservative env-style identifier.
   - Value input should be `type="password"`.
   - Existing secret values must not be fetched or shown. Editing should require re-entering any changed value.
4. Delivery outputs
   - Bundle create/edit uses delivery-method tabs so admins explicitly choose either environment variables or a generated secret file.
   - Environment-variable delivery maps each stored secret key to a preview runtime env var.
   - File delivery should read like saving a private file, not composing resolver JSON: admins enter the runtime file path, choose `Raw text` or `JSON`, and paste the exact file contents. The frontend translates that into one encrypted managed value plus one generated file output in the existing backend model.
   - Copy should make clear that users need one delivery method, not both.
   - Validate output path rules client-side where possible, but rely on backend validation as source of truth.
5. Actions
   - `Test bundle`
   - `Save`
   - `Cancel`

For edit flows, prefer patching unchanged metadata without requiring plaintext secret re-entry when backend support exists. If backend support does not allow preserving encrypted values, make the form copy explicit by using an empty password field placeholder such as `Leave blank to keep existing value` only after the API can actually honor that behavior. Do not imply secret preservation before it exists.

### Testing a Bundle

The `Test` action should call the id-based test endpoint for the active bundle. It should surface:

- `Ready` as a success toast and/or badge.
- Validation failures as an inline row error or toast with the backend's safe message.
- Network/server failures through the shared error treatment.

Test responses must not include secret values.

### Delete Behavior

Deletion should use a confirmation dialog because disabling a bundle can break preview startup for any repo config that references it.

Copy should name the bundle and repository. The confirmation should not mention or reveal secret values.

## Preview API Section

### Token Table

Columns:

| Column | Content |
|---|---|
| Token | Token name. Never the token value. |
| Scopes | Badges for `previews:create`, `previews:read`, `previews:stop`. |
| Repository access | `All repositories` or a count/list summary from `repository_ids`. |
| Last used | `last_used_at` when present, otherwise `Never`. |
| Actions | `Revoke`. |

The token secret should only be shown immediately after creation, matching common API-key behavior. After the admin leaves that state, the token value cannot be recovered.

### Create Token Dialog

Fields:

- Name.
- Scopes using checkboxes.
- Repository access using the same repository list query. Leave empty means all repositories.

On success:

- Show the created token value in a copyable one-time result area.
- Refresh the token table.
- Do not persist the token value in local storage or URL params.

## API and Data Considerations

### Existing Routes to Use

Preview secret bundles (routes called by this page are marked **used**):

- `GET /api/v1/repositories/{id}/preview-secret-bundles` — **used** (bundle list)
- `POST /api/v1/repositories/{id}/preview-secret-bundles` — **used** (create bundle)
- `PATCH /api/v1/preview-secret-bundles/{id}` — **used** (edit bundle)
- `DELETE /api/v1/repositories/{id}/preview-secret-bundles/{name}` — **used** (delete bundle)
- `POST /api/v1/preview-secret-bundles/{id}/test` — **used** (test bundle)
- `GET /api/v1/repositories/{id}/preview-secret-bundles/{name}` — available, not called by this page
- `GET /api/v1/preview-secret-bundles/{id}` — available, not called by this page
- `DELETE /api/v1/preview-secret-bundles/{id}` — available, not called by this page

Preview API tokens:

- `GET /api/v1/previews/api-tokens`
- `POST /api/v1/previews/api-tokens`
- `DELETE /api/v1/previews/api-tokens/{token_id}`

### Stale Client API to Remove

The current frontend client includes settings-level preview secret bundle calls under `/api/v1/settings/preview-secret-bundles`. Those routes are not the source of truth for the implemented repo-scoped bundle model. Replace page usage with the repository-scoped API client.

Do not add a second, settings-level backend API unless the data model changes to support org-global bundle identities. A settings page can still manage repo-scoped bundles by requiring repository selection.

### Query Keys

Use repo-specific query keys for bundles so invalidation does not refresh unrelated repositories:

```ts
queryKeys.repositories.previewSecretBundles(repositoryId)
```

Use a separate token query key for preview API tokens:

```ts
["preview-api-tokens"]
```

Repository list can use the existing repositories query key.

## Security and Privacy

- Never render decrypted secret values after save.
- Never put secret values in route params, query params, local storage, logs, toast messages, audit details, React Query keys, or test names.
- Keep password inputs controlled only for the active dialog/sheet.
- Clear secret form state on successful save, cancel, and dialog close.
- Audit events should include bundle ID, bundle name, repository ID, source type, output summaries, and action. They must not include source values or rendered file contents.
- Preview API tokens should be hashed server-side and shown only once on creation.
- Admin-only mutations must continue to use backend RBAC. Frontend role checks are only affordance hiding.

## Frontend Implementation Steps

1. Rename settings sidebar item from `Preview API` to `Preview`.
2. Rename `/settings/previews` page header from `Preview API` to `Preview`.
3. Remove `PreviewSecretBundlesSection` from the repository detail page.
4. Keep repository detail preview actions such as `Preview branch`; only remove secret bundle management.
5. Replace the current `/settings/previews` secret bundle form with:
   - Repository selector.
   - Bundle inventory table on desktop.
   - Stacked bundle rows on mobile.
   - New/edit bundle dialog or sheet.
   - Delete confirmation.
6. Keep preview API token management on the same page but move token creation into a dialog or sheet.
7. Update TypeScript types if needed. There should be one `PreviewSecretBundleSummary` shape matching the backend response. Avoid duplicate interface declarations with incompatible fields.
8. Update API tests for removed `/api/v1/settings/preview-secret-bundles` client calls.
9. Add page tests for the new central settings behavior.

## Backend Considerations

No backend data-model change is required for this UI pass if the page uses repo-scoped APIs.

Backend checks to confirm before implementation:

- List/read bundle endpoints remain available to all org roles that can view repositories.
- Create/update/delete/test endpoints remain admin-only.
- Every preview secret bundle query filters by `org_id` and `repository_id` where applicable.
- Summary responses expose output names and metadata only.
- Patch semantics are clear for encrypted source values. If partial source updates are not safe, the frontend must require complete source re-entry rather than pretending to preserve existing values.

If a future version needs org-global bundle reuse across repositories, design a separate bundle identity and repo binding model. Do not fake org-global bundles in the settings UI by dropping repository context.

## Edge Cases

- **No repositories:** Show an empty state in Preview secrets. Disable `New bundle`.
- **Repository has no bundles:** Show an inline empty state with `New bundle`.
- **Bundle required by repo config but missing:** This page does not need to parse every branch config in v1, but it should not block future missing-bundle hints from preview config detection.
- **Bundle name conflict:** Show backend conflict as an inline form error.
- **Invalid output JSON/file path:** Show backend validation error and keep the dialog open.
- **Secret value accidentally blank:** Treat blank values intentionally only if backend accepts them. Otherwise validate and explain before submit.
- **Changing output shape:** A saved output change may break previews. Keep `Test bundle` close to `Save`.
- **Deleting active bundle:** Confirm with repository and bundle name.
- **Token created with all repos:** Render as `All repositories`, not `0 repositories`.
- **Token repository allowlist references deleted/disconnected repo:** Render a count if names cannot be resolved; do not crash.
- **Mobile long names:** Bundle names, file paths, repository names, and token names should truncate or wrap without overlapping actions.

## Test Plan

Frontend tests from `frontend/`:

1. Repository detail page no longer renders `Preview secrets`, `Create or update`, or bundle save controls.
2. Settings sidebar renders `Preview`, not `Preview API`.
3. Preview settings page title renders `Preview`.
4. Preview secrets section loads repositories and then loads bundles for the selected repository.
5. Changing the selected repository refetches repo-scoped bundles.
6. Empty bundle state renders with a local `New bundle` action for admins.
7. Creating a bundle posts to `/api/v1/repositories/{id}/preview-secret-bundles`.
8. Testing a bundle posts to `/api/v1/preview-secret-bundles/{id}/test`.
9. Deleting a bundle requires confirmation and then calls the repo-scoped delete endpoint.
10. Preview API token creation still posts to `/api/v1/previews/api-tokens`.
11. Revoking a token still calls `/api/v1/previews/api-tokens/{token_id}`.
12. Mobile rendering uses stacked rows and does not expose a desktop-only table as the only readable UI.

Run after frontend changes:

```bash
cd frontend
npm run typecheck
npm run lint
npm run build
```

Backend tests are only required if route behavior, handler payloads, or models change. If Go code changes, run:

```bash
go vet ./...
go build ./...
go test ./...
```
