"use client";

import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { DebouncedTextarea } from "@/components/debounced-fields";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { X } from "lucide-react";
import { useAutosave } from "@/hooks/useAutosave";
import { useAutosaveNumericField } from "@/hooks/useAutosaveNumericField";
import { availableAgentModelGroups, pmUsableResolvedCredentials } from "@/lib/agents";
import { queryKeys } from "@/lib/query-keys";
import type {
  CodingCredentialSummary,
  ListResponse,
  Organization,
  OrgSettings,
  RepoSettings,
  RepoPMSettings,
  Repository,
  SingleResponse,
} from "@/lib/types";
import { DEFAULT_PM_MODEL } from "@/lib/model-constants";

const PM_SCHEDULE_MIN = 1;
const PM_SCHEDULE_MAX = 24;

const clamp = (value: number, min: number, max: number) =>
  Math.min(max, Math.max(min, value));

type RepoPatch = { settings: RepoSettings };

// Repo settings is replaced wholesale on the server, so the latest patch
// wins — no field-level coalesce needed.
const coalesceReplaceLatest = <T,>(_a: T, b: T) => b;

// Seed a PM object from org-level defaults. Used whenever the first edit on a
// non-customized repo needs to be promoted to a full PM object — partial PM
// objects fail server validation, so every entry point materializes the whole
// thing up front. `override` lets callers swap in the field they're actually
// editing (e.g. `pm_schedule_hours`) without re-specifying the rest.
function seedPMFromOrg(
  orgSettings: OrgSettings,
  override: Partial<RepoPMSettings> = {},
): RepoPMSettings {
  return {
    pm_schedule_hours: orgSettings.pm_schedule_hours ?? 24,
    pm_model: orgSettings.pm_model ?? DEFAULT_PM_MODEL,
    product_context: {
      philosophy: orgSettings.product_context?.philosophy ?? "",
      direction:
        orgSettings.product_context?.direction ??
        orgSettings.product_direction ??
        "",
      focus_areas: orgSettings.product_context?.focus_areas ?? [],
      avoid_areas: orgSettings.product_context?.avoid_areas ?? [],
    },
    ...override,
  };
}

interface RepoPMSettingsProps {
  repository: Repository;
}

