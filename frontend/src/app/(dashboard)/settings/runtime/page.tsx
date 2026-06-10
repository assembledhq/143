"use client";

import { useQuery } from "@tanstack/react-query";
import { Network } from "lucide-react";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { CopyButton } from "@/components/copy-button";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useAutosaveNumericField } from "@/hooks/useAutosaveNumericField";
import { useOrgSettingsAutosave } from "@/hooks/use-org-settings-autosave";
import { usePageTitle } from "@/hooks/use-page-title";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import {
  DEFAULT_COMPLETED_SESSION_RETENTION_MINUTES,
  DEFAULT_IDLE_PREVIEW_TTL_MINUTES,
  DEFAULT_PREVIEW_MAX_CPU_MILLIS,
  DEFAULT_PREVIEW_MAX_EPHEMERAL_DISK_MIB,
  DEFAULT_PREVIEW_MAX_MEMORY_MIB,
  DEFAULT_PREVIEW_MAX_PREVIEWS_PER_USER,
  MAX_COMPLETED_SESSION_RETENTION_MINUTES,
  MAX_CONCURRENT_RUNS,
  MAX_IDLE_PREVIEW_TTL_MINUTES,
  MAX_PREVIEW_MAX_CPU_MILLIS,
  MAX_PREVIEW_MAX_EPHEMERAL_DISK_MIB,
  MAX_PREVIEW_MAX_MEMORY_MIB,
  MAX_PREVIEW_MAX_PREVIEWS_PER_USER,
  MAX_SESSION_DURATION_MINUTES,
  MIN_COMPLETED_SESSION_RETENTION_MINUTES,
  MIN_CONCURRENT_RUNS,
  MIN_IDLE_PREVIEW_TTL_MINUTES,
  MIN_PREVIEW_MAX_CPU_MILLIS,
  MIN_PREVIEW_MAX_EPHEMERAL_DISK_MIB,
  MIN_PREVIEW_MAX_MEMORY_MIB,
  MIN_PREVIEW_MAX_PREVIEWS_PER_USER,
  MIN_SESSION_DURATION_MINUTES,
  clampNumber,
} from "@/lib/settings-constants";
import type { Organization, OrgSettings, SandboxResourceTier, SingleResponse } from "@/lib/types";

const DEFAULT_EXECUTION_SETTINGS = {
  max_concurrent_runs: 5,
  max_session_duration_seconds: 25 * 60,
};

const RESOURCE_TIERS: { value: SandboxResourceTier; label: string; description: string }[] = [
  { value: "small", label: "Small", description: "Lower CPU and memory for lightweight tasks." },
  { value: "standard", label: "Standard", description: "Default runtime resources for most work." },
  { value: "large", label: "Large", description: "Higher CPU and memory for heavier builds." },
];

function useOrgSettingsQuery() {
  return useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });
}

function SectionHeader({
  title,
  status,
}: {
  title: string;
  status?: ReturnType<typeof useOrgSettingsAutosave>["status"];
}) {
  return (
    <div className="flex items-center justify-between">
      <h2 className="text-xs font-medium text-foreground">{title}</h2>
      {status ? <AutosaveIndicator status={status} /> : null}
    </div>
  );
}

