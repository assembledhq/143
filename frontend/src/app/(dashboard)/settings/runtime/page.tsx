"use client";

import {
  useEffect,
  useRef,
  useState,
  type ChangeEvent,
  type ReactNode,
} from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, CircleHelp, Minus, Plus } from "lucide-react";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { CopyButton } from "@/components/copy-button";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { SettingsLastActivity } from "@/components/settings/settings-last-activity";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useAutosaveNumericField } from "@/hooks/useAutosaveNumericField";
import { useOrgSettingsAutosave } from "@/hooks/use-org-settings-autosave";
import { usePageTitle } from "@/hooks/use-page-title";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { cn } from "@/lib/utils";
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
import type {
  Organization,
  OrgSettings,
  SandboxResourceTier,
  SingleResponse,
} from "@/lib/types";

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
  return (
    Math.round((cores * CPU_MILLIS_PER_CORE) / CPU_MILLIS_STEP) *
    CPU_MILLIS_STEP
  );
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
  action,
}: {
  title: string;
  status?: ReturnType<typeof useOrgSettingsAutosave>["status"];
  action?: ReactNode;
}) {
  return (
    <div className="flex items-center justify-between">
      <h2 className="text-xs font-medium text-foreground">{title}</h2>
      <div className="flex items-center gap-2">
        {status ? <AutosaveIndicator status={status} /> : null}
        {action}
      </div>
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
  tooltip?: string;
}) {
  const ariaLabel = `About ${label.charAt(0).toLowerCase()}${label.slice(1)}`;

  return (
    <div className="flex items-center gap-1.5">
      <Label htmlFor={htmlFor}>{label}</Label>
      {tooltip ? (
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
      ) : null}
    </div>
  );
}

function HelperText({
  children,
  className,
}: {
  children: string;
  className?: string;
}) {
  return (
    <p className={cn("text-xs leading-5 text-muted-foreground", className)}>
      {children}
    </p>
  );
}

function SettingRow({
  id,
  label,
  description,
  helper,
  tooltip,
  children,
  className,
}: {
  id: string;
  label: string;
  description: string;
  helper?: string;
  tooltip?: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "grid gap-3 border-b border-border/70 py-4 last:border-b-0 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-start",
        className,
      )}
    >
      <div className="min-w-0 space-y-1">
        <SettingLabel htmlFor={id} label={label} tooltip={tooltip} />
        <HelperText>{description}</HelperText>
        {helper ? (
          <HelperText className="text-muted-foreground/80">{helper}</HelperText>
        ) : null}
      </div>
      <div className="min-w-0 sm:w-[15rem]">{children}</div>
    </div>
  );
}

function UnitInput({
  unit,
  children,
  className,
}: {
  unit?: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex min-w-0 items-center rounded-md border border-input bg-background focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/20",
        className,
      )}
    >
      {children}
      {unit ? (
        <span className="shrink-0 border-l border-border px-2.5 text-xs text-muted-foreground">
          {unit}
        </span>
      ) : null}
    </div>
  );
}

