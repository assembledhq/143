# PR Required Checks Redis Cache

> **Status:** Implemented | **Last reviewed:** 2026-06-06

143 gates manual PR merges on authoritative GitHub mergeability and check state. A clean PR with zero visible check runs is ambiguous immediately after PR creation because GitHub may not have created check runs yet. 143 treats zero check runs as merge-ready only when the PR base branch has no required status checks configured.

## Cache contract

The GitHub branch-protection lookup for required status checks is cached in Redis per organization, repository, and base branch:

```text
github:required-checks:{org_id}:{repo_full_name}:{base_branch}
```

The cached payload stores whether required checks are configured and when the value was observed. Redis TTL is the source of expiry:

- `required=true`: 24 hours
- `required=false`: 6 hours

The permissive `false` value uses a shorter TTL because stale "no checks required" can allow a PR to merge sooner than expected after a branch-protection change. Stale `true` only delays merge.

## Failure behavior

Redis is an optimization, not a source of truth. On Redis miss, malformed payload, or Redis outage, 143 falls back to a live GitHub branch lookup. If GitHub cannot answer the branch-protection lookup while the PR has zero visible check runs, PR health sync fails closed and direct merge remains blocked by the final backend refresh.

Direct merge still refreshes GitHub state immediately before calling the GitHub merge API. The final merge gate requires `can_merge=true`, which for zero-check PRs requires `checks_confirmed=true` from either a fresh or cached "no required checks" branch-protection result.