function ResourceTierSelect({
  id,
  label,
  value,
  onChange,
}: {
  id: string;
  label: string;
  value: SandboxResourceTier;
  onChange: (value: SandboxResourceTier) => void;
}) {
  return (
    <Select value={value} onValueChange={(nextValue) => onChange(nextValue as SandboxResourceTier)}>
      <SelectTrigger id={id} aria-label={label}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {RESOURCE_TIERS.map((tier) => (
          <SelectItem key={tier.value} value={tier.value}>
            {tier.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

function SandboxNetworkSection() {
  const { data: settingsResponse } = useOrgSettingsQuery();
  const { data: networkStatusResponse } = useQuery({
    queryKey: queryKeys.settings.network,
    queryFn: () => api.settings.getNetworkStatus(),
  });
  const autosave = useOrgSettingsAutosave();

  const settings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;
  const sandboxNetwork = settings.sandbox_network ?? {};
  const networkStatus = networkStatusResponse?.data;
  const available = networkStatus?.static_egress_available ?? false;
  const publicIP = networkStatus?.static_egress_public_ip;
  const enabled = sandboxNetwork.static_egress_enabled ?? networkStatus?.static_egress_enabled ?? false;

  return (
    <section className="space-y-3">
      <SectionHeader title="Sandbox network" status={autosave.status} />
      <Card>
        <CardContent className="space-y-4">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
            <div className="space-y-1">
              <Label htmlFor="static-egress-enabled">Use static egress IP for sessions and previews</Label>
              <p className="text-xs text-muted-foreground">
                Uses a stable public IP for new and hydrated sandboxes.
              </p>
              {enabled && networkStatusResponse && !available && (
                <p className="text-xs text-muted-foreground">
                  Static egress is not currently available for new sandbox starts.
                </p>
              )}
            </div>
            <Switch
              id="static-egress-enabled"
              checked={enabled}
              onCheckedChange={(checked) => {
                autosave.save({
                  settings: {
                    sandbox_network: {
                      ...sandboxNetwork,
                      static_egress_enabled: checked,
                    },
                  },
                });
              }}
              aria-label="Use static egress IP for sessions and previews"
            />
          </div>
          <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-muted/30 px-3 py-2">
            <span className="text-xs text-muted-foreground">Public IP</span>
            <code className="font-mono text-xs text-foreground">{publicIP ?? "Not configured"}</code>
            <CopyButton value={publicIP} label="Copy static egress public IP" />
          </div>
        </CardContent>
      </Card>
    </section>
  );
}

function CapacityLimitsSection() {
  const { data: settingsResponse } = useOrgSettingsQuery();
  const autosave = useOrgSettingsAutosave();

  const settings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;
  const maxConcurrentRuns =
    settings.max_concurrent_runs ?? DEFAULT_EXECUTION_SETTINGS.max_concurrent_runs;
  const maxPreviewsPerUser =
    settings.preview_max_previews_per_user ?? DEFAULT_PREVIEW_MAX_PREVIEWS_PER_USER;

  const concurrentRunsField = useAutosaveNumericField({
    serverValue: maxConcurrentRuns,
    autosave,
    toPatch: (value) => ({ settings: { max_concurrent_runs: value } }),
    clamp: (value) => clampNumber(value, MIN_CONCURRENT_RUNS, MAX_CONCURRENT_RUNS),
  });
  const maxPreviewsPerUserField = useAutosaveNumericField({
    serverValue: maxPreviewsPerUser,
    autosave,
    toPatch: (value) => ({ settings: { preview_max_previews_per_user: value } }),
    clamp: (value) =>
      clampNumber(
        value,
        MIN_PREVIEW_MAX_PREVIEWS_PER_USER,
        MAX_PREVIEW_MAX_PREVIEWS_PER_USER,
      ),
  });

  return (
    <section className="space-y-3">
      <SectionHeader title="Capacity limits" status={autosave.status} />
      <Card>
        <CardContent className="grid gap-4 md:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="max-concurrent-runs">Concurrent coding-agent runs</Label>
            <Input
              id="max-concurrent-runs"
              type="number"
              inputMode="numeric"
              min={MIN_CONCURRENT_RUNS}
              max={MAX_CONCURRENT_RUNS}
              value={concurrentRunsField.value}
              onChange={concurrentRunsField.onChange}
              onBlur={concurrentRunsField.onBlur}
            />
            <p className="text-xs text-muted-foreground">
              Limits how many agent turns can run for the org at once.
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="preview-max-previews-per-user">Active previews per user</Label>
            <Input
              id="preview-max-previews-per-user"
              type="number"
              inputMode="numeric"
              min={MIN_PREVIEW_MAX_PREVIEWS_PER_USER}
              max={MAX_PREVIEW_MAX_PREVIEWS_PER_USER}
              value={maxPreviewsPerUserField.value}
              onChange={maxPreviewsPerUserField.onChange}
              onBlur={maxPreviewsPerUserField.onBlur}
            />
            <p className="text-xs text-muted-foreground">
              Limits how many previews one user can keep running at once.
            </p>
          </div>
        </CardContent>
      </Card>
    </section>
  );
}

function LifecycleDefaultsSection() {
  const { data: settingsResponse } = useOrgSettingsQuery();
  const autosave = useOrgSettingsAutosave();

  const settings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;
  const lifecycle = settings.sandbox_lifecycle ?? {};
  const completedRetentionMinutes =
    lifecycle.completed_session_retention_minutes ?? DEFAULT_COMPLETED_SESSION_RETENTION_MINUTES;
  const idlePreviewTTLMinutes = lifecycle.idle_preview_ttl_minutes ?? DEFAULT_IDLE_PREVIEW_TTL_MINUTES;
  const previewHoldsSandbox = lifecycle.preview_holds_sandbox ?? true;

  const completedRetentionField = useAutosaveNumericField({
    serverValue: completedRetentionMinutes,
    autosave,
    toPatch: (value) => ({
      settings: { sandbox_lifecycle: { completed_session_retention_minutes: value } },
    }),
    clamp: (value) =>
      clampNumber(
        value,
        MIN_COMPLETED_SESSION_RETENTION_MINUTES,
        MAX_COMPLETED_SESSION_RETENTION_MINUTES,
      ),
  });
  const idlePreviewTTLField = useAutosaveNumericField({
    serverValue: idlePreviewTTLMinutes,
    autosave,
    toPatch: (value) => ({
      settings: { sandbox_lifecycle: { idle_preview_ttl_minutes: value } },
    }),
    clamp: (value) =>
      clampNumber(value, MIN_IDLE_PREVIEW_TTL_MINUTES, MAX_IDLE_PREVIEW_TTL_MINUTES),
  });

  return (
    <section className="space-y-3">
      <SectionHeader title="Lifecycle defaults" status={autosave.status} />
      <Card>
        <CardContent className="space-y-4">
          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="completed-session-retention">Completed session retention</Label>
              <div className="flex items-center gap-2">
                <Input
                  id="completed-session-retention"
                  type="number"
                  inputMode="numeric"
                  min={MIN_COMPLETED_SESSION_RETENTION_MINUTES}
                  max={MAX_COMPLETED_SESSION_RETENTION_MINUTES}
                  value={completedRetentionField.value}
                  onChange={completedRetentionField.onChange}
                  onBlur={completedRetentionField.onBlur}
                />
                <span className="text-xs text-muted-foreground">minutes</span>
              </div>
              <p className="text-xs text-muted-foreground">
                Keeps completed sandbox state available before cleanup.
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="idle-preview-ttl">Idle preview TTL</Label>
              <div className="flex items-center gap-2">
                <Input
                  id="idle-preview-ttl"
                  type="number"
                  inputMode="numeric"
                  min={MIN_IDLE_PREVIEW_TTL_MINUTES}
                  max={MAX_IDLE_PREVIEW_TTL_MINUTES}
                  value={idlePreviewTTLField.value}
                  onChange={idlePreviewTTLField.onChange}
                  onBlur={idlePreviewTTLField.onBlur}
                />
                <span className="text-xs text-muted-foreground">minutes</span>
              </div>
              <p className="text-xs text-muted-foreground">
                Stops idle preview sandboxes after the configured window.
              </p>
            </div>
          </div>
          <div className="flex flex-col gap-3 rounded-md border border-border p-3 sm:flex-row sm:items-center sm:justify-between">
            <div className="space-y-1">
              <Label htmlFor="preview-holds-sandbox">Preview holds sandbox</Label>
              <p className="text-xs text-muted-foreground">
                Keeps the preview sandbox allocated while the preview remains active.
              </p>
            </div>
            <Switch
              id="preview-holds-sandbox"
              checked={previewHoldsSandbox}
              onCheckedChange={(checked) => {
                autosave.save({ settings: { sandbox_lifecycle: { preview_holds_sandbox: checked } } });
              }}
              aria-label="Preview holds sandbox"
            />
          </div>
        </CardContent>
      </Card>
    </section>
  );
}

function ResourceDefaultsSection() {
  const { data: settingsResponse } = useOrgSettingsQuery();
  const autosave = useOrgSettingsAutosave();

  const settings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;
  const resources = settings.sandbox_resources ?? {};
  const agentDefaultTier = resources.agent_default_tier ?? "standard";
  const previewDefaultTier = resources.preview_default_tier ?? "standard";
  const allowRepoResourceRequests = resources.allow_repo_resource_requests ?? true;
  const previewMaxTier = resources.preview_max_tier ?? "large";
  const previewMaxCPUMillis =
    resources.preview_max_cpu_millis ?? DEFAULT_PREVIEW_MAX_CPU_MILLIS;
  const previewMaxMemoryMiB =
    resources.preview_max_memory_mib ?? DEFAULT_PREVIEW_MAX_MEMORY_MIB;
  const previewMaxEphemeralDiskMiB =
    resources.preview_max_ephemeral_disk_mib ?? DEFAULT_PREVIEW_MAX_EPHEMERAL_DISK_MIB;

  const previewMaxCPUField = useAutosaveNumericField({
    serverValue: previewMaxCPUMillis,
    autosave,
    toPatch: (value) => ({ settings: { sandbox_resources: { preview_max_cpu_millis: value } } }),
    clamp: (value) =>
      clampNumber(value, MIN_PREVIEW_MAX_CPU_MILLIS, MAX_PREVIEW_MAX_CPU_MILLIS),
  });
  const previewMaxMemoryField = useAutosaveNumericField({
    serverValue: previewMaxMemoryMiB,
    autosave,
    toPatch: (value) => ({ settings: { sandbox_resources: { preview_max_memory_mib: value } } }),
    clamp: (value) =>
      clampNumber(value, MIN_PREVIEW_MAX_MEMORY_MIB, MAX_PREVIEW_MAX_MEMORY_MIB),
  });
  const previewMaxDiskField = useAutosaveNumericField({
    serverValue: previewMaxEphemeralDiskMiB,
    autosave,
    toPatch: (value) => ({
      settings: { sandbox_resources: { preview_max_ephemeral_disk_mib: value } },
    }),
    clamp: (value) =>
      clampNumber(
        value,
        MIN_PREVIEW_MAX_EPHEMERAL_DISK_MIB,
        MAX_PREVIEW_MAX_EPHEMERAL_DISK_MIB,
      ),
  });

  return (
    <section className="space-y-3">
      <SectionHeader title="Resource defaults" status={autosave.status} />
      <Card>
        <CardContent className="space-y-4">
          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="agent-default-tier">Agent default tier</Label>
              <ResourceTierSelect
                id="agent-default-tier"
                label="Agent default tier"
                value={agentDefaultTier}
                onChange={(value) => {
                  autosave.save({ settings: { sandbox_resources: { agent_default_tier: value } } });
                }}
              />
              <p className="text-xs text-muted-foreground">
                Default sandbox size for new coding-agent sessions.
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="preview-default-tier">Preview default tier</Label>
              <ResourceTierSelect
                id="preview-default-tier"
                label="Preview default tier"
                value={previewDefaultTier}
                onChange={(value) => {
                  autosave.save({ settings: { sandbox_resources: { preview_default_tier: value } } });
                }}
              />
              <p className="text-xs text-muted-foreground">
                Default sandbox size for previews when repo config does not request one.
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="preview-max-tier">Preview max tier</Label>
              <ResourceTierSelect
                id="preview-max-tier"
                label="Preview max tier"
                value={previewMaxTier}
                onChange={(value) => {
                  autosave.save({ settings: { sandbox_resources: { preview_max_tier: value } } });
                }}
              />
              <p className="text-xs text-muted-foreground">
                Highest resource tier previews may request from repo configuration.
              </p>
            </div>
            <div className="flex flex-col gap-3 rounded-md border border-border p-3 sm:flex-row sm:items-center sm:justify-between">
              <div className="space-y-1">
                <Label htmlFor="allow-repo-resource-requests">Allow repo resource requests</Label>
                <p className="text-xs text-muted-foreground">
                  Allows repository preview config to request CPU, memory, and disk up to the org limits.
                </p>
              </div>
              <Switch
                id="allow-repo-resource-requests"
                checked={allowRepoResourceRequests}
                onCheckedChange={(checked) => {
                  autosave.save({
                    settings: { sandbox_resources: { allow_repo_resource_requests: checked } },
                  });
                }}
                aria-label="Allow repo resource requests"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="preview-max-cpu-millis">Preview CPU request max</Label>
              <div className="flex items-center gap-2">
                <Input
                  id="preview-max-cpu-millis"
                  type="number"
                  inputMode="numeric"
                  min={MIN_PREVIEW_MAX_CPU_MILLIS}
                  max={MAX_PREVIEW_MAX_CPU_MILLIS}
                  value={previewMaxCPUField.value}
                  onChange={previewMaxCPUField.onChange}
                  onBlur={previewMaxCPUField.onBlur}
                />
                <span className="text-xs text-muted-foreground">millicores</span>
              </div>
              <p className="text-xs text-muted-foreground">
                Hard platform cap is {MAX_PREVIEW_MAX_CPU_MILLIS} millicores.
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="preview-max-memory-mib">Preview memory request max</Label>
              <div className="flex items-center gap-2">
                <Input
                  id="preview-max-memory-mib"
                  type="number"
                  inputMode="numeric"
                  min={MIN_PREVIEW_MAX_MEMORY_MIB}
                  max={MAX_PREVIEW_MAX_MEMORY_MIB}
                  value={previewMaxMemoryField.value}
                  onChange={previewMaxMemoryField.onChange}
                  onBlur={previewMaxMemoryField.onBlur}
                />
                <span className="text-xs text-muted-foreground">MiB</span>
              </div>
              <p className="text-xs text-muted-foreground">
                Hard platform cap is {MAX_PREVIEW_MAX_MEMORY_MIB} MiB.
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="preview-max-ephemeral-disk-mib">Preview ephemeral disk request max</Label>
              <div className="flex items-center gap-2">
                <Input
                  id="preview-max-ephemeral-disk-mib"
                  type="number"
                  inputMode="numeric"
                  min={MIN_PREVIEW_MAX_EPHEMERAL_DISK_MIB}
                  max={MAX_PREVIEW_MAX_EPHEMERAL_DISK_MIB}
                  value={previewMaxDiskField.value}
                  onChange={previewMaxDiskField.onChange}
                  onBlur={previewMaxDiskField.onBlur}
                />
                <span className="text-xs text-muted-foreground">MiB</span>
              </div>
              <p className="text-xs text-muted-foreground">
                Hard platform cap is {MAX_PREVIEW_MAX_EPHEMERAL_DISK_MIB} MiB.
              </p>
            </div>
          </div>
        </CardContent>
      </Card>
    </section>
  );
}

function SessionRuntimeSection() {
  const { data: settingsResponse } = useOrgSettingsQuery();
  const autosave = useOrgSettingsAutosave();

  const settings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;
  const sessionMinutes = Math.round(
    (settings.max_session_duration_seconds ?? DEFAULT_EXECUTION_SETTINGS.max_session_duration_seconds) / 60,
  );
  const tabToolsEnabled = settings.coding_agent_tab_tools_enabled ?? true;
  const maxSessionMinutesField = useAutosaveNumericField({
    serverValue: sessionMinutes,
    autosave,
    toPatch: (minutes) => ({ settings: { max_session_duration_seconds: minutes * 60 } }),
    clamp: (value) => clampNumber(value, MIN_SESSION_DURATION_MINUTES, MAX_SESSION_DURATION_MINUTES),
  });

  return (
    <section className="space-y-3">
      <SectionHeader title="Session runtime" status={autosave.status} />
      <Card>
        <CardContent className="space-y-4">
          <div className="max-w-[560px] space-y-2">
            <Label htmlFor="max-session-minutes">Maximum session duration</Label>
            <div className="flex items-center gap-2">
              <Input
                id="max-session-minutes"
                type="number"
                inputMode="numeric"
                min={MIN_SESSION_DURATION_MINUTES}
                max={MAX_SESSION_DURATION_MINUTES}
                value={maxSessionMinutesField.value}
                onChange={maxSessionMinutesField.onChange}
                onBlur={maxSessionMinutesField.onBlur}
              />
              <span className="text-xs text-muted-foreground">minutes</span>
            </div>
            <p className="text-xs text-muted-foreground">
              Stops long-running turns after the configured org limit.
            </p>
          </div>
          <div className="flex flex-col gap-3 rounded-md border border-border p-3 sm:flex-row sm:items-center sm:justify-between">
            <div className="space-y-1">
              <Label htmlFor="sandbox-tab-tools">Sandbox tab tools</Label>
              <p className="text-xs text-muted-foreground">
                Allows agent tabs in the same session to coordinate through the 143 tools CLI.
              </p>
            </div>
            <Switch
              id="sandbox-tab-tools"
              checked={tabToolsEnabled}
              onCheckedChange={(checked) => {
                autosave.save({ settings: { coding_agent_tab_tools_enabled: checked } });
              }}
              aria-label="Sandbox tab tools"
            />
          </div>
        </CardContent>
      </Card>
    </section>
  );
}

function RuntimeDiagnosticsSection() {
  const { data: runtimeStatusResponse } = useQuery({
    queryKey: queryKeys.settings.runtimeStatus,
    queryFn: () => api.settings.getRuntimeStatus(),
  });

  const runtimeStatus = runtimeStatusResponse?.data;
  const staticEgress = runtimeStatus?.static_egress;
  const capacity = runtimeStatus?.capacity;
  const staticEgressAvailable = staticEgress?.available ?? false;
  const staticEgressEnabled = staticEgress?.enabled ?? false;
  const capacityState = capacity?.state === "limited" ? "Limited" : "Normal";

  const rows = [
    {
      label: "Static egress",
      value: staticEgressAvailable ? "Available" : "Unavailable",
      detail: staticEgressEnabled ? "Enabled for new sandbox starts" : "Disabled for new sandbox starts",
      tone: staticEgressAvailable ? "default" : "secondary",
    },
    {
      label: "Agent runs",
      value: capacity
        ? `${capacity.active_agent_runs} / ${capacity.max_concurrent_agent_runs}`
        : "Loading",
      detail: "Active coding-agent runs against the org concurrency limit",
      tone: "secondary",
    },
    {
      label: "Active previews",
      value: capacity
        ? `${capacity.active_previews} / ${capacity.max_previews_per_user}`
        : "Loading",
      detail: "Active previews against the per-user limit",
      tone: "secondary",
    },
    {
      label: "Capacity",
      value: capacityState,
      detail: capacity?.state === "limited" ? "One or more runtime limits is currently saturated" : "Runtime capacity is within configured limits",
      tone: capacity?.state === "limited" ? "destructive" : "default",
    },
  ] satisfies {
    label: string;
    value: string;
    detail: string;
    tone: "default" | "secondary" | "destructive";
  }[];

  return (
    <section className="space-y-3">
      <SectionHeader title="Runtime diagnostics" />
      <Card>
        <CardContent className="divide-y divide-border/60 p-0">
          {rows.map((row) => (
            <div
              key={row.label}
              className="grid gap-2 px-4 py-3 sm:grid-cols-[minmax(0,180px)_minmax(0,1fr)_auto] sm:items-center"
            >
              <div className="text-xs font-medium text-foreground">{row.label}</div>
              <div className="text-xs text-muted-foreground">{row.detail}</div>
              <div>
                <Badge variant={row.tone}>{row.value}</Badge>
              </div>
            </div>
          ))}
        </CardContent>
      </Card>
    </section>
  );
}

export default function RuntimeSettingsPage() {
  usePageTitle("Runtime settings");

  return (
    <PageContainer size="default">
      <div className="space-y-8">
        <PageHeader
          title="Runtime"
          description="Configure sandbox networking, capacity, and lifecycle defaults."
        />
        <div className="grid gap-3 rounded-md border border-border bg-muted/30 px-3 py-3 text-xs text-muted-foreground md:grid-cols-[auto_minmax(0,1fr)]">
          <Network className="h-4 w-4 text-muted-foreground" />
          <p>
            These settings apply to sandbox runtimes across coding-agent sessions and previews.
          </p>
        </div>
        <SandboxNetworkSection />
        <CapacityLimitsSection />
        <SessionRuntimeSection />
        <LifecycleDefaultsSection />
        <ResourceDefaultsSection />
        <RuntimeDiagnosticsSection />
      </div>
    </PageContainer>
  );
}
