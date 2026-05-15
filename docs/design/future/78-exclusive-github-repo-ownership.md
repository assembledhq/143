# Design: Exclusive GitHub Repository Ownership

> **Status:** In Progress | **Last reviewed:** 2026-05-14

## Summary

One GitHub App installation can now be shared by multiple 143 organizations, but each GitHub repository has exactly one active 143 owner. The GitHub installation is the platform access grant; the repository row is the 143 organization ownership record.

Historical repository rows can remain disconnected in prior orgs so old sessions, projects, automations, and audit history stay readable. New work only uses active repository rows.

## Model

- `github_installations` stores global GitHub App installation identity keyed by `installation_id`.
- `github_installation_org_links` records which 143 organizations are linked to an installation.
- `repositories` remains org-scoped and now has a partial unique index on `github_id` where `status = 'active'`.
- Repo claiming is explicit. Installing or updating the GitHub App does not automatically import every accessible repository into an org.

## Claiming And Transfer

Admins list installation repositories from `/api/v1/integrations/github/repositories`. Each repository reports one claim status:

- `unclaimed`
- `owned_by_current_org`
- `owned_by_other_org`
- `disconnected_in_current_org`

`POST /api/v1/integrations/github/repositories/claim` activates selected repositories for the current 143 org. If a selected repo is actively owned elsewhere, the request returns a conflict unless transfer is requested and the acting user is an admin in both orgs.

Transfer disconnects the previous org's repo row and activates a row in the destination org. It does not copy sessions, projects, automations, settings, or learned context.

## Authorization

Claiming requires:

- active 143 org role `admin`
- a linked GitHub App user token for the acting user
- GitHub user access to each selected repository

If user auth is missing, the API returns `GITHUB_USER_AUTH_REQUIRED`.

## Rollout Preflight

Before applying the migration that creates the active-owner partial unique index, run:

```sql
SELECT github_id, count(*) AS active_rows
FROM repositories
WHERE status = 'active' AND github_id IS NOT NULL
GROUP BY github_id
HAVING count(*) > 1;
```

The migration intentionally fails if this query returns rows. Resolve each duplicate with an explicit disconnect or transfer decision before deploying the index.

## Webhooks

GitHub installation webhooks update installation metadata only. `installation_repositories.added` does not auto-claim repos. Removed repositories and deleted installations disconnect affected active repository rows.

PR, review, and check webhooks are accepted only when the GitHub repository has an active 143 owner. Events for unclaimed repos are acknowledged and ignored.