function BoundedNumberInput({
  id,
  label,
  value,
  onChange,
  onBlur,
  min,
  max,
  inputMode = "numeric",
  step,
  unit,
  onStep,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (event: ChangeEvent<HTMLInputElement>) => void;
  onBlur: () => void;
  min: number;
  max: number;
  inputMode?: "numeric" | "decimal";
  step?: number;
  unit?: string;
  onStep?: (direction: -1 | 1) => void;
}) {
  if (onStep) {
    return (
      <div
        role="group"
        aria-label={`${label} controls`}
        className="flex h-9 min-w-0 overflow-hidden rounded-md border border-input bg-background shadow-xs focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/20"
      >
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={`Decrease ${label}`}
          onClick={() => onStep(-1)}
          className="h-full w-9 rounded-none border-r border-border text-muted-foreground shadow-none hover:text-foreground focus-visible:z-10"
        >
          <Minus className="h-3.5 w-3.5" />
        </Button>
        <UnitInput
          unit={unit}
          className="min-w-0 flex-1 rounded-none border-0 bg-transparent focus-within:ring-0"
        >
          <Input
            id={id}
            type="number"
            inputMode={inputMode}
            min={min}
            max={max}
            step={step}
            value={value}
            onChange={onChange}
            onBlur={onBlur}
            aria-label={label}
            className="h-full rounded-none border-0 focus-visible:ring-0"
          />
        </UnitInput>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={`Increase ${label}`}
          onClick={() => onStep(1)}
          className="h-full w-9 rounded-none border-l border-border text-muted-foreground shadow-none hover:text-foreground focus-visible:z-10"
        >
          <Plus className="h-3.5 w-3.5" />
        </Button>
      </div>
    );
  }

  return (
    <div className="flex min-w-0 items-center">
      <UnitInput unit={unit}>
        <Input
          id={id}
          type="number"
          inputMode={inputMode}
          min={min}
          max={max}
          step={step}
          value={value}
          onChange={onChange}
          onBlur={onBlur}
          aria-label={label}
          className="border-0 focus-visible:ring-0"
        />
      </UnitInput>
    </div>
  );
}

function formatMinutes(minutes: number): string {
  if (minutes >= 60 && minutes % 60 === 0) {
    const hours = minutes / 60;
    return `${hours} ${hours === 1 ? "hour" : "hours"}`;
  }
  return `${minutes} ${minutes === 1 ? "minute" : "minutes"}`;
}

function formatGiB(mib: number): string {
  const gib = mib / 1024;
  return Number.isInteger(gib) ? `${gib} GiB` : `${gib.toFixed(1)} GiB`;
}

function getSettings(response?: SingleResponse<Organization>): OrgSettings {
  return (response?.data?.settings ?? {}) as OrgSettings;
}

function RuntimeSummary() {
  const { data: settingsResponse } = useOrgSettingsQuery();
  const settings = getSettings(settingsResponse);
  const lifecycle = settings.sandbox_lifecycle ?? {};

  const maxConcurrentRuns =
    settings.max_concurrent_runs ??
    DEFAULT_EXECUTION_SETTINGS.max_concurrent_runs;
  const maxPreviewsPerUser =
    settings.preview_max_previews_per_user ??
    DEFAULT_PREVIEW_MAX_PREVIEWS_PER_USER;
  const sessionMinutes = Math.round(
    (settings.max_session_duration_seconds ??
      DEFAULT_EXECUTION_SETTINGS.max_session_duration_seconds) / 60,
  );
  const idlePreviewTTLMinutes =
    lifecycle.idle_preview_ttl_minutes ?? DEFAULT_IDLE_PREVIEW_TTL_MINUTES;

  const items = [
    {
      label: "Agent runs",
      value: `${maxConcurrentRuns} concurrent`,
      description: "Org-wide cap",
    },
    {
      label: "Active previews",
      value: `${maxPreviewsPerUser} per user`,
      description: "Per-user cap",
    },
    {
      label: "Session max",
      value: formatMinutes(sessionMinutes),
      description: "Agent turn limit",
    },
    {
      label: "Preview idle TTL",
      value: formatMinutes(idlePreviewTTLMinutes),
      description: "Auto-stop preview",
    },
  ];

  return (
    <div className="grid overflow-hidden rounded-md border border-border bg-card sm:grid-cols-2 lg:grid-cols-4">
      {items.map((item) => (
        <div
          key={item.label}
          className="border-b border-border/70 p-3 last:border-b-0 sm:border-r sm:last:border-r-0 sm:[&:nth-last-child(-n+2)]:border-b-0 lg:border-b-0"
        >
          <p className="text-xs font-medium text-muted-foreground">
            {item.label}
          </p>
          <p className="mt-1 text-sm font-medium text-foreground">
            {item.value}
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            {item.description}
          </p>
        </div>
      ))}
    </div>
  );
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
    autosave.save({
      settings: { sandbox_resources: { preview_max_cpu_millis: millis } },
    });
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
    <Select
      value={value}
      onValueChange={(nextValue) => onChange(nextValue as SandboxResourceTier)}
    >
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

  const settings = getSettings(settingsResponse);
  const sandboxNetwork = settings.sandbox_network ?? {};
  const networkStatus = networkStatusResponse?.data;
  const available = networkStatus?.static_egress_available ?? false;
  const publicIP = networkStatus?.static_egress_public_ip;
  const enabled =
    sandboxNetwork.static_egress_enabled ??
    networkStatus?.static_egress_enabled ??
    false;

  return (
    <section className="space-y-3">
      <SectionHeader title="Sandbox network" status={autosave.status} />
      <Card>
        <CardContent>
          <SettingRow
            id="static-egress-enabled"
            label="Static egress IP"
            description="Use one stable public IP for new and resumed sandboxes."
            helper={
              enabled && networkStatusResponse && !available
                ? "Static egress is not currently available for new sandbox starts."
                : undefined
            }
            tooltip="Use this when external services need to allowlist sandbox traffic."
          >
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
              className="sm:ml-auto"
            />
          </SettingRow>
          <div
            data-testid="public-ip-row"
            className="flex flex-col items-start gap-2 py-4"
          >
            <div className="min-w-0 space-y-1">
              <p className="text-xs font-medium text-foreground">Public IP</p>
              <p className="text-xs text-muted-foreground">
                Add this address to external allowlists when static egress is
                enabled.
              </p>
            </div>
            <div
              data-testid="public-ip-value"
              className="inline-flex max-w-full min-w-0 items-center gap-2 self-start rounded-md border border-border bg-muted/30 px-3 py-2"
            >
              <code className="min-w-0 flex-1 truncate font-mono text-xs text-foreground">
                {publicIP ?? "Not configured"}
              </code>
              <CopyButton
                value={publicIP}
                label="Copy static egress public IP"
              />
            </div>
          </div>
        </CardContent>
      </Card>
    </section>
  );
}

