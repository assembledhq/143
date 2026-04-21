# Settings pages — conventions

## TL;DR

All settings pages **autosave by default**. No "Save" button, no per-field
colored status text, no sticky bottom footer.

- Use `useAutosave` (`@/hooks/useAutosave`) to wire the mutation.
- Render one `<AutosaveIndicator>` (`@/components/AutosaveIndicator`) per
  logical save scope — typically in the page or section header.
- Errors surface through Sonner; optimistic updates roll back automatically.

Exceptions that still use explicit buttons are enumerated below. If your field
is not on that list, autosave it.

## The hook

See `@/hooks/useAutosave.ts`.

```ts
const { save, flush, status } = useAutosave<SettingsPatch>({
  queryKey: queryKeys.settings.all,
  mutationFn: (payload) => api.settings.update(payload),
  applyOptimistic: applyOrgSettingsPatch,
  coalesce: coalesceSettingsPatch,
  debounceMs: 0, // 0 for toggles/selects/radios; 400 for text/number
});
```

Contract:

- `save(vars)` — debounced dispatch. Two calls to `save` inside the same
  debounce window coalesce into one mutation.
- `flush()` — fire any pending debounced payload immediately. Wire this to
  `onBlur` for text/number inputs so Tab-away commits without waiting.
- `status` — `"idle" | "saving" | "saved" | "error"`. Feed directly into
  `<AutosaveIndicator status={status} />`.

A single shared queue per `queryKey` guarantees at most one mutation in flight
and merges incoming saves via `coalesce` until the in-flight resolves. This is
what prevents the server's read-modify-write PATCH from clobbering concurrent
edits.

### Debounce convention

| Input type                      | `debounceMs` | Commit on |
| ------------------------------- | ------------ | --------- |
| Toggle / checkbox / radio       | `0`          | change    |
| Select                          | `0`          | change    |
| Text / textarea / number        | `400`        | blur via `flush()` |
| Tag list (Enter-to-add)         | `0`          | change    |

For text/number, also pair with `onBlur={flush}` so advancing with Tab commits
immediately.

### Optimistic update helpers

For org-level settings, use the helpers in `@/lib/settings-autosave`:

- `applyOrgSettingsPatch(prev, patch)` — shallow-merges `patch.settings` into
  `previous.data.settings`.
- `coalesceSettingsPatch(a, b)` — merges two patches with later keys winning.

For other resources (per-repo, etc.) write a small `applyOptimistic` that
patches the specific cache entry.

### Nested objects

The server's settings PATCH is a **shallow** merge at the top level. If you
autosave a single field inside a nested object like `agent_config` or
`product_context`, you must send the full merged nested object — otherwise the
server will wipe sibling keys.

```ts
// WRONG — wipes other providers' env vars in agent_config.codex
save({ settings: { agent_config: { codex: { OPENAI_API_KEY: value } } } });

// RIGHT — read current, patch one field, send the whole nested object
const merged = { ...currentAgentConfig, codex: { ...currentAgentConfig.codex, OPENAI_API_KEY: value } };
save({ settings: { agent_config: merged } });
```

See `settings/agent/page.tsx` (`saveAgentConfigField`) and
`components/autopilot/autopilot-steering-sheet.tsx` (`saveProductContext`) for
the canonical patterns.

## The indicator

See `@/components/AutosaveIndicator.tsx`.

Place **one** indicator per logical save scope:

- Page-level autosave → in the page header next to the title.
- Section-scoped autosave (e.g. Model vs. API keys on `/settings/llm`) →
  inside that section's heading row.

The indicator is `role="status" aria-live="polite"`, so screen readers
announce state transitions; no additional aria plumbing is required.

Never reimplement status with inline `<p className="text-emerald-600">Saved</p>`
or `<p className="text-destructive">Failed</p>`. Errors go to Sonner via the
hook; success goes to the indicator.

## When NOT to autosave

Use an explicit save (button, dialog, modal) only for:

1. **Secret / credential inputs** — API keys, OAuth tokens, webhook URLs.
   These should not be optimistically applied, and the server may validate
   asynchronously. Examples: per-provider "Save key" on `/settings/llm` and
   `/settings/account`; the Notion token dialog on `/settings/integrations`.
2. **Destructive or irreversible actions** — remove, disconnect, archive,
   role change, "set as team default". Wrap in an `AlertDialog` with an
   explicit confirm button. Examples: disconnect-GitHub, remove team member,
   role-change confirmation on `/settings/team`, set-as-team-default on
   `/settings/account`.
3. **Resource-creation wizards** — inviting a teammate, creating an eval,
   OAuth callback pages. These are terminal flows where "Cancel" must be
   distinct from "Create".

If a change is easy to undo, is not a secret, and does not cross the destructive
bar, autosave it.

## Exempt pages

These pages are explicitly **not** autosave surfaces:

- `audit-log/` — read-only.
- `usage/` — read-only.
- `evals/**` — creation wizard and read-only detail/batch views.
- `integrations/github/setup/` — OAuth callback.

If you add a new page under `settings/` and it does not belong to the exempt
list above, it should use the autosave pattern.

## Before / after

Before (the pattern we are replacing):

```tsx
const [llmModel, setLlmModel] = useState(settings.llm_model ?? DEFAULT);
const [saveStatus, setSaveStatus] = useState<"idle" | "saving" | "saved" | "error">("idle");
const mutation = useMutation({
  mutationFn: () => api.settings.update({ settings: { llm_model: llmModel } }),
  onSuccess: () => {
    setSaveStatus("saved");
    setTimeout(() => setSaveStatus("idle"), 1500);
    queryClient.invalidateQueries({ queryKey: ["settings"] });
  },
  onError: () => setSaveStatus("error"),
});
// ...
<Select value={llmModel} onValueChange={setLlmModel}>...</Select>
<Button onClick={() => mutation.mutate()}>Save model</Button>
{saveStatus === "saved" && <p className="text-emerald-600">Saved</p>}
{saveStatus === "error" && <p className="text-destructive">Failed</p>}
```

After:

```tsx
const autosave = useAutosave<SettingsPatch>({
  queryKey: queryKeys.settings.all,
  mutationFn: (payload) => api.settings.update(payload),
  applyOptimistic: applyOrgSettingsPatch,
  coalesce: coalesceSettingsPatch,
});
const llmModel = settings.llm_model ?? DEFAULT;
// ...
<AutosaveIndicator status={autosave.status} />
<Select value={llmModel} onValueChange={(v) => autosave.save({ settings: { llm_model: v } })}>...</Select>
```

No local state, no `setTimeout`, no manual invalidation, no Save button.

## Testing checklist

When adding or changing an autosaved field, verify:

- Rapid toggles of the same control don't produce out-of-order server state
  (the queue coalesces in-flight saves — you should see at most two network
  requests for N > 2 rapid clicks).
- Killing the backend and editing the field shows the optimistic value, then
  rolls back, and a Sonner error toast appears.
- The indicator shows `Saving…`, then `Saved ✓`, then clears after ~1.5s.
- Navigating away from the page mid-save does not throw and does not leave a
  dangling optimistic update on return.
- Autosaving one key inside a nested object (`agent_config`,
  `product_context`) does not wipe sibling keys — check the other siblings
  after a refetch.
- For text/number fields, Tab-away commits the value (no need to wait out the
  debounce).
