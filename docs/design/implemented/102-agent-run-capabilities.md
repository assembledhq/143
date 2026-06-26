# Design: Agent Run Capabilities

> **Status:** Partially Implemented | **Last reviewed:** 2026-06-16

Agent runs now have a shared capability policy foundation for manual sessions
and automations. The implemented slice covers the code-owned capability
catalog, org default policies, automation override policies, launch snapshots,
and a capability-aware `143-tools` filtering wrapper.

Implemented pieces:

- `agent_capability_policies` and `agent_capability_policy_grants` use the
  insert-only settings pattern for org defaults and automation overrides.
- `session_execution_metadata.capability_snapshot` and
  `automation_runs.capability_snapshot` store the run-time grants used by an
  agent run.
- The Go model layer defines typed string enums for capability IDs, access
  levels, risks, scopes, policy types, and grant sources.
- `internal/services/agentcapabilities` owns the v1 catalog, grant validation,
  recommended defaults, and policy-to-snapshot resolution.
- `/api/v1/agent-capabilities`, `/api/v1/settings/agent/capabilities`, and
  `/api/v1/automations/{id}/capabilities` expose catalog and policy surfaces.
- Settings -> Coding Agents shows editable default capability toggles for
  admins and read-only defaults for viewers.
- `mcp.NewCapabilityFilteredToolSource` filters `143-tools` visibility and
  direct calls according to a capability snapshot.

Remaining follow-up work from the future spec:

- Resolve and persist snapshots at every session and automation launch call
  site.
- Add the internal `capability list/request` namespace and wire approved
  in-session grants through human-input approvals.
- Add internal session-history search/detail/message APIs and CLI namespace.
- Apply the filtered tool source and env injection in sandbox runtime startup.
- Surface automation override controls in create/edit forms and run-history
  snapshot details.

The full target design remains in
[future/102-automation-capabilities.md](../future/102-automation-capabilities.md)
until those follow-up pieces are complete.