function CapacityLimitsSection() {
  const { data: settingsResponse } = useOrgSettingsQuery();
  const autosave = useOrgSettingsAutosave();

  const settings = getSettings(settingsResponse);
  const maxConcurrentRuns =
    settings.max_concurrent_runs ??
    DEFAULT_EXECUTION_SETTINGS.max_concurrent_runs;
  const maxPreviewsPerUser =
    settings.preview_max_previews_per_user ??
    DEFAULT_PREVIEW_MAX_PREVIEWS_PER_USER;

  const concurrentRunsField = useAutosaveNumericField({
    serverValue: maxConcurrentRuns,
    autosave,
    toPatch: (value) => ({ settings: { max_concurrent_runs: value } }),
    clamp: (value) =>
      clampNumber(value, MIN_CONCURRENT_RUNS, MAX_CONCURRENT_RUNS),
  });
  const maxPreviewsPerUserField = useAutosaveNumericField({
    serverValue: maxPreviewsPerUser,
    autosave,
    toPatch: (value) => ({
      settings: { preview_max_previews_per_user: value },
    }),
    clamp: (value) =>
      clampNumber(
        value,
        MIN_PREVIEW_MAX_PREVIEWS_PER_USER,
        MAX_PREVIEW_MAX_PREVIEWS_PER_USER,
      ),
  });

  return (
    <section className="space-y-3">
      <SectionHeader title="Capacity" status={autosave.status} />
      <Card>
        <CardContent>
          <SettingRow
            id="max-concurrent-runs"
            label="Concurrent agent runs"
            description="Limit simultaneous coding-agent turns across the organization."
            helper={`Range ${MIN_CONCURRENT_RUNS}-${MAX_CONCURRENT_RUNS}`}
            tooltip="This protects shared runtime capacity. Users can still queue additional work."
          >
            <BoundedNumberInput
              id="max-concurrent-runs"
              label="Concurrent agent runs"
              min={MIN_CONCURRENT_RUNS}
              max={MAX_CONCURRENT_RUNS}
              value={concurrentRunsField.value}
              onChange={concurrentRunsField.onChange}
              onBlur={concurrentRunsField.onBlur}
              onStep={(direction) => {
                const current = Number.parseInt(concurrentRunsField.value, 10);
                const next = clampNumber(
                  (Number.isNaN(current) ? maxConcurrentRuns : current) +
                    direction,
                  MIN_CONCURRENT_RUNS,
                  MAX_CONCURRENT_RUNS,
                );
                concurrentRunsField.setValueAndSave(next);
              }}
            />
          </SettingRow>
          <SettingRow
            id="preview-max-previews-per-user"
            label="Active previews per user"
            description="Limit how many preview environments one user can keep running."
            helper={`Range ${MIN_PREVIEW_MAX_PREVIEWS_PER_USER}-${MAX_PREVIEW_MAX_PREVIEWS_PER_USER}`}
            tooltip="This is a per-user guardrail, not a total organization-wide preview cap."
          >
            <BoundedNumberInput
              id="preview-max-previews-per-user"
              label="Active previews per user"
              min={MIN_PREVIEW_MAX_PREVIEWS_PER_USER}
              max={MAX_PREVIEW_MAX_PREVIEWS_PER_USER}
              value={maxPreviewsPerUserField.value}
              onChange={maxPreviewsPerUserField.onChange}
              onBlur={maxPreviewsPerUserField.onBlur}
              onStep={(direction) => {
                const current = Number.parseInt(
                  maxPreviewsPerUserField.value,
                  10,
                );
                const next = clampNumber(
                  (Number.isNaN(current) ? maxPreviewsPerUser : current) +
                    direction,
                  MIN_PREVIEW_MAX_PREVIEWS_PER_USER,
                  MAX_PREVIEW_MAX_PREVIEWS_PER_USER,
                );
                maxPreviewsPerUserField.setValueAndSave(next);
              }}
            />
          </SettingRow>
        </CardContent>
      </Card>
    </section>
  );
}

