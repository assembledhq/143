# Browser Page Titles

> **Status:** Implemented | **Last reviewed:** 2026-05-23

Browser tabs should always identify both the product and the current workspace context. The standard format is:

```
143 | <page>
```

The root document title falls back to `143` while the app is loading. Once the client route is known, the frontend resolves a page title from a central route registry. Detail pages replace the generic route fallback with the loaded entity name for sessions, projects, automations, repositories, eval tasks, and eval batches.

## Route Title Plan

- `/` -> `143 | Home`
- `/login` -> `143 | Login`
- `/invite/accept` -> `143 | Accept invite`
- `/autopilot` -> `143 | Autopilot`
- `/autopilot/decisions` -> `143 | Autopilot decisions`
- `/sessions` -> `143 | Sessions`
- `/sessions/new` -> `143 | New session`
- `/sessions/:id` -> `143 | <session title>`, falling back to `143 | Session`
- `/automations` -> `143 | Automations`
- `/automations/new` -> `143 | New automation`
- `/automations/templates` -> `143 | Automation templates`
- `/automations/:id` -> `143 | <automation name>`, falling back to `143 | Automation`
- `/projects` -> `143 | Projects`
- `/projects/new` -> `143 | New project`
- `/projects/:id` -> `143 | <project title>`, falling back to `143 | Project`
- `/repositories/:id` -> `143 | <owner/repo>`, falling back to `143 | Repository`
- `/integrations` -> `143 | Integrations`
- `/team` -> `143 | Team`
- `/settings` and child routes -> settings-specific titles such as `Account settings`, `Audit log`, `Usage`, `GitHub setup`, and `Team settings`; eval task and batch detail pages use the loaded eval or batch name
- Landing/support pages use literal page names: `About`, `Privacy`, `Security`, and `Terms`

## Future Page Fallback

The route resolver derives a readable title from the last non-ID path segment when a route has not yet been registered. For example, `/settings/billing-profiles` becomes `143 | Billing profiles`, and `/ops/release-gates/gate-1` becomes `143 | Release gates`.

New pages should still be added to the explicit title registry when the desired title differs from the derived fallback or when the route is a dynamic entity detail page. Entity detail pages should use the shared page-title hook to replace generic titles with the loaded entity name.
