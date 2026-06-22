"use client";

import Link from "next/link";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { SettingsLastActivity } from "@/components/settings/settings-last-activity";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { DebouncedInput } from "@/components/debounced-fields";
import { useAuth } from "@/hooks/use-auth";
import { useOrgSettingsAutosave } from "@/hooks/use-org-settings-autosave";
import type { Organization, OrgSettings, PRReadinessCustomCheck, PRReadinessEnforcement, PRReadinessPolicyConfig, Repository, SingleResponse } from "@/lib/types";

const PR_AUTHORSHIP_OPTIONS = [
  { value: "user_preferred", label: "User preferred", description: "Use the user's GitHub token when available, fall back to the 143 app" },
  { value: "app_only", label: "App only", description: "Always create PRs as the 143 GitHub App" },
  { value: "user_required", label: "User required", description: "Require users to connect GitHub before creating PRs" },
] as const;

const ORG_READINESS_SCOPE = "__org__";
const READINESS_CHECKS = [
  "freshness",
  "agent_review_clean",
  "diff_collected",
  "test_evidence_present",
  "risk_flags",
  "dependency_config_risk",
  "generated_file_churn",
  "context_complete",
  "review_packet_draftable",
] as const;
const READINESS_ROLES = [
  { key: "builder", label: "Builder" },
  { key: "engineer", label: "Engineer" },
  { key: "admin", label: "Admin" },
] as const;
const READINESS_ENFORCEMENTS: PRReadinessEnforcement[] = ["off", "advisory", "blocking"];
type ReadinessRole = "builder" | "engineer" | "admin";
type ReadinessPresetValue = "off" | "advisory" | "builder_guarded" | "strict" | "custom";

const READINESS_PRESETS: Array<{ value: ReadinessPresetValue; label: string; description: string }> = [
  { value: "off", label: "Off", description: "Do not enforce readiness checks before PR creation." },
  { value: "advisory", label: "Advisory", description: "Run checks as guidance without blocking PR creation." },
  { value: "builder_guarded", label: "Builder guarded", description: "Block builders on freshness and clean agent review checks." },
  { value: "strict", label: "Strict", description: "Block builders on every built-in readiness check." },
  { value: "custom", label: "Custom", description: "Use the per-check and per-role settings below." },
];

function blankReadinessCustomCheck(): PRReadinessCustomCheck {
  return {
    check_key: "",
    name: "",
    prompt: "",
    paths: { include: [], exclude: [] },
    enforcement: { builder: "advisory", engineer: "advisory", admin: "advisory" },
  };
}

function readinessLabel(value: string) {
  return value.replaceAll("_", " ");
}

function csvToList(value: string) {
  return value.split(",").map((item) => item.trim()).filter(Boolean);
}

function listToCsv(value?: string[]) {
  return (value ?? []).join(", ");
}

// parseThreshold maps a number field's committed text to a non-negative integer,
// or undefined when blank/invalid so the server default applies (omitempty).
// Using undefined rather than `|| 25` lets 0 be entered and avoids silently
// resetting a cleared field to a hard-coded default.
function parseThreshold(value: string): number | undefined {
  const trimmed = value.trim();
  if (trimmed === "") return undefined;
  const parsed = Number(trimmed);
  if (!Number.isFinite(parsed) || parsed < 0) return undefined;
  return Math.floor(parsed);
}

function toggleRole(roles: string[] | undefined, role: string, enabled: boolean) {
  const set = new Set(roles ?? []);
  if (enabled) set.add(role);
  else set.delete(role);
  return Array.from(set);
}

function customCheckProvenanceLabel(check: PRReadinessCustomCheck) {
  if (check.source === "repo_config") {
    return ".143/config.json";
  }
  if (check.repository_id) {
    return "repo settings";
  }
  return "org settings";
}

function customCheckEditableInScope(check: PRReadinessCustomCheck, scopedRepositoryId?: string) {
  if (check.source === "repo_config") {
    return false;
  }
  if (scopedRepositoryId) {
    return check.repository_id === scopedRepositoryId;
  }
  return !check.repository_id;
}

