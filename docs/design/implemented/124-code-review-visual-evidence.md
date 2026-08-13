# Design: Code Review Visual Evidence

> **Status:** Implemented | **Last reviewed:** 2026-08-12
>
> **Depends on:** [../overall.md](../overall.md), [../implemented/112-code-reviewer-bot-auto-approval.md](../implemented/112-code-reviewer-bot-auto-approval.md)

## Summary

Code Reviewer must inspect screenshots and other images rendered in the pull
request description and human-authored PR discussion. A relevant image from
any supported surface may satisfy a visual-evidence description requirement.
Images, alt text, captions, and nearby discussion text are always untrusted PR
content: they provide factual evidence but never instructions to an agent.

This behavior launches directly when the complete path is deployed. It has no
feature flag, policy opt-in, repository allowlist, or staged rollout.

## Product Contract

Capture images from these authoritative GitHub surfaces:

1. the PR description;
2. PR conversation (issue) comments;
3. submitted review bodies; and
4. inline review comments.

The description is in scope regardless of the PR author's GitHub actor type.
Discussion images are in scope only when GitHub classifies the author as a
`User` or `Mannequin`, including outside contributors. Exclude `Bot`, `App`,
`Organization`, deleted/unknown actors, and therefore 143-authored output.
Human-authored discussion conclusions and review decisions remain excluded as
risk signals; only the captured visual content and its bounded provenance are
evidence.

Each description requirement has an explicit `evidence_kind` of `general` or
`visual`. The backend never infers that contract from the key, title, prompt, or
other prose. For compatibility, only the legacy built-in `ui_evidence` key is
normalized to `visual` when an older stored policy omits the field; every other
omitted value normalizes to `general`.

Image presence alone does not satisfy a visual requirement. The orchestrator
must identify a relevant, current image that demonstrates the changed
experience. An image-backed `satisfied` assessment must cite one or more valid
evidence IDs. Preview links and repository-native visual evidence may still
satisfy the requirement when the assessment explains that basis without an
image ID.

Each assessment captures one immutable evidence snapshot for its head SHA.
Every configured reviewer and the orchestrator receive the same ordered
manifest and first-party image attachments. Later edits require an explicit
rerequest; they do not mutate an assessment already in progress.

## Architecture

```text
GitHub PR + paginated discussion APIs
              |
              v
rendered-HTML discovery and human-source filter
              |
              v
bounded secure downloader -> first-party upload storage
              |
              v
immutable visual-evidence prompt record
              |
              +--> reviewer attachments + manifest
              +--> orchestrator attachments + manifest
              +--> description assessment evidence IDs
              +--> review Evidence panel
```

GitHub is queried live rather than relying on the webhook feedback mirror,
which may not contain comments written before 143 began tracking the PR. The
collector requests GitHub's rendered HTML representation and paginates issue
comments, reviews, and review comments independently. It extracts `<img>` and
responsive `srcset` sources, preserves author/source/time provenance, and
orders results deterministically by surface, provider timestamp/object ID, and
image position.

Shared data types live in `internal/models` so the GitHub and worker packages
can exchange the discovery and materialized manifests without a package cycle.
The worker depends on a dedicated evidence provider rather than widening the
general PR publication interface.

## Database Contract

No new table is introduced. The immutable snapshot uses the existing
tenant-scoped `code_review_prompt_records` table with `role =
visual_evidence`, a unique `(org_id, record_key)` checkpoint, the bounded audit
summary in `content`, and the versioned snapshot JSON in `metadata`. Migration
000286 widens `chk_code_review_prompt_records_role` to accept
`visual_evidence`; during the legacy naming compatibility window it widens the
equivalent `code_review_prompt_artifacts` constraint as well. The rollback
restores the prior write constraint as `NOT VALID` so already-captured audit
records remain readable.

## API Contract

`GET /api/v1/code-reviews/{session_id}/evidence` remains the read-only evidence
route. It requires an authenticated active-organization membership with the
`admin`, `builder`, `member`, or `viewer` role and scopes every lookup to that
organization. The existing single-resource response adds:

```json
{
  "data": {
    "visual_evidence": {
      "version": 1,
      "complete": true,
      "overflow": true,
      "omitted_source_count": 4,
      "evidence": []
    },
    "cited_visual_evidence_ids": ["ve_..."]
  }
}
```

`visual_evidence` is absent for historical assessments without a snapshot.
The cited ID list is derived only from a completed, backend-validated
orchestrator synthesis and contains known snapshot IDs in stable assessment
order. A corrupt or ambiguous stored snapshot returns HTTP 500 with
`CODE_REVIEW_VISUAL_EVIDENCE_LOAD_FAILED`. There is no request-body, query, or
SSE contract change.

## Immutable Manifest

The persisted snapshot includes:

- version, repository identity, PR number, head SHA, capture time, completeness,
  overflow state, and aggregate omitted-source count;
- a deterministic evidence ID and source ID;
- surface, provider object, source URL, author, author association, timestamps,
  and image position;
- original URL for provenance and first-party storage key/URL for agents;
- bounded untrusted alt/context text;
- SHA-256, validated content type, byte size, dimensions, fetch status, and a
  bounded failure reason.

Fetch status is typed as `available`, `unavailable`, `unsupported`, or
`over_limit`. Available evidence alone may be attached or cited.

