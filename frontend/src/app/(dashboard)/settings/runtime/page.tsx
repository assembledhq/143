"use client";

import { useEffect, useRef, useState, type ChangeEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { CircleHelp } from "lucide-react";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { CopyButton } from "@/components/copy-button";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
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

const CPU_MILLIS_PER_CORE = 1000;
const CPU_MILLIS_STEP = 250;

const RESOURCE_TIERS: { value: SandboxResourceTier; label: string }[] = [
  { value: "small", label: "Small" },
  { value: "standard", label: "Standard" },
  { value: "large", label: "Large" },
];

function formatCPUCores(millis: number): string {
  const cores = millis / CPU_MILLIS_PER_CORE;
  return Number.isInteger(cores)
    ? String(cores)
    : cores.toFixed(2).replace(/0+$/, "").replace(/\.$/, "");
}

function cpuCoresToMillis(cores: number): number {
  return Math.round((cores * CPU_MILLIS_PER_CORE) / CPU_MILLIS_STEP) * CPU_MILLIS_STEP;
}

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

function SettingLabel({
  htmlFor,
  label,
  tooltip,
}: {
  htmlFor: string;
  label: string;
  tooltip: string;
}) {
  const ariaLabel = `About ${label.charAt(0).toLowerCase()}${label.slice(1)}`;

  return (
    <div className="flex items-center gap-1.5">
      <Label htmlFor={htmlFor}>{label}</Label>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="h-5 w-5 rounded-full text-muted-foreground hover:text-foreground"
            aria-label={ariaLabel}
          >
            <CircleHelp className="h-3.5 w-3.5" />
          </Button>
        </TooltipTrigger>
        <TooltipContent side="top" sideOffset={6} className="max-w-72">
          {tooltip}
        </TooltipContent>
      </Tooltip>
    </div>
  );
}

function MaxHint({ children }: { children: string }) {
  return <p className="text-xs text-muted-foreground">{children}</p>;
}

function usePreviewCPUField({
  serverMillis,
  autosave,
}: {
  serverMillis: number;
  autosave: ReturnType<typeof useOrgSettingsAutosave>;
}) {
  const [trackedServerMillis, setTrackedServerMillis] = useState(serverMillis);
  const [local, setLocal] = useState(() => formatCPUCores(serverMillis));
  const [lastSentMillis, setLastSentMillis] = useState(serverMillis);
  const debounceTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pendingMillisRef = useRef<number | null>(null);
  const hasEditedRef = useRef(false);

  if (serverMillis !== trackedServerMillis) {
    setTrackedServerMillis(serverMillis);
    const hasPendingEdit = local !== formatCPUCores(lastSentMillis);
    if (serverMillis !== lastSentMillis && !hasPendingEdit) {
      setLocal(formatCPUCores(serverMillis));
      setLastSentMillis(serverMillis);
    }
  }

  useEffect(() => {
    return () => {
      if (debounceTimerRef.current) {
        clearTimeout(debounceTimerRef.current);
        debounceTimerRef.current = null;
      }
    };
  }, []);

  const clampMillis = (cores: number) =>
    clampNumber(
      cpuCoresToMillis(cores),
      MIN_PREVIEW_MAX_CPU_MILLIS,
      MAX_PREVIEW_MAX_CPU_MILLIS,
    );

  const dispatch = (millis: number) => {
    setLastSentMillis(millis);
    autosave.save({ settings: { sandbox_resources: { preview_max_cpu_millis: millis } } });
  };

  const onChange = (event: ChangeEvent<HTMLInputElement>) => {
    const raw = event.target.value;
    hasEditedRef.current = true;
    setLocal(raw);
    if (raw.trim() === "") return;
    const parsed = Number.parseFloat(raw);
    if (Number.isNaN(parsed)) return;
    const clampedMillis = clampMillis(parsed);
    pendingMillisRef.current = clampedMillis;
    if (debounceTimerRef.current) clearTimeout(debounceTimerRef.current);
    debounceTimerRef.current = setTimeout(() => {
      debounceTimerRef.current = null;
      const millis = pendingMillisRef.current;
      pendingMillisRef.current = null;
      if (millis !== null) dispatch(millis);
    }, 400);
  };

  const onBlur = () => {
    if (debounceTimerRef.current) {
      clearTimeout(debounceTimerRef.current);
      debounceTimerRef.current = null;
    }
    if (!hasEditedRef.current) {
      pendingMillisRef.current = null;
      return;
    }
    const parsed = Number.parseFloat(local);
    if (Number.isNaN(parsed)) {
      setLocal(formatCPUCores(serverMillis));
      setLastSentMillis(serverMillis);
      pendingMillisRef.current = null;
      hasEditedRef.current = false;
      return;
    }
    const clampedMillis = clampMillis(parsed);
    setLocal(formatCPUCores(clampedMillis));
    pendingMillisRef.current = null;
    hasEditedRef.current = false;
    if (clampedMillis !== lastSentMillis) dispatch(clampedMillis);
  };

  return { value: local, onChange, onBlur };
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
              <SettingLabel
                htmlFor="static-egress-enabled"
                label="Static egress IP"
                tooltip="Routes new and resumed sandboxes through one stable public IP so allowlists work."
              />
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
              aria-label="Static egress IP"
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
      <SectionHeader title="Usage limits" status={autosave.status} />
      <Card>
        <CardContent className="grid gap-4 md:grid-cols-2">
          <div className="space-y-2">
            <SettingLabel
              htmlFor="max-concurrent-runs"
              label="Concurrent agent runs"
              tooltip="Maximum number of coding-agent turns that can run for this organization at the same time."
            />
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
            <MaxHint>{`Max ${MAX_CONCURRENT_RUNS}`}</MaxHint>
          </div>
          <div className="space-y-2">
            <SettingLabel
              htmlFor="preview-max-previews-per-user"
              label="Active previews per user"
              tooltip="Maximum number of preview environments one user can keep running at the same time."
            />
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
            <MaxHint>{`Max ${MAX_PREVIEW_MAX_PREVIEWS_PER_USER}`}</MaxHint>
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
      <SectionHeader title="Cleanup defaults" status={autosave.status} />
      <Card>
        <CardContent className="space-y-4">
          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-2">
              <SettingLabel
                htmlFor="completed-session-retention"
                label="Keep completed sessions for"
                tooltip="How long completed session sandboxes stay available before cleanup."
              />
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
              <MaxHint>{`Max ${MAX_COMPLETED_SESSION_RETENTION_MINUTES} minutes`}</MaxHint>
            </div>
            <div className="space-y-2">
              <SettingLabel
                htmlFor="idle-preview-ttl"
                label="Idle preview timeout"
                tooltip="Stops a preview sandbox after it sits idle for this long."
              />
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
              <MaxHint>{`Max ${MAX_IDLE_PREVIEW_TTL_MINUTES} minutes`}</MaxHint>
            </div>
          </div>
          <div className="flex flex-col gap-3 rounded-md border border-border p-3 sm:flex-row sm:items-center sm:justify-between">
            <div className="space-y-1">
              <SettingLabel
                htmlFor="preview-holds-sandbox"
                label="Keep sandbox while preview is active"
                tooltip="Keeps the sandbox allocated for as long as the preview is running, even if the coding session has finished."
              />
            </div>
            <Switch
              id="preview-holds-sandbox"
              checked={previewHoldsSandbox}
              onCheckedChange={(checked) => {
                autosave.save({ settings: { sandbox_lifecycle: { preview_holds_sandbox: checked } } });
              }}
              aria-label="Keep sandbox while preview is active"
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

  const previewMaxCPUField = usePreviewCPUField({
    serverMillis: previewMaxCPUMillis,
    autosave,
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
              <SettingLabel
                htmlFor="agent-default-tier"
                label="Agent sandbox size"
                tooltip="Default sandbox size for new coding-agent sessions."
              />
              <ResourceTierSelect
                id="agent-default-tier"
                label="Agent sandbox size"
                value={agentDefaultTier}
                onChange={(value) => {
                  autosave.save({ settings: { sandbox_resources: { agent_default_tier: value } } });
                }}
              />
            </div>
            <div className="space-y-2">
              <SettingLabel
                htmlFor="preview-default-tier"
                label="Preview sandbox size"
                tooltip="Default sandbox size for previews when the repository does not request one."
              />
              <ResourceTierSelect
                id="preview-default-tier"
                label="Preview sandbox size"
                value={previewDefaultTier}
                onChange={(value) => {
                  autosave.save({ settings: { sandbox_resources: { preview_default_tier: value } } });
                }}
              />
            </div>
            <div className="space-y-2">
              <SettingLabel
                htmlFor="preview-max-tier"
                label="Largest preview size"
                tooltip="Largest sandbox size a repository preview config can request."
              />
              <ResourceTierSelect
                id="preview-max-tier"
                label="Largest preview size"
                value={previewMaxTier}
                onChange={(value) => {
                  autosave.save({ settings: { sandbox_resources: { preview_max_tier: value } } });
                }}
              />
            </div>
            <div className="flex flex-col gap-3 rounded-md border border-border p-3 sm:flex-row sm:items-center sm:justify-between">
              <div className="space-y-1">
                <SettingLabel
                  htmlFor="allow-repo-resource-requests"
                  label="Allow repository resource requests"
                  tooltip="Lets repository preview config request CPU, memory, and disk up to the organization limits."
                />
              </div>
              <Switch
                id="allow-repo-resource-requests"
                checked={allowRepoResourceRequests}
                onCheckedChange={(checked) => {
                  autosave.save({
                    settings: { sandbox_resources: { allow_repo_resource_requests: checked } },
                  });
                }}
                aria-label="Allow repository resource requests"
              />
            </div>
            <div className="space-y-2">
              <SettingLabel
                htmlFor="preview-max-cpu-millis"
                label="Preview CPU limit"
                tooltip="Largest CPU request a repository preview config can make, shown in CPU cores."
              />
              <div className="flex items-center gap-2">
                <Input
                  id="preview-max-cpu-millis"
                  type="number"
                  inputMode="decimal"
                  min={MIN_PREVIEW_MAX_CPU_MILLIS / CPU_MILLIS_PER_CORE}
                  max={MAX_PREVIEW_MAX_CPU_MILLIS / CPU_MILLIS_PER_CORE}
                  step={CPU_MILLIS_STEP / CPU_MILLIS_PER_CORE}
                  value={previewMaxCPUField.value}
                  onChange={previewMaxCPUField.onChange}
                  onBlur={previewMaxCPUField.onBlur}
                />
                <span className="text-xs text-muted-foreground">cores</span>
              </div>
              <MaxHint>{`Max ${formatCPUCores(MAX_PREVIEW_MAX_CPU_MILLIS)} cores`}</MaxHint>
            </div>
            <div className="space-y-2">
              <SettingLabel
                htmlFor="preview-max-memory-mib"
                label="Preview memory limit"
                tooltip="Largest memory request a repository preview config can make."
              />
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
              <MaxHint>{`Max ${MAX_PREVIEW_MAX_MEMORY_MIB} MiB`}</MaxHint>
            </div>
            <div className="space-y-2">
              <SettingLabel
                htmlFor="preview-max-ephemeral-disk-mib"
                label="Preview disk limit"
                tooltip="Largest temporary disk request a repository preview config can make."
              />
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
              <MaxHint>{`Max ${MAX_PREVIEW_MAX_EPHEMERAL_DISK_MIB} MiB`}</MaxHint>
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
      <SectionHeader title="Sessions" status={autosave.status} />
      <Card>
        <CardContent className="space-y-4">
          <div className="max-w-[560px] space-y-2">
            <SettingLabel
              htmlFor="max-session-minutes"
              label="Maximum session length"
              tooltip="Stops a coding-agent turn when it runs longer than this organization limit."
            />
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
            <MaxHint>{`Max ${MAX_SESSION_DURATION_MINUTES} minutes`}</MaxHint>
          </div>
          <div className="flex flex-col gap-3 rounded-md border border-border p-3 sm:flex-row sm:items-center sm:justify-between">
            <div className="space-y-1">
              <SettingLabel
                htmlFor="sandbox-tab-tools"
                label="Agent tab tools"
                tooltip="Lets agent tabs in the same session coordinate with each other through the 143 tools CLI."
              />
            </div>
            <Switch
              id="sandbox-tab-tools"
              checked={tabToolsEnabled}
              onCheckedChange={(checked) => {
                autosave.save({ settings: { coding_agent_tab_tools_enabled: checked } });
              }}
              aria-label="Agent tab tools"
            />
          </div>
        </CardContent>
      </Card>
    </section>
  );
}

export default function RuntimeSettingsPage() {
  usePageTitle("Runtime settings");

  return (
    <PageContainer size="default">
      <TooltipProvider delayDuration={150}>
        <div className="space-y-8">
          <PageHeader
            title="Runtime"
            description="Configure sandbox networking, capacity, and lifecycle defaults."
          />
          <SandboxNetworkSection />
          <CapacityLimitsSection />
          <SessionRuntimeSection />
          <LifecycleDefaultsSection />
          <ResourceDefaultsSection />
        </div>
      </TooltipProvider>
    </PageContainer>
  );
}