function defaultReadinessChecks(): PRReadinessPolicyConfig["checks"] {
  const checks: PRReadinessPolicyConfig["checks"] = {};
  for (const checkKey of READINESS_CHECKS) {
    checks[checkKey] = {
      enforcement: {
        builder: checkKey === "freshness" || checkKey === "agent_review_clean" ? "blocking" : "advisory",
        engineer: "advisory",
        admin: "advisory",
      },
    };
  }
  return checks;
}

function checksWithEnforcement(
  builder: PRReadinessEnforcement,
  engineer: PRReadinessEnforcement,
  admin: PRReadinessEnforcement,
): PRReadinessPolicyConfig["checks"] {
  const checks: PRReadinessPolicyConfig["checks"] = {};
  for (const checkKey of READINESS_CHECKS) {
    checks[checkKey] = {
      enforcement: { builder, engineer, admin },
    };
  }
  return checks;
}

function strictReadinessChecks(): PRReadinessPolicyConfig["checks"] {
  const checks: PRReadinessPolicyConfig["checks"] = {};
  for (const checkKey of READINESS_CHECKS) {
    checks[checkKey] = {
      enforcement: { builder: "blocking", engineer: "advisory", admin: "advisory" },
    };
  }
  return checks;
}

function getCheckEnforcement(
  checks: PRReadinessPolicyConfig["checks"] | undefined,
  checkKey: string,
  role: ReadinessRole,
): PRReadinessEnforcement {
  return checks?.[checkKey]?.enforcement?.[role] ?? "advisory";
}

function checksMatch(a: PRReadinessPolicyConfig["checks"] | undefined, b: PRReadinessPolicyConfig["checks"] | undefined) {
  return READINESS_CHECKS.every((checkKey) =>
    READINESS_ROLES.every((role) => getCheckEnforcement(a, checkKey, role.key) === getCheckEnforcement(b, checkKey, role.key)),
  );
}

function readinessPresetValue(config: PRReadinessPolicyConfig): ReadinessPresetValue {
  const checks = config.checks ?? defaultReadinessChecks();
  if (!config.enabled_for_builders && checksMatch(checks, checksWithEnforcement("off", "off", "off"))) {
    return "off";
  }
  if (config.enabled_for_builders && checksMatch(checks, checksWithEnforcement("advisory", "advisory", "advisory"))) {
    return "advisory";
  }
  if (config.enabled_for_builders && checksMatch(checks, defaultReadinessChecks())) {
    return "builder_guarded";
  }
  if (config.enabled_for_builders && checksMatch(checks, strictReadinessChecks())) {
    return "strict";
  }
  return "custom";
}

function applyReadinessPreset(config: PRReadinessPolicyConfig, preset: ReadinessPresetValue): PRReadinessPolicyConfig {
  if (preset === "custom") return config;
  if (preset === "off") {
    return {
      ...config,
      enabled_for_builders: false,
      checks: checksWithEnforcement("off", "off", "off"),
    };
  }
  if (preset === "advisory") {
    return {
      ...config,
      enabled_for_builders: true,
      checks: checksWithEnforcement("advisory", "advisory", "advisory"),
    };
  }
  if (preset === "strict") {
    return {
      ...config,
      enabled_for_builders: true,
      checks: strictReadinessChecks(),
    };
  }
  return {
    ...config,
    enabled_for_builders: true,
    checks: defaultReadinessChecks(),
  };
}

function countRoleEnforcement(config: PRReadinessPolicyConfig, role: ReadinessRole, enforcement: PRReadinessEnforcement) {
  const checks = config.checks ?? defaultReadinessChecks();
  return READINESS_CHECKS.filter((checkKey) => getCheckEnforcement(checks, checkKey, role) === enforcement).length;
}

