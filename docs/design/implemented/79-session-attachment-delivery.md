# Session Attachment Delivery

## Problem

Session messages can persist uploaded files as `/api/v1/uploads/files/...` URLs, but coding agents execute inside sandboxes and cannot rely on browser-facing upload routes or authenticated HTTP access. Image-only or attachment-heavy prompts therefore need a server-side handoff path.

## Decision

Coding-agent workers resolve first-party uploaded session attachments before building the agent prompt. For each upload URL under `/api/v1/uploads/files/{orgID}/...`, the worker verifies that the URL org matches the session org, opens the file through upload storage, copies it into the sandbox under `$HOME/.143/attachments/turn-{n}/`, and appends an `## Attached files` section to the prompt with the sandbox-local path and content type.

Unreadable uploads do not fail the turn. They are included in the same prompt section as warnings so the agent and user-visible logs preserve what was missing.

## Scope

- Local filesystem and S3 upload stores expose a worker read path separate from browser serving.
- Initial manual runs materialize attachments from the latest user message.
- Follow-up runs materialize attachments from every pending user message processed in that turn.
- Resume-history fallback prompts include prior raw attachment URLs so reconstructed contexts remain understandable.

## Limitation

External image and file URLs are not fetched in v1. They remain textual links in the prompt with a warning that external attachments were not copied into the sandbox.