export function RepoPMSettingsEditor({ repository }: RepoPMSettingsProps) {
  const queryClient = useQueryClient();
  const { data: orgResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });
  const { data: resolvedCredsResponse } = useQuery<ListResponse<CodingCredentialSummary>>({
    queryKey: queryKeys.codingCredentials.list("resolved"),
    queryFn: () => api.codingCredentials.list("resolved"),
  });
  const { data: orgCodingCredentialsResponse } = useQuery<ListResponse<CodingCredentialSummary>>({
    queryKey: queryKeys.codingCredentials.list("org"),
    queryFn: () => api.codingCredentials.list("org"),
  });

  const orgSettings = (orgResponse?.data?.settings ?? {}) as OrgSettings;
  const repoSettings = (repository.settings ?? {}) as RepoSettings;
  const customized = repoSettings.pm != null;

  // Effective values — what the form renders. When not customized, fall back
  // to org defaults so the "Customize" stamp captures what the user sees.
  const effectiveScheduleHours =
    repoSettings.pm?.pm_schedule_hours ?? orgSettings.pm_schedule_hours ?? 24;
  const effectiveModel =
    repoSettings.pm?.pm_model ?? orgSettings.pm_model ?? DEFAULT_PM_MODEL;
  const effectivePhilosophy =
    repoSettings.pm?.product_context?.philosophy ??
    orgSettings.product_context?.philosophy ??
    "";
  const effectiveDirection =
    repoSettings.pm?.product_context?.direction ??
    orgSettings.product_context?.direction ??
    orgSettings.product_direction ??
    "";
  const effectiveFocusAreas =
    repoSettings.pm?.product_context?.focus_areas ??
    orgSettings.product_context?.focus_areas ??
    [];
  const effectiveAvoidAreas =
    repoSettings.pm?.product_context?.avoid_areas ??
    orgSettings.product_context?.avoid_areas ??
    [];

  const [focusInput, setFocusInput] = useState("");
  const [avoidInput, setAvoidInput] = useState("");

  const resolvedCredentials = useMemo(
    () => resolvedCredsResponse?.data ?? [],
    [resolvedCredsResponse],
  );
  const orgCodingCredentials = useMemo(
    () => orgCodingCredentialsResponse?.data ?? [],
    [orgCodingCredentialsResponse],
  );
  const pmResolvedCredentials = useMemo(
    () => pmUsableResolvedCredentials(resolvedCredentials),
    [resolvedCredentials],
  );

  const pmModelGroups = useMemo(() => {
    return availableAgentModelGroups(
      pmResolvedCredentials,
      null,
      orgCodingCredentials,
      orgSettings.default_agent_type || "codex",
      { orgAgentConfig: orgSettings.agent_config },
    );
  }, [pmResolvedCredentials, orgCodingCredentials, orgSettings.default_agent_type, orgSettings.agent_config]);

  const credentialsLoaded = Boolean(
    resolvedCredsResponse && orgCodingCredentialsResponse,
  );

  const autosave = useAutosave<RepoPatch>({
    queryKey: ["repository", repository.id],
    mutationFn: async (payload) => {
      // useAutosave invalidates its own queryKey on settle. The repositories
      // list query (rendered by the sidebar, repo picker, etc.) caches the
      // same data under a different key, so invalidate it here too; otherwise
      // list views show stale PM settings until the next navigation. Run the
      // invalidation in `finally` so an error path — where the optimistic
      // cache was rolled back — also reconciles the list.
      try {
        return await api.repositories.update(repository.id, payload);
      } finally {
        void queryClient.invalidateQueries({ queryKey: queryKeys.repositories.all });
      }
    },
    applyOptimistic: (prev, patch) => {
      const previous = prev as SingleResponse<Repository> | undefined;
      if (!previous?.data) return previous;
      return {
        ...previous,
        data: { ...previous.data, settings: patch.settings },
      };
    },
    coalesce: coalesceReplaceLatest,
  });

  // Helper: produce a patch that writes a new PM object. Undefined means
  // "switch to org defaults" (send an empty settings object to clear PM).
  const savePM = (pm: RepoPMSettings | undefined) => {
    autosave.save({ settings: pm == null ? {} : { pm } });
  };

  // Updates a slice of the PM settings. Seeds from the org defaults if the
  // repo isn't currently customized so the first edit turns into a full PM
  // object rather than a partial one that fails server validation.
  const updatePM = (mutate: (pm: RepoPMSettings) => RepoPMSettings) => {
    const current: RepoPMSettings = repoSettings.pm ?? seedPMFromOrg(orgSettings);
    savePM(mutate(current));
  };

  const scheduleField = useAutosaveNumericField({
    serverValue: effectiveScheduleHours,
    autosave,
    toPatch: (v) => {
      const current: RepoPMSettings =
        repoSettings.pm ?? seedPMFromOrg(orgSettings, { pm_schedule_hours: v });
      return { settings: { pm: { ...current, pm_schedule_hours: v } } };
    },
    clamp: (v) => clamp(v, PM_SCHEDULE_MIN, PM_SCHEDULE_MAX),
  });

  const handleToggleCustomized = (nextCustomized: boolean) => {
    if (nextCustomized === customized) return;
    if (nextCustomized) {
      // Stamp the currently displayed values (which are org defaults when not
      // customized) into a new repo-level PM object.
      savePM({
        pm_schedule_hours: effectiveScheduleHours,
        pm_model: effectiveModel,
        product_context: {
          philosophy: effectivePhilosophy,
          direction: effectiveDirection,
          focus_areas: effectiveFocusAreas,
          avoid_areas: effectiveAvoidAreas,
        },
      });
    } else {
      savePM(undefined);
    }
  };

  const addTag = (
    raw: string,
    list: string[],
    updateList: (next: string[]) => void,
  ) => {
    const trimmed = raw.trim();
    if (!trimmed || list.includes(trimmed)) return;
    updateList([...list, trimmed]);
  };

  const removeTag = (
    value: string,
    list: string[],
    updateList: (next: string[]) => void,
  ) => {
    updateList(list.filter((item) => item !== value));
  };

  const updateFocusAreas = (next: string[]) => {
    updatePM((pm) => ({
      ...pm,
      product_context: {
        philosophy: pm.product_context?.philosophy ?? "",
        direction: pm.product_context?.direction ?? "",
        focus_areas: next,
        avoid_areas: pm.product_context?.avoid_areas,
      },
    }));
  };

  const updateAvoidAreas = (next: string[]) => {
    updatePM((pm) => ({
      ...pm,
      product_context: {
        philosophy: pm.product_context?.philosophy ?? "",
        direction: pm.product_context?.direction ?? "",
        focus_areas: pm.product_context?.focus_areas,
        avoid_areas: next,
      },
    }));
  };

  const orgPhilosophy = orgSettings.product_context?.philosophy;
  const orgDirection =
    orgSettings.product_context?.direction ?? orgSettings.product_direction;

  return (
    <div className="space-y-4">
      <div className="flex justify-end">
        <AutosaveIndicator status={autosave.status} />
      </div>

      <Card>
        <CardContent>
          <div className="flex items-center justify-between">
            <div>
              <p className="text-sm font-medium">PM settings</p>
              <p className="text-xs text-muted-foreground">
                {customized
                  ? "Custom PM settings for this repository."
                  : "Using organization defaults (Staff PM)."}
              </p>
            </div>
            <div className="flex gap-2">
              <Button
                variant={customized ? "outline" : "default"}
                size="sm"
                onClick={() => handleToggleCustomized(false)}
              >
                Org defaults
              </Button>
              <Button
                variant={customized ? "default" : "outline"}
                size="sm"
                onClick={() => handleToggleCustomized(true)}
              >
                Customize
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      {customized && (
        <>
          <Card>
            <CardContent>
              <div className="grid gap-4 md:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="repo-pm-schedule">Schedule (hours)</Label>
                  <Input
                    id="repo-pm-schedule"
                    type="number"
                    min={PM_SCHEDULE_MIN}
                    max={PM_SCHEDULE_MAX}
                    value={scheduleField.value}
                    onChange={scheduleField.onChange}
                    onBlur={scheduleField.onBlur}
                    placeholder="24"
                  />
                  <p className="text-xs text-muted-foreground">
                    Org default: every {orgSettings.pm_schedule_hours ?? 24} hours
                  </p>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="repo-pm-model">PM Model</Label>
                  <Select
                    value={effectiveModel}
                    onValueChange={(value) =>
                      updatePM((pm) => ({ ...pm, pm_model: value }))
                    }
                  >
                    <SelectTrigger id="repo-pm-model" aria-label="PM Model">
                      <SelectValue placeholder="Select a model" />
                    </SelectTrigger>
                    <SelectContent>
                      {!credentialsLoaded ? (
                        <SelectItem value={DEFAULT_PM_MODEL} disabled>
                          Loading providers…
                        </SelectItem>
                      ) : pmModelGroups.length === 0 ? (
                        <SelectItem value={DEFAULT_PM_MODEL} disabled>
                          No providers configured
                        </SelectItem>
                      ) : (
                        pmModelGroups.map((group) => (
                          <SelectGroup key={group.key}>
                            <SelectLabel>{group.label}</SelectLabel>
                            {group.models.map((model) => (
                              <SelectItem key={model} value={model}>
                                {model}
                              </SelectItem>
                            ))}
                          </SelectGroup>
                        ))
                      )}
                    </SelectContent>
                  </Select>
                  <p className="text-xs text-muted-foreground">
                    Org default: {orgSettings.pm_model ?? DEFAULT_PM_MODEL}
                  </p>
                </div>
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardContent>
              <div className="space-y-4">
                <div className="space-y-2">
                  <Label htmlFor="repo-philosophy">Philosophy</Label>
                  <DebouncedTextarea
                    id="repo-philosophy"
                    rows={3}
                    serverValue={effectivePhilosophy}
                    onCommit={(value) =>
                      updatePM((pm) => ({
                        ...pm,
                        product_context: {
                          philosophy: value,
                          direction: pm.product_context?.direction ?? "",
                          focus_areas: pm.product_context?.focus_areas,
                          avoid_areas: pm.product_context?.avoid_areas,
                        },
                      }))
                    }
                    placeholder="How should the PM think about tradeoffs for this repo?"
                  />
                  {orgPhilosophy && (
                    <p className="text-xs text-muted-foreground">
                      Org default: {orgPhilosophy.length > 60 ? orgPhilosophy.slice(0, 60) + "..." : orgPhilosophy}
                    </p>
                  )}
                </div>
                <div className="space-y-2">
                  <Label htmlFor="repo-direction">Current direction</Label>
                  <DebouncedTextarea
                    id="repo-direction"
                    rows={2}
                    serverValue={effectiveDirection}
                    onCommit={(value) =>
                      updatePM((pm) => ({
                        ...pm,
                        product_context: {
                          philosophy: pm.product_context?.philosophy ?? "",
                          direction: value,
                          focus_areas: pm.product_context?.focus_areas,
                          avoid_areas: pm.product_context?.avoid_areas,
                        },
                      }))
                    }
                    placeholder="What is this repo focused on?"
                  />
                  {orgDirection && (
                    <p className="text-xs text-muted-foreground">
                      Org default: {orgDirection.length > 60 ? orgDirection.slice(0, 60) + "..." : orgDirection}
                    </p>
                  )}
                </div>
                <div className="grid gap-4 md:grid-cols-2">
                  <div className="space-y-2">
                    <Label htmlFor="repo-focus-areas">Focus areas</Label>
                    <Input
                      id="repo-focus-areas"
                      value={focusInput}
                      onChange={(e) => setFocusInput(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === "Enter" || e.key === ",") {
                          e.preventDefault();
                          addTag(focusInput, effectiveFocusAreas, updateFocusAreas);
                          setFocusInput("");
                        }
                      }}
                      placeholder="Add focus area and press Enter"
                    />
                    <div className="flex flex-wrap gap-2">
                      {effectiveFocusAreas.map((area) => (
                        <Badge key={area} variant="secondary" className="text-xs">
                          {area}
                          <Button
                            variant="ghost"
                            size="sm"
                            className="ml-1 h-4 w-4 p-0"
                            onClick={() => removeTag(area, effectiveFocusAreas, updateFocusAreas)}
                          >
                            <X className="h-3 w-3" />
                          </Button>
                        </Badge>
                      ))}
                    </div>
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor="repo-avoid-areas">Avoid areas</Label>
                    <Input
                      id="repo-avoid-areas"
                      value={avoidInput}
                      onChange={(e) => setAvoidInput(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === "Enter" || e.key === ",") {
                          e.preventDefault();
                          addTag(avoidInput, effectiveAvoidAreas, updateAvoidAreas);
                          setAvoidInput("");
                        }
                      }}
                      placeholder="Add avoid area and press Enter"
                    />
                    <div className="flex flex-wrap gap-2">
                      {effectiveAvoidAreas.map((area) => (
                        <Badge key={area} variant="secondary" className="text-xs">
                          {area}
                          <Button
                            variant="ghost"
                            size="sm"
                            className="ml-1 h-4 w-4 p-0"
                            onClick={() => removeTag(area, effectiveAvoidAreas, updateAvoidAreas)}
                          >
                            <X className="h-3 w-3" />
                          </Button>
                        </Badge>
                      ))}
                    </div>
                  </div>
                </div>
              </div>
            </CardContent>
          </Card>
        </>
      )}
    </div>
  );
}