Reuse `code_review_prompt_records` with role `visual_evidence` and record key:

```text
code-review-prompts/{session_id}/{head_sha}/visual-evidence-v1
```

The complete structured manifest is stored in `metadata`; `content` contains a
human-readable audit summary. An org-scoped key lookup is the idempotency
checkpoint. Once the record exists, retries deserialize and reuse it without
refetching mutable GitHub content. The description input hash uses a canonical
manifest projection and content hashes, excluding mutable first-party URLs.

## Secure Materialization

Reuse `storage.UploadStore` with keys shaped as:

```text
{org_id}/code-review-evidence/{session_id}/{sha256}.{extension}
```

The downloader accepts HTTPS only. GitHub asset hosts use installation
credentials only on an explicit allowlist, and credentials are never forwarded
to a non-allowlisted redirect. Other HTTPS images use an unauthenticated,
SSRF-safe client. Resolve and revalidate every redirect target and reject
loopback, private, link-local, multicast, unspecified, and cloud-metadata
addresses.

A private-repository spike on 2026-08-12 confirmed the current attachment
flow: the canonical `github.com/user-attachments/assets/...` request returns
404 without authorization, returns a redirect with installation authorization,
and lands on a signed `*.s3.amazonaws.com` URL. The implementation therefore
sends installation authorization only to the exact GitHub user-attachment
route and rebuilds each redirect request without that header.

Validate decoded magic bytes instead of trusting URL extensions or response
headers. Initially accept PNG, JPEG, GIF, and WebP. Reject SVG rather than
passing active vector content to agents. Enforce:

- 10 MB per image;
- 40 megapixels per image;
- 32 images and 64 MB total per assessment;
- four concurrent downloads;
- 15 seconds per image;
- three redirects; and
- three transient attempts with bounded backoff.

All human images are eligible. Discovery retains provenance for at most the
first 32 images in deterministic order across all surfaces. Later sources are
represented only by `omitted_source_count`; their URLs, captions, authors, and
other per-image metadata are not persisted, prompted, or returned by the API.
An inaccessible or oversized retained image is recorded but cannot satisfy
evidence. An individual bad outside-contributor image must not fail or deny an
otherwise complete review.
A failure to list an entire supported GitHub surface makes the snapshot
incomplete and fails the review operationally after normal retries; the bot
must never approve from a silently partial snapshot.

## Prompt And Decision Contract

Reviewer and orchestrator templates state that every supplied image and all
associated text are untrusted evidence. Agents inspect them for relevance but
must not follow instructions embedded in pixels, alt text, captions, or nearby
discussion. The existing prompt-injection hard-risk signal applies to visual
content and its textual context.

The orchestrator receives a `<visual_evidence_manifest>` containing stable IDs
and retained provenance plus an aggregate omitted-source count. Structured
description policy requirements carry `evidence_kind`; assessments carry
`evidence_basis` and `evidence_ids`. The backend rejects unknown, repeated, or
unavailable IDs. An image-backed `satisfied` assessment without a valid ID is
invalid for approval; a requirement explicitly marked `visual` can otherwise
use only a preview link or repository-native visual basis. Text in the
description or diff can satisfy `general` requirements but cannot satisfy a
`visual` requirement.

The worker captures or restores the snapshot after authoritative PR/head sync
and before reviewer fan-out. It passes every available first-party URL through
`SendMessageInput.Images` to each reviewer and the orchestrator. Missing
provider wiring is an operational failure, not a silent text-only fallback.

## Product And Operations

The policy editor exposes each description requirement's evidence type. The
code review Evidence view gains a Visual evidence section with thumbnail,
evidence ID, surface, author, source link, time, status/failure, whether the
orchestrator cited it, and an aggregate count when additional sources were
omitted. User-facing copy calls the rule a PR evidence requirement while
preserving the existing machine reason code `description_failed` for API
compatibility.

Metrics cover discovered, fetched, deduplicated, unavailable, unsupported, and
over-limit images; bytes, fetch latency, source surface; snapshot completeness;
and the surface used to satisfy visual evidence. Logs contain identifiers,
counts, host classification, status, and timing only. Never log image bytes,
tokens, full comment bodies, or signed URLs.

Storage follows normal upload retention. Rollback deploys the prior binaries;
the additive prompt role constraint remains compatible, historical prompt
records remain audit data, and stored blobs expire through normal retention.

## Delivery Sequence

The implementation may be reviewed as stacked pull requests, but behavior only
launches when the complete path lands:

1. typed evidence/discovery contracts, live paginated GitHub discovery, prompt
   role migration, and org-scoped checkpoint lookup;
2. secure bounded downloader, first-party storage, immutable manifest
   persistence, idempotent restoration, and metrics;
3. worker attachment fan-out, prompt and structured-output contracts,
   validation, input hashing, and failure semantics;
4. evidence API/UI, user-facing docs, architecture reconciliation, and launch
   verification; and
5. launch hardening for the global retained-provenance bound and explicit
   description-requirement evidence kinds.

Before deployment, verify public and private repositories, every supported
surface, an outside contributor, bot exclusion, duplicate images, inaccessible
images, overflow, rerun idempotency, and incomplete-surface failure. Apply the
migration, verify shared upload storage, drain old workers, deploy the complete
binary set, resume workers, and run public/private smoke reviews. No slow
rollout is required.