function PRAuthorshipSettings() {
  const queryClient = useQueryClient();
  const [readinessScope, setReadinessScope] = useState(ORG_READINESS_SCOPE);
  const [editingCheckId, setEditingCheckId] = useState<string | null>(null);
  const [newCheck, setNewCheck] = useState<PRReadinessCustomCheck>(blankReadinessCustomCheck());
  const [readinessSheetOpen, setReadinessSheetOpen] = useState(false);
  const scopedRepositoryId = readinessScope === ORG_READINESS_SCOPE ? undefined : readinessScope;
  const selectReadinessScope = (scope: string) => {
    setReadinessScope(scope);
    setEditingCheckId(null);
    setNewCheck(blankReadinessCustomCheck());
  };
  const { data: settingsResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });
  const { data: readinessPolicyResponse } = useQuery({
    queryKey: queryKeys.settings.prReadinessPolicy(scopedRepositoryId ?? null),
    queryFn: () => api.settings.getPRReadinessPolicy(scopedRepositoryId),
  });
  const { data: customChecksResponse } = useQuery({
    queryKey: queryKeys.settings.prReadinessCustomChecks(scopedRepositoryId ?? null),
    queryFn: () => api.settings.listPRReadinessCustomChecks(scopedRepositoryId),
  });
  const { data: repositoriesResponse } = useQuery({
    queryKey: ["repositories", "pr-readiness-settings"],
    queryFn: () => api.repositories.list(),
  });

  const { data: githubAccountStatus } = useQuery({
    queryKey: ["github-status"],
    queryFn: () => api.githubStatus.get(),
  });

  const settings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;
  const currentAuthorship = settings.pr_authorship ?? "user_preferred";
  const currentDraftDefault = settings.pr_draft_default ?? false;
  const currentAutoArchive = settings.auto_archive_on_pr_close ?? true;

  const accountConnected = githubAccountStatus?.connected ?? false;
  const accountNeedsReconnect = githubAccountStatus?.needs_reconnect ?? false;
  // Contextual hint tying this org-level setting to the per-user account
  // connection it implies, so the relationship is visible from both pages.
  const authorshipAccountHint =
    currentAuthorship === "app_only"
      ? "PRs are authored by the 143 app — connecting your GitHub account is optional."
      : accountNeedsReconnect
        ? "Your GitHub authorization expired — reconnect it so PRs are authored as you."
        : accountConnected
          ? "Your GitHub account is connected, so PRs can be authored as you."
          : currentAuthorship === "user_required"
            ? "You haven't connected your GitHub account — it's required for this mode."
            : "You haven't connected your GitHub account — connect it so PRs are authored as you.";

  const { save, status } = useOrgSettingsAutosave();
  const readinessPolicy = readinessPolicyResponse?.data.config ?? defaultReadinessPolicyConfig();
  const customChecks = customChecksResponse?.data ?? [];
  const repositories = repositoriesResponse?.data ?? [];
  const readinessPreset = readinessPresetValue(readinessPolicy);
  const readinessPresetLabel = READINESS_PRESETS.find((preset) => preset.value === readinessPreset)?.label ?? "Custom";
  const builderBlockingChecks = countRoleEnforcement(readinessPolicy, "builder", "blocking");
  const engineerAdvisoryChecks = countRoleEnforcement(readinessPolicy, "engineer", "advisory");
  const customCheckLabel = `${customChecks.length} custom ${customChecks.length === 1 ? "check" : "checks"}`;
  const updateReadinessPolicy = useMutation({
    mutationFn: (config: PRReadinessPolicyConfig) => api.settings.updatePRReadinessPolicy(config, scopedRepositoryId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.settings.prReadinessPolicy(scopedRepositoryId ?? null) });
    },
  });
  const createCustomCheck = useMutation({
    mutationFn: (check: PRReadinessCustomCheck) => api.settings.createPRReadinessCustomCheck({ ...check, repository_id: scopedRepositoryId }),
    onSuccess: () => {
      setNewCheck(blankReadinessCustomCheck());
      queryClient.invalidateQueries({ queryKey: queryKeys.settings.prReadinessCustomChecks(scopedRepositoryId ?? null) });
    },
  });
  const updateCustomCheck = useMutation({
    mutationFn: (check: PRReadinessCustomCheck) => api.settings.updatePRReadinessCustomCheck(check.id!, { ...check, repository_id: scopedRepositoryId }),
    onSuccess: () => {
      setEditingCheckId(null);
      setNewCheck(blankReadinessCustomCheck());
      queryClient.invalidateQueries({ queryKey: queryKeys.settings.prReadinessCustomChecks(scopedRepositoryId ?? null) });
    },
  });
  const deleteCustomCheck = useMutation({
    mutationFn: (id: string) => api.settings.deletePRReadinessCustomCheck(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.settings.prReadinessCustomChecks(scopedRepositoryId ?? null) });
    },
  });
  const patchReadinessPolicy = (patch: Partial<PRReadinessPolicyConfig>) => {
    updateReadinessPolicy.mutate({ ...readinessPolicy, ...patch });
  };
  const setReadinessPreset = (preset: ReadinessPresetValue) => {
    updateReadinessPolicy.mutate(applyReadinessPreset(readinessPolicy, preset));
  };
  const engineerReadinessEnabled = Object.values(readinessPolicy.checks ?? {}).some((check) => check.enforcement?.engineer && check.enforcement.engineer !== "off");
  const setEngineerReadinessEnabled = (enabled: boolean) => {
    const checks = { ...(readinessPolicy.checks ?? defaultReadinessChecks()) };
    for (const checkKey of READINESS_CHECKS) {
      const existing = checks[checkKey]?.enforcement ?? {};
      const existingEngineer = existing.engineer ?? "off";
      checks[checkKey] = {
        enforcement: {
          builder: existing.builder ?? "advisory",
          // Enabling only promotes checks that are currently off so it doesn't
          // overwrite a per-check "blocking" an admin set in the matrix below;
          // disabling turns engineer enforcement off across the board.
          engineer: enabled ? (existingEngineer !== "off" ? existingEngineer : "advisory") : "off",
          admin: existing.admin ?? "advisory",
        },
      };
    }
    patchReadinessPolicy({ checks });
  };
  const setCheckEnforcement = (checkKey: string, role: "builder" | "engineer" | "admin", enforcement: PRReadinessEnforcement) => {
    const existing = readinessPolicy.checks?.[checkKey]?.enforcement ?? {};
    const next = {
      builder: existing.builder ?? "advisory",
      engineer: existing.engineer ?? "advisory",
      admin: existing.admin ?? "advisory",
      [role]: enforcement,
    };
    patchReadinessPolicy({
      checks: {
        ...(readinessPolicy.checks ?? {}),
        [checkKey]: {
          enforcement: next,
        },
      },
    });
  };

  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between">
        <h2 className="text-xs font-medium text-foreground">Pull requests</h2>
        <AutosaveIndicator status={status} />
      </div>
      <Card>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label>PR authorship</Label>
            <p className="text-xs text-muted-foreground">
              Controls who appears as the author when 143 creates a pull request.
            </p>
            <div className="space-y-1.5">
              {PR_AUTHORSHIP_OPTIONS.map((option) => (
                <label
                  key={option.value}
                  className="flex items-start gap-2 cursor-pointer"
                >
                  <input
                    type="radio"
                    name="pr_authorship"
                    value={option.value}
                    checked={currentAuthorship === option.value}
                    onChange={() =>
                      save({ settings: { pr_authorship: option.value } })
                    }
                    className="mt-0.5"
                  />
                  <div>
                    <span className="text-xs font-medium">{option.label}</span>
                    <p className="text-xs text-muted-foreground">{option.description}</p>
                  </div>
                </label>
              ))}
            </div>
            <p className="text-xs text-muted-foreground">
              {authorshipAccountHint}{" "}
              <Link href="/settings/integrations" className="underline">
                Manage on Integrations
              </Link>
            </p>
          </div>
          <div className="flex items-center gap-2">
            <input
              type="checkbox"
              id="pr-draft-default"
              checked={currentDraftDefault}
              onChange={(e) =>
                save({ settings: { pr_draft_default: e.target.checked } })
              }
            />
            <Label htmlFor="pr-draft-default" className="cursor-pointer">
              Create PRs as drafts by default
            </Label>
          </div>
          <div className="space-y-1">
            <div className="flex items-center gap-2">
              <input
                type="checkbox"
                id="auto-archive-on-pr-close"
                checked={currentAutoArchive}
                onChange={(e) =>
                  save({ settings: { auto_archive_on_pr_close: e.target.checked } })
                }
              />
              <Label htmlFor="auto-archive-on-pr-close" className="cursor-pointer">
                Auto-archive after PR merge or close
              </Label>
            </div>
            <p className="text-xs text-muted-foreground pl-6">
              Automatically archive sessions when their associated pull request is merged or closed.
            </p>
          </div>
          <div className="space-y-3 border-t border-border pt-4">
            <div className="flex items-start justify-between gap-4">
              <div className="space-y-1">
                <Label>PR readiness</Label>
                <p className="text-xs text-muted-foreground">
                  {readinessPresetLabel} policy for {readinessScope === ORG_READINESS_SCOPE ? "Organization default" : repositories.find((repo: Repository) => repo.id === readinessScope)?.full_name ?? "selected repository"}.
                </p>
              </div>
              <Button size="sm" variant="outline" aria-label="Manage readiness policy" onClick={() => setReadinessSheetOpen(true)}>
                Manage
              </Button>
            </div>
            <div className="grid gap-2 sm:grid-cols-4">
              <div className="rounded-md border border-border px-3 py-2">
                <div className="text-xs text-muted-foreground">Scope</div>
                <div className="text-xs font-medium">Organization default</div>
              </div>
              <div className="rounded-md border border-border px-3 py-2">
                <div className="text-xs text-muted-foreground">Builder blocks</div>
                <div className="text-xs font-medium">{builderBlockingChecks} checks</div>
              </div>
              <div className="rounded-md border border-border px-3 py-2">
                <div className="text-xs text-muted-foreground">Engineer advisory</div>
                <div className="text-xs font-medium">{engineerAdvisoryChecks} checks</div>
              </div>
              <div className="rounded-md border border-border px-3 py-2">
                <div className="text-xs text-muted-foreground">Bypasses</div>
                <div className="flex items-center gap-2 text-xs font-medium">
                  <span>{readinessPolicyResponse?.data.bypass_counts?.total ?? 0} total</span>
                  <span className="text-muted-foreground">{customCheckLabel}</span>
                </div>
              </div>
            </div>
          </div>
          <Sheet open={readinessSheetOpen} onOpenChange={setReadinessSheetOpen}>
            <SheetContent className="w-full sm:max-w-3xl">
              <SheetHeader>
                <SheetTitle>PR readiness policy</SheetTitle>
                <SheetDescription>
                  Configure pre-PR checks, bypass behavior, and prompt-based checks for organization defaults or repository overrides.
                </SheetDescription>
              </SheetHeader>
              <div className="space-y-5 pt-4">
                <div className="space-y-2">
                  <Label htmlFor="readiness-preset">Policy preset</Label>
                  <Select value={readinessPreset} onValueChange={(value) => setReadinessPreset(value as ReadinessPresetValue)}>
                    <SelectTrigger id="readiness-preset">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {READINESS_PRESETS.map((preset) => (
                        <SelectItem key={preset.value} value={preset.value}>
                          {preset.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <p className="text-xs text-muted-foreground">
                    {READINESS_PRESETS.find((preset) => preset.value === readinessPreset)?.description ?? READINESS_PRESETS.at(-1)?.description}
                  </p>
                </div>
            <div className="grid gap-3 sm:grid-cols-[240px_1fr]">
              <div className="space-y-1">
                <Label htmlFor="readiness-scope">PR readiness policy</Label>
                <p className="text-xs text-muted-foreground">Configure org defaults or a repository override.</p>
              </div>
              <Select value={readinessScope} onValueChange={selectReadinessScope}>
                <SelectTrigger id="readiness-scope">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ORG_READINESS_SCOPE}>Organization default</SelectItem>
                  {repositories.map((repo: Repository) => (
                    <SelectItem key={repo.id} value={repo.id}>{repo.full_name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex items-start justify-between gap-4">
              <div className="space-y-1">
                <Label htmlFor="readiness-builders">Enable builder readiness policy</Label>
                <p className="text-xs text-muted-foreground">When disabled, builder enforcement is treated as off for this policy scope.</p>
              </div>
              <Switch
                id="readiness-builders"
                checked={readinessPolicy.enabled_for_builders}
                onCheckedChange={(checked) => patchReadinessPolicy({ enabled_for_builders: checked })}
              />
            </div>
            <div className="flex items-start justify-between gap-4">
              <div className="space-y-1">
                <Label htmlFor="readiness-engineers">Enable advisory checks for engineers</Label>
                <p className="text-xs text-muted-foreground">When disabled, engineer enforcement is set to off for every built-in check.</p>
              </div>
              <Switch
                id="readiness-engineers"
                checked={engineerReadinessEnabled}
                onCheckedChange={setEngineerReadinessEnabled}
              />
            </div>
            <div className="space-y-2">
              <Label>Built-in checks</Label>
              <div className="space-y-2">
                {READINESS_CHECKS.map((checkKey) => (
                  <div key={checkKey} className="grid gap-2 rounded-md border border-border px-3 py-2 md:grid-cols-[1fr_repeat(3,140px)]">
                    <div className="text-xs font-medium">{readinessLabel(checkKey)}</div>
                    {READINESS_ROLES.map((role) => (
                      <div key={`${checkKey}-${role.key}`} className="space-y-1">
                        <Label className="text-xs text-muted-foreground">{role.label}</Label>
                        <Select
                          value={readinessPolicy.checks?.[checkKey]?.enforcement?.[role.key] ?? "advisory"}
                          onValueChange={(value) => setCheckEnforcement(checkKey, role.key, value as PRReadinessEnforcement)}
                        >
                          <SelectTrigger>
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            {READINESS_ENFORCEMENTS.map((mode) => (
                              <SelectItem key={mode} value={mode}>{mode}</SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      </div>
                    ))}
                  </div>
                ))}
              </div>
            </div>
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
              <div className="space-y-1">
                <Label htmlFor="readiness-large-files">Large diff files</Label>
                <DebouncedInput
                  id="readiness-large-files"
                  type="number"
                  serverValue={String(readinessPolicy.large_diff_file_threshold ?? 25)}
                  onCommit={(value) => patchReadinessPolicy({ large_diff_file_threshold: parseThreshold(value) })}
                />
              </div>
              <div className="space-y-1">
                <Label htmlFor="readiness-large-lines">Large diff lines</Label>
                <DebouncedInput
                  id="readiness-large-lines"
                  type="number"
                  serverValue={String(readinessPolicy.large_diff_line_threshold ?? 500)}
                  onCommit={(value) => patchReadinessPolicy({ large_diff_line_threshold: parseThreshold(value) })}
                />
              </div>
              <div className="space-y-1">
                <Label htmlFor="readiness-sensitive-paths">Sensitive paths</Label>
                <DebouncedInput
                  id="readiness-sensitive-paths"
                  serverValue={listToCsv(readinessPolicy.sensitive_paths)}
                  onCommit={(value) => patchReadinessPolicy({ sensitive_paths: csvToList(value) })}
                />
              </div>
              <div className="space-y-1">
                <Label htmlFor="readiness-generated-allowed">Allowed generated paths</Label>
                <DebouncedInput
                  id="readiness-generated-allowed"
                  serverValue={listToCsv(readinessPolicy.generated_file_allowed_paths)}
                  onCommit={(value) => patchReadinessPolicy({ generated_file_allowed_paths: csvToList(value) })}
                />
              </div>
            </div>
            <div className="grid gap-3 sm:grid-cols-3">
              <div className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2">
                <Label htmlFor="readiness-bypass" className="text-xs">Bypass enabled</Label>
                <Switch
                  id="readiness-bypass"
                  checked={readinessPolicy.bypass?.enabled ?? true}
                  onCheckedChange={(checked) => patchReadinessPolicy({ bypass: { ...(readinessPolicy.bypass ?? { enabled: true }), enabled: checked } })}
                />
              </div>
              <div className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2">
                <Label htmlFor="readiness-auto-complete" className="text-xs">Auto-run on completion</Label>
                <Switch
                  id="readiness-auto-complete"
                  checked={readinessPolicy.auto_run?.after_session_completion ?? false}
                  onCheckedChange={(checked) => patchReadinessPolicy({ auto_run: { ...(readinessPolicy.auto_run ?? { after_session_completion: false, on_create_pr: false }), after_session_completion: checked } })}
                />
              </div>
              <div className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2">
                <Label htmlFor="readiness-auto-pr" className="text-xs">Auto-run on Create PR</Label>
                <Switch
                  id="readiness-auto-pr"
                  checked={readinessPolicy.auto_run?.on_create_pr ?? false}
                  onCheckedChange={(checked) => patchReadinessPolicy({ auto_run: { ...(readinessPolicy.auto_run ?? { after_session_completion: false, on_create_pr: false }), on_create_pr: checked } })}
                />
              </div>
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-2 rounded-md border border-border px-3 py-2">
                <Label>Bypass roles</Label>
                <div className="flex flex-wrap gap-3">
                  {["admin", "member", "builder"].map((role) => (
                    <label key={role} className="flex items-center gap-2 text-xs">
                      <Checkbox
                        checked={(readinessPolicy.bypass?.allowed_roles ?? ["admin", "member", "builder"]).includes(role)}
                        onCheckedChange={(checked) => patchReadinessPolicy({
                          bypass: {
                            ...(readinessPolicy.bypass ?? { enabled: true }),
                            allowed_roles: toggleRole(readinessPolicy.bypass?.allowed_roles ?? ["admin", "member", "builder"], role, checked === true),
                          },
                        })}
                      />
                      {role === "member" ? "engineer" : role}
                    </label>
                  ))}
                </div>
              </div>
              <div className="space-y-1">
                <Label htmlFor="readiness-non-bypassable">Non-bypassable checks</Label>
                <DebouncedInput
                  id="readiness-non-bypassable"
                  serverValue={listToCsv(readinessPolicy.bypass?.non_bypassable_checks)}
                  onCommit={(value) => patchReadinessPolicy({
                    bypass: {
                      ...(readinessPolicy.bypass ?? { enabled: true }),
                      non_bypassable_checks: csvToList(value),
                    },
                  })}
                />
              </div>
            </div>
            <div className="space-y-2 rounded-md border border-border px-3 py-2">
              <div className="flex items-center justify-between">
                <Label>Bypass counts</Label>
                <Badge variant="outline">{readinessPolicyResponse?.data.bypass_counts?.total ?? 0} total</Badge>
              </div>
              <BypassCounts counts={readinessPolicyResponse?.data.bypass_counts?.by_repository} empty="No repository bypasses yet" />
              <BypassCounts counts={readinessPolicyResponse?.data.bypass_counts?.by_check} empty="No check bypasses yet" />
              <BypassCounts counts={readinessPolicyResponse?.data.bypass_counts?.by_user} empty="No user bypasses yet" />
            </div>
          </div>
          <div className="space-y-3 border-t border-border pt-4">
            <div>
              <Label>Custom prompt checks</Label>
              <p className="text-xs text-muted-foreground">Settings checks can be edited here. Repo config checks are shown with provenance and refreshed from `.143/config.json`.</p>
            </div>
            <div className="space-y-2">
              {customChecks.length === 0 ? (
                <p className="text-xs text-muted-foreground">No custom checks configured.</p>
              ) : customChecks.map((check) => (
                <div key={check.id ?? check.check_key} className="flex items-start justify-between gap-3 rounded-md border border-border px-3 py-2">
                  <div>
                    <div className="text-xs font-medium">{check.name}</div>
                    <div className="text-xs text-muted-foreground">{check.check_key} · {customCheckProvenanceLabel(check)}</div>
                  </div>
                  <div className="flex gap-2">
                    {check.id && customCheckEditableInScope(check, scopedRepositoryId) && (
                      <Button size="xs" variant="outline" onClick={() => {
                        setEditingCheckId(check.id!);
                        setNewCheck(check);
                      }}>
                        Edit
                      </Button>
                    )}
                    {check.id && customCheckEditableInScope(check, scopedRepositoryId) && (
                      <Button size="xs" variant="outline" onClick={() => deleteCustomCheck.mutate(check.id!)}>
                        Delete
                      </Button>
                    )}
                  </div>
                </div>
              ))}
            </div>
            <div className="grid gap-2">
              <Input
                value={newCheck.check_key}
                onChange={(event) => setNewCheck((current) => ({ ...current, check_key: event.target.value }))}
                placeholder="check_key"
              />
              <Input
                value={newCheck.name}
                onChange={(event) => setNewCheck((current) => ({ ...current, name: event.target.value }))}
                placeholder="Check name"
              />
              <div className="grid gap-2 sm:grid-cols-2">
                <Input
                  value={listToCsv(newCheck.paths?.include)}
                  onChange={(event) => setNewCheck((current) => ({ ...current, paths: { ...(current.paths ?? {}), include: csvToList(event.target.value) } }))}
                  placeholder="Include paths"
                />
                <Input
                  value={listToCsv(newCheck.paths?.exclude)}
                  onChange={(event) => setNewCheck((current) => ({ ...current, paths: { ...(current.paths ?? {}), exclude: csvToList(event.target.value) } }))}
                  placeholder="Exclude paths"
                />
              </div>
              <div className="grid gap-2 sm:grid-cols-3">
                {READINESS_ROLES.map((role) => (
                  <Select
                    key={`custom-${role.key}`}
                    value={newCheck.enforcement?.[role.key] ?? "advisory"}
                    onValueChange={(value) => setNewCheck((current) => ({
                      ...current,
                      enforcement: { ...(current.enforcement ?? {}), [role.key]: value as PRReadinessEnforcement },
                    }))}
                  >
                    <SelectTrigger>
                      <SelectValue placeholder={`${role.label} enforcement`} />
                    </SelectTrigger>
                    <SelectContent>
                      {READINESS_ENFORCEMENTS.map((mode) => (
                        <SelectItem key={mode} value={mode}>{role.label}: {mode}</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                ))}
              </div>
              <Textarea
                value={newCheck.prompt}
                onChange={(event) => setNewCheck((current) => ({ ...current, prompt: event.target.value }))}
                rows={4}
                placeholder="Prompt template"
              />
              <div className="flex gap-2">
                <Button
                  size="sm"
                  disabled={(editingCheckId ? updateCustomCheck.isPending : createCustomCheck.isPending) || !newCheck.check_key.trim() || !newCheck.name.trim() || !newCheck.prompt.trim()}
                  onClick={() => editingCheckId ? updateCustomCheck.mutate({ ...newCheck, id: editingCheckId }) : createCustomCheck.mutate(newCheck)}
                >
                  {editingCheckId ? "Save custom check" : "Add custom check"}
                </Button>
                {editingCheckId && (
                  <Button size="sm" variant="outline" onClick={() => {
                    setEditingCheckId(null);
                    setNewCheck(blankReadinessCustomCheck());
                  }}>
                    Cancel
                  </Button>
                )}
              </div>
            </div>
          </div>
            </SheetContent>
          </Sheet>
        </CardContent>
      </Card>
    </section>
  );
}

function BypassCounts({ counts, empty }: { counts?: Array<{ key: string; count: number }>; empty: string }) {
  if (!counts || counts.length === 0) {
    return <p className="text-xs text-muted-foreground">{empty}</p>;
  }
  return (
    <div className="flex flex-wrap gap-2">
      {counts.slice(0, 6).map((count) => (
        <Badge key={count.key} variant="secondary">
          {count.key}: {count.count}
        </Badge>
      ))}
    </div>
  );
}

function defaultReadinessPolicyConfig(): PRReadinessPolicyConfig {
  return {
    enabled_for_builders: true,
    checks: defaultReadinessChecks(),
    bypass: {
      enabled: true,
      allowed_roles: ["admin", "member", "builder"],
      scopes: ["completed_blocking_checks"],
      non_bypassable_checks: [],
    },
    auto_run: { after_session_completion: false, on_create_pr: false },
    sensitive_paths: ["*auth*", "*security*", "*billing*", ".github/workflows/**", "deploy/**", "infra/**", "terraform/**"],
    generated_file_allowed_paths: [],
    large_diff_file_threshold: 25,
    large_diff_line_threshold: 500,
  };
}

export default function SettingsPage() {
  const { user } = useAuth();
  const { data: settings } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });
  const autosave = useOrgSettingsAutosave();

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="Organization"
          description="Manage your organization."
        />

        <section className="space-y-3">
          <div className="flex items-center justify-between">
            <h2 className="text-xs font-medium text-foreground">Organization</h2>
            {user?.role === "admin" && <AutosaveIndicator status={autosave.status} />}
          </div>
          <Card>
            <CardContent>
              <div className="max-w-[560px] space-y-2">
                <Label htmlFor="org-name">Organization name</Label>
                <DebouncedInput
                  id="org-name"
                  serverValue={settings?.data?.name ?? ""}
                  onCommit={(name) => autosave.save({ name })}
                  disabled={user?.role !== "admin"}
                  className={user?.role !== "admin" ? "bg-muted" : undefined}
                />
              </div>
            </CardContent>
          </Card>
        </section>

        {user?.role === "admin" && <PRAuthorshipSettings />}

        <SettingsLastActivity
          scopes={{ resource_type: "settings" }}
          title="Settings activity"
        />
      </div>
    </PageContainer>
  );
}