function SessionsAndCleanupSection() {
  const { data: settingsResponse } = useOrgSettingsQuery();
  const autosave = useOrgSettingsAutosave();
  const [open, setOpen] = useState(false);

  const settings = getSettings(settingsResponse);
  const lifecycle = settings.sandbox_lifecycle ?? {};
  const sessionMinutes = Math.round(
    (settings.max_session_duration_seconds ??
      DEFAULT_EXECUTION_SETTINGS.max_session_duration_seconds) / 60,
  );
  const tabToolsEnabled = settings.coding_agent_tab_tools_enabled ?? true;
  const completedRetentionMinutes =
    lifecycle.completed_session_retention_minutes ??
    DEFAULT_COMPLETED_SESSION_RETENTION_MINUTES;
  const idlePreviewTTLMinutes =
    lifecycle.idle_preview_ttl_minutes ?? DEFAULT_IDLE_PREVIEW_TTL_MINUTES;
  const previewHoldsSandbox = lifecycle.preview_holds_sandbox ?? true;

  const maxSessionMinutesField = useAutosaveNumericField({
    serverValue: sessionMinutes,
    autosave,
    toPatch: (minutes) => ({
      settings: { max_session_duration_seconds: minutes * 60 },
    }),
    clamp: (value) =>
      clampNumber(
        value,
        MIN_SESSION_DURATION_MINUTES,
        MAX_SESSION_DURATION_MINUTES,
      ),
  });
  const completedRetentionField = useAutosaveNumericField({
    serverValue: completedRetentionMinutes,
    autosave,
    toPatch: (value) => ({
      settings: {
        sandbox_lifecycle: { completed_session_retention_minutes: value },
      },
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
      clampNumber(
        value,
        MIN_IDLE_PREVIEW_TTL_MINUTES,
        MAX_IDLE_PREVIEW_TTL_MINUTES,
      ),
  });

  const summary = settingsResponse
    ? `Session max ${formatMinutes(sessionMinutes)} · preview idle ${formatMinutes(idlePreviewTTLMinutes)}`
    : null;

  return (
    <section className="space-y-3">
      <Collapsible open={open} onOpenChange={setOpen}>
        <Card>
          <CardContent>
            <CollapsibleTrigger asChild>
              <Button
                type="button"
                variant="ghost"
                aria-label={
                  open
                    ? "Hide session lifecycle controls"
                    : "Show session lifecycle controls"
                }
                className="flex h-auto w-full items-center justify-between gap-4 rounded-none px-0 py-4 text-left hover:bg-transparent"
              >
                <span className="block min-w-0 space-y-1">
                  <span className="flex items-center gap-2 text-xs font-medium text-foreground">
                    Sessions and cleanup
                    <AutosaveIndicator status={autosave.status} />
                  </span>
                  {summary && (
                    <span className="block text-sm font-medium text-foreground">
                      {summary}
                    </span>
                  )}
                  <span className="block text-xs leading-5 whitespace-normal text-muted-foreground">
                    Lifecycle, retention, preview idle behavior, and advanced agent tab coordination.
                  </span>
                </span>
                <span className="flex shrink-0 items-center gap-2 text-xs text-muted-foreground">
                  {open ? "Hide" : "Show"}
                  <ChevronDown
                    className={cn(
                      "h-4 w-4 transition-transform",
                      open && "rotate-180",
                    )}
                    aria-hidden="true"
                  />
                </span>
              </Button>
            </CollapsibleTrigger>
            <CollapsibleContent className="border-t border-border/70">
              <SettingRow
                id="max-session-minutes"
                label="Maximum session length"
                description="Stop an agent turn when it exceeds this organization limit."
                helper={`Range ${MIN_SESSION_DURATION_MINUTES}-${MAX_SESSION_DURATION_MINUTES} minutes`}
                tooltip="Longer limits help large changes finish, but they also hold sandbox capacity longer."
              >
                <BoundedNumberInput
                  id="max-session-minutes"
                  label="Maximum session length"
                  min={MIN_SESSION_DURATION_MINUTES}
                  max={MAX_SESSION_DURATION_MINUTES}
                  value={maxSessionMinutesField.value}
                  onChange={maxSessionMinutesField.onChange}
                  onBlur={maxSessionMinutesField.onBlur}
                  unit="min"
                />
              </SettingRow>
              <SettingRow
                id="completed-session-retention"
                label="Keep completed sessions for"
                description="Keep completed sandboxes available briefly for inspection and preview reuse."
                helper={`Range ${MIN_COMPLETED_SESSION_RETENTION_MINUTES}-${MAX_COMPLETED_SESSION_RETENTION_MINUTES} minutes`}
                tooltip="Use 0 minutes when completed sandboxes should be cleaned up immediately."
              >
                <BoundedNumberInput
                  id="completed-session-retention"
                  label="Keep completed sessions for"
                  min={MIN_COMPLETED_SESSION_RETENTION_MINUTES}
                  max={MAX_COMPLETED_SESSION_RETENTION_MINUTES}
                  value={completedRetentionField.value}
                  onChange={completedRetentionField.onChange}
                  onBlur={completedRetentionField.onBlur}
                  unit="min"
                />
              </SettingRow>
              <SettingRow
                id="idle-preview-ttl"
                label="Idle preview timeout"
                description="Stop a preview sandbox after it sits idle for this long."
                helper={`Range ${MIN_IDLE_PREVIEW_TTL_MINUTES}-${MAX_IDLE_PREVIEW_TTL_MINUTES} minutes`}
                tooltip="Active previews are still stopped when they hit this idle window."
              >
                <BoundedNumberInput
                  id="idle-preview-ttl"
                  label="Idle preview timeout"
                  min={MIN_IDLE_PREVIEW_TTL_MINUTES}
                  max={MAX_IDLE_PREVIEW_TTL_MINUTES}
                  value={idlePreviewTTLField.value}
                  onChange={idlePreviewTTLField.onChange}
                  onBlur={idlePreviewTTLField.onBlur}
                  unit="min"
                />
              </SettingRow>
              <SettingRow
                id="preview-holds-sandbox"
                label="Keep sandbox while preview is active"
                description="Preserve the backing sandbox while a preview is still running."
                tooltip="Disable this only if preview cost is more important than fast preview reuse."
              >
                <Switch
                  id="preview-holds-sandbox"
                  checked={previewHoldsSandbox}
                  onCheckedChange={(checked) => {
                    autosave.save({
                      settings: {
                        sandbox_lifecycle: { preview_holds_sandbox: checked },
                      },
                    });
                  }}
                  aria-label="Keep sandbox while preview is active"
                  className="sm:ml-auto"
                />
              </SettingRow>
              <SettingRow
                id="sandbox-tab-tools"
                label="Agent tab tools"
                description="Allow sibling agent tabs in the same session to coordinate through 143 tools."
                tooltip="This only exposes scoped tab coordination for the current session and repository."
              >
                <Switch
                  id="sandbox-tab-tools"
                  checked={tabToolsEnabled}
                  onCheckedChange={(checked) => {
                    autosave.save({
                      settings: { coding_agent_tab_tools_enabled: checked },
                    });
                  }}
                  aria-label="Agent tab tools"
                  className="sm:ml-auto"
                />
              </SettingRow>
            </CollapsibleContent>
          </CardContent>
        </Card>
      </Collapsible>
    </section>
  );
}

function ResourceDefaultsSection() {
  const { data: settingsResponse } = useOrgSettingsQuery();
  const autosave = useOrgSettingsAutosave();
  const [advancedOpen, setAdvancedOpen] = useState(false);

  const settings = getSettings(settingsResponse);
  const resources = settings.sandbox_resources ?? {};
  const agentDefaultTier = resources.agent_default_tier ?? "standard";
  const previewDefaultTier = resources.preview_default_tier ?? "standard";
  const allowRepoResourceRequests =
    resources.allow_repo_resource_requests ?? true;
  const previewMaxTier = resources.preview_max_tier ?? "large";
  const previewMaxCPUMillis =
    resources.preview_max_cpu_millis ?? DEFAULT_PREVIEW_MAX_CPU_MILLIS;
  const previewMaxMemoryMiB =
    resources.preview_max_memory_mib ?? DEFAULT_PREVIEW_MAX_MEMORY_MIB;
  const previewMaxEphemeralDiskMiB =
    resources.preview_max_ephemeral_disk_mib ??
    DEFAULT_PREVIEW_MAX_EPHEMERAL_DISK_MIB;

  const previewMaxCPUField = usePreviewCPUField({
    serverMillis: previewMaxCPUMillis,
    autosave,
  });
  const previewMaxMemoryField = useAutosaveNumericField({
    serverValue: previewMaxMemoryMiB,
    autosave,
    toPatch: (value) => ({
      settings: { sandbox_resources: { preview_max_memory_mib: value } },
    }),
    clamp: (value) =>
      clampNumber(
        value,
        MIN_PREVIEW_MAX_MEMORY_MIB,
        MAX_PREVIEW_MAX_MEMORY_MIB,
      ),
  });
  const previewMaxDiskField = useAutosaveNumericField({
    serverValue: previewMaxEphemeralDiskMiB,
    autosave,
    toPatch: (value) => ({
      settings: {
        sandbox_resources: { preview_max_ephemeral_disk_mib: value },
      },
    }),
    clamp: (value) =>
      clampNumber(
        value,
        MIN_PREVIEW_MAX_EPHEMERAL_DISK_MIB,
        MAX_PREVIEW_MAX_EPHEMERAL_DISK_MIB,
      ),
  });
  const advancedSummary = `${previewMaxTier.charAt(0).toUpperCase()}${previewMaxTier.slice(1)} max · ${formatCPUCores(previewMaxCPUMillis)} cores · ${formatGiB(previewMaxMemoryMiB)}`;

  return (
    <>
      <section data-testid="sandbox-defaults-section" className="space-y-3">
        <SectionHeader title="Sandbox defaults" status={autosave.status} />
        <Card>
          <CardContent>
            <SettingRow
              id="agent-default-tier"
              label="Agent sandbox size"
              description="Choose the default sandbox tier for new coding-agent sessions."
              tooltip="Repositories can still influence preview resources separately."
            >
              <ResourceTierSelect
                id="agent-default-tier"
                label="Agent sandbox size"
                value={agentDefaultTier}
                onChange={(value) => {
                  autosave.save({
                    settings: {
                      sandbox_resources: { agent_default_tier: value },
                    },
                  });
                }}
              />
            </SettingRow>
            <SettingRow
              id="preview-default-tier"
              label="Preview sandbox size"
              description="Choose the default preview tier when repository config does not request one."
              tooltip="This default applies before repository-specific preview requests are considered."
            >
              <ResourceTierSelect
                id="preview-default-tier"
                label="Preview sandbox size"
                value={previewDefaultTier}
                onChange={(value) => {
                  autosave.save({
                    settings: {
                      sandbox_resources: { preview_default_tier: value },
                    },
                  });
                }}
              />
            </SettingRow>
            <SettingRow
              id="allow-repo-resource-requests"
              label="Allow repository resource requests"
              description="Let repository preview config request CPU, memory, and disk up to org limits."
              tooltip="Disable this to force previews to use the organization preview default tier."
            >
              <Switch
                id="allow-repo-resource-requests"
                checked={allowRepoResourceRequests}
                onCheckedChange={(checked) => {
                  autosave.save({
                    settings: {
                      sandbox_resources: {
                        allow_repo_resource_requests: checked,
                      },
                    },
                  });
                }}
                aria-label="Allow repository resource requests"
                className="sm:ml-auto"
              />
            </SettingRow>
          </CardContent>
        </Card>
      </section>
      <section
        data-testid="advanced-resource-limits-section"
        className="space-y-3"
      >
        <Collapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
          <Card>
            <CardContent>
              <CollapsibleTrigger asChild>
                <Button
                  type="button"
                  variant="ghost"
                  aria-label={
                    advancedOpen
                      ? "Hide advanced resource limits"
                      : "Show advanced resource limits"
                  }
                  className="flex h-auto w-full items-center justify-between gap-4 rounded-none px-0 py-4 text-left hover:bg-transparent"
                >
                  <span className="block min-w-0 space-y-1">
                    <span className="block text-xs font-medium text-foreground">
                      Advanced resource limits
                    </span>
                    <span className="block text-sm font-medium text-foreground">
                      {advancedSummary}
                    </span>
                    <span className="block text-xs leading-5 whitespace-normal text-muted-foreground">
                      Exact caps for repository-requested previews.
                    </span>
                  </span>
                  <span className="flex shrink-0 items-center gap-2 text-xs font-medium text-muted-foreground">
                    <AutosaveIndicator status={autosave.status} />
                    {advancedOpen ? "Hide" : "Show"}
                    <ChevronDown
                      className={cn(
                        "h-3.5 w-3.5 transition-transform",
                        advancedOpen && "rotate-180",
                      )}
                    />
                  </span>
                </Button>
              </CollapsibleTrigger>
              <CollapsibleContent>
                <SettingRow
                  id="preview-max-tier"
                  label="Largest preview size"
                  description="Set the largest sandbox tier a repository preview config can request."
                  tooltip="This cap applies only when repository resource requests are allowed."
                >
                  <ResourceTierSelect
                    id="preview-max-tier"
                    label="Largest preview size"
                    value={previewMaxTier}
                    onChange={(value) => {
                      autosave.save({
                        settings: {
                          sandbox_resources: { preview_max_tier: value },
                        },
                      });
                    }}
                  />
                </SettingRow>
                <SettingRow
                  id="preview-max-cpu-millis"
                  label="Preview CPU limit"
                  description="Set the largest CPU request a repository preview config can make."
                  helper={`Range ${formatCPUCores(MIN_PREVIEW_MAX_CPU_MILLIS)}-${formatCPUCores(MAX_PREVIEW_MAX_CPU_MILLIS)} cores`}
                  tooltip="CPU is shown in cores but saved as millicores."
                >
                  <BoundedNumberInput
                    id="preview-max-cpu-millis"
                    label="Preview CPU limit"
                    inputMode="decimal"
                    min={MIN_PREVIEW_MAX_CPU_MILLIS / CPU_MILLIS_PER_CORE}
                    max={MAX_PREVIEW_MAX_CPU_MILLIS / CPU_MILLIS_PER_CORE}
                    step={CPU_MILLIS_STEP / CPU_MILLIS_PER_CORE}
                    value={previewMaxCPUField.value}
                    onChange={previewMaxCPUField.onChange}
                    onBlur={previewMaxCPUField.onBlur}
                    unit="cores"
                  />
                </SettingRow>
                <SettingRow
                  id="preview-max-memory-mib"
                  label="Preview memory limit"
                  description="Set the largest memory request a repository preview config can make."
                  helper={`Range ${MIN_PREVIEW_MAX_MEMORY_MIB}-${MAX_PREVIEW_MAX_MEMORY_MIB} MiB`}
                  tooltip="Use this to bound memory-heavy preview services without changing default tiers."
                >
                  <BoundedNumberInput
                    id="preview-max-memory-mib"
                    label="Preview memory limit"
                    min={MIN_PREVIEW_MAX_MEMORY_MIB}
                    max={MAX_PREVIEW_MAX_MEMORY_MIB}
                    value={previewMaxMemoryField.value}
                    onChange={previewMaxMemoryField.onChange}
                    onBlur={previewMaxMemoryField.onBlur}
                    unit="MiB"
                  />
                </SettingRow>
                <SettingRow
                  id="preview-max-ephemeral-disk-mib"
                  label="Preview disk limit"
                  description="Set the largest temporary disk request a repository preview config can make."
                  helper={`Range ${MIN_PREVIEW_MAX_EPHEMERAL_DISK_MIB}-${MAX_PREVIEW_MAX_EPHEMERAL_DISK_MIB} MiB`}
                  tooltip="This limits ephemeral workspace disk, not persistent repository storage."
                >
                  <BoundedNumberInput
                    id="preview-max-ephemeral-disk-mib"
                    label="Preview disk limit"
                    min={MIN_PREVIEW_MAX_EPHEMERAL_DISK_MIB}
                    max={MAX_PREVIEW_MAX_EPHEMERAL_DISK_MIB}
                    value={previewMaxDiskField.value}
                    onChange={previewMaxDiskField.onChange}
                    onBlur={previewMaxDiskField.onBlur}
                    unit="MiB"
                  />
                </SettingRow>
              </CollapsibleContent>
            </CardContent>
          </Card>
        </Collapsible>
      </section>
    </>
  );
}

export default function RuntimeSettingsPage() {
  usePageTitle("Sandboxes");

  return (
    <PageContainer size="default">
      <TooltipProvider delayDuration={150}>
        <div className="space-y-8">
          <PageHeader
            title="Sandboxes"
            description="Configure sandbox networking, capacity, and lifecycle defaults."
          />
          <RuntimeSummary />
          <SandboxNetworkSection />
          <CapacityLimitsSection />
          <SessionsAndCleanupSection />
          <ResourceDefaultsSection />
          <SettingsLastActivity
            scopes={{ resource_type: "settings" }}
            title="Sandbox settings activity"
          />
        </div>
      </TooltipProvider>
    </PageContainer>
  );
}
