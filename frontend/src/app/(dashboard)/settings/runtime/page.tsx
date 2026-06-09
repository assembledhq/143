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
import { Switch } from "@/components/ui/switch";
import { useAutosaveNumericField } from "@/hooks/useAutosaveNumericField";
import { useOrgSettingsAutosave } from "@/hooks/use-org-settings-autosave";
import { usePageTitle } from "@/hooks/use-page-title";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import {
  DEFAULT_PREVIEW_MAX_PREVIEWS_PER_USER,
  MAX_CONCURRENT_RUNS,
  MAX_PREVIEW_MAX_PREVIEWS_PER_USER,
  MAX_SESSION_DURATION_MINUTES,
  MIN_CONCURRENT_RUNS,
  MIN_PREVIEW_MAX_PREVIEWS_PER_USER,
  MIN_SESSION_DURATION_MINUTES,
  clampNumber,
} from "@/lib/settings-constants";
import type { Organization, OrgSettings, SingleResponse } from "@/lib/types";

const DEFAULT_EXECUTION_SETTINGS = {
  max_concurrent_runs: 5,
  max_session_duration_seconds: 25 * 60,
};

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
              {!available && (
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
              <Label htmlFor="agent-tab-tools">Sandbox tab tools</Label>
              <p className="text-xs text-muted-foreground">
                Allows agent tabs in the same session to coordinate through the 143 tools CLI.
              </p>
            </div>
            <Switch
              id="agent-tab-tools"
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
  const { data: settingsResponse } = useOrgSettingsQuery();
  const { data: networkStatusResponse } = useQuery({
    queryKey: queryKeys.settings.network,
    queryFn: () => api.settings.getNetworkStatus(),
  });

  const settings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;
  const networkStatus = networkStatusResponse?.data;
  const staticEgressAvailable = networkStatus?.static_egress_available ?? false;
  const staticEgressEnabled =
    settings.sandbox_network?.static_egress_enabled ?? networkStatus?.static_egress_enabled ?? false;

  const rows = [
    {
      label: "Static egress",
      value: staticEgressAvailable ? "Available" : "Unavailable",
      detail: staticEgressEnabled ? "Enabled for new sandbox starts" : "Disabled for new sandbox starts",
      tone: staticEgressAvailable ? "default" : "secondary",
    },
    {
      label: "Public IP",
      value: networkStatus?.static_egress_public_ip ?? "Not configured",
      detail: "Allowlist this address with external services that require stable source IPs.",
      tone: "outline",
    },
  ] as const;

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
        <RuntimeDiagnosticsSection />
      </div>
    </PageContainer>
  );
}
