"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { AuditLogTrigger } from "@/components/audit/audit-log-trigger";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { CopyButton } from "@/components/copy-button";
import { DebouncedInput } from "@/components/debounced-fields";
import { useAuth } from "@/hooks/use-auth";
import { useAutosave } from "@/hooks/useAutosave";
import { useAutosaveNumericField } from "@/hooks/useAutosaveNumericField";
import {
  applyOrgSettingsPatch,
  coalesceSettingsPatch,
  type SettingsPatch,
} from "@/lib/settings-autosave";
import type { MembershipsResponse, Organization, OrgSettings, SingleResponse } from "@/lib/types";

const PR_AUTHORSHIP_OPTIONS = [
  { value: "user_preferred", label: "User preferred", description: "Use the user's GitHub token when available, fall back to the 143 app" },
  { value: "app_only", label: "App only", description: "Always create PRs as the 143 GitHub App" },
  { value: "user_required", label: "User required", description: "Require users to connect GitHub before creating PRs" },
] as const;

const DEFAULT_PREVIEW_MAX_PREVIEWS_PER_USER = 4;
const MIN_PREVIEW_MAX_PREVIEWS_PER_USER = 1;
const MAX_PREVIEW_MAX_PREVIEWS_PER_USER = 20;

const settingsTimestampFormatter = new Intl.DateTimeFormat("en-US", {
  dateStyle: "long",
  timeStyle: "short",
  timeZone: "UTC",
});

function formatUpdatedAt(updatedAt: string | undefined): string | undefined {
  if (!updatedAt) return undefined;
  const date = new Date(updatedAt);
  if (Number.isNaN(date.getTime())) return undefined;
  return `${settingsTimestampFormatter.format(date)} UTC`;
}

function useOrgSettingsAutosave() {
  const queryClient = useQueryClient();
  return useAutosave<SettingsPatch>({
    queryKey: queryKeys.settings.all,
    mutationFn: async (payload) => {
      const response = await api.settings.update(payload);
      queryClient.setQueryData(queryKeys.settings.all, response);
      if (payload.name !== undefined) {
        queryClient.setQueryData<SingleResponse<MembershipsResponse> | undefined>(
          queryKeys.auth.memberships,
          (previous) => {
            if (!previous?.data) return previous;
            return {
              ...previous,
              data: {
                ...previous.data,
                memberships: previous.data.memberships.map((membership) =>
                  membership.org_id === response.data.id
                    ? { ...membership, org_name: response.data.name }
                    : membership,
                ),
              },
            };
          },
        );
      }
      void queryClient.invalidateQueries({ queryKey: ["audit-logs", "latest"] });
      return response;
    },
    applyOptimistic: applyOrgSettingsPatch,
    coalesce: coalesceSettingsPatch,
    invalidateOnSettled: false,
  });
}

function PRAuthorshipSettings() {
  const { data: settingsResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });

  const settings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;
  const currentAuthorship = settings.pr_authorship ?? "user_preferred";
  const currentDraftDefault = settings.pr_draft_default ?? false;
  const currentAutoArchive = settings.auto_archive_on_pr_close ?? false;
  const requireBuilderReview = settings.builder_permissions?.require_review_before_pr ?? true;

  const { save, status } = useOrgSettingsAutosave();

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
          <div className="flex items-start justify-between gap-4 border-t border-border pt-4">
            <div className="space-y-1">
              <Label htmlFor="builder-review-before-pr">Require builder review before PR</Label>
              <p className="text-xs text-muted-foreground">
                Builders must run Review successfully before creating a pull request.
              </p>
            </div>
            <Switch
              id="builder-review-before-pr"
              checked={requireBuilderReview}
              onCheckedChange={(checked) =>
                save({ settings: { builder_permissions: { require_review_before_pr: checked } } })
              }
              aria-label="Require builder review before PR"
            />
          </div>
        </CardContent>
      </Card>
    </section>
  );
}

function NetworkAccessSettings() {
  const { data: settingsResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });
  const { data: networkStatusResponse } = useQuery({
    queryKey: queryKeys.settings.network,
    queryFn: () => api.settings.getNetworkStatus(),
  });
  const { save, status } = useOrgSettingsAutosave();

  const settings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;
  const sandboxNetwork = settings.sandbox_network ?? {};
  const networkStatus = networkStatusResponse?.data;
  const available = networkStatus?.static_egress_available ?? false;
  const publicIP = networkStatus?.static_egress_public_ip;
  const unavailableReason = networkStatus?.static_egress_unavailable_reason;
  const enabled = sandboxNetwork.static_egress_enabled ?? networkStatus?.static_egress_enabled ?? false;

  const saveStaticEgress = (checked: boolean) => {
    save({
      settings: {
        sandbox_network: {
          ...sandboxNetwork,
          static_egress_enabled: checked,
        },
      },
    });
  };

  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between">
        <h2 className="text-xs font-medium text-foreground">Network access</h2>
        <AutosaveIndicator status={status} />
      </div>
      <Card>
        <CardContent className="space-y-4">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
            <div className="space-y-1">
              <Label htmlFor="static-egress-enabled">Use static egress IP for sessions and previews</Label>
              <p className="text-xs text-muted-foreground">
                Uses a stable public IP for new and hydrated sandboxes.
              </p>
              {!available && unavailableReason && (
                <p className="text-xs text-muted-foreground">{unavailableReason}</p>
              )}
            </div>
            <Switch
              id="static-egress-enabled"
              checked={enabled}
              onCheckedChange={saveStaticEgress}
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

function PreviewCapacitySettings() {
  const { data: settingsResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });

  const settings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;
  const currentMaxPreviewsPerUser =
    settings.preview_max_previews_per_user ?? DEFAULT_PREVIEW_MAX_PREVIEWS_PER_USER;
  const autosave = useOrgSettingsAutosave();
  const maxPreviewsPerUserField = useAutosaveNumericField({
    serverValue: currentMaxPreviewsPerUser,
    autosave,
    toPatch: (value) => ({ settings: { preview_max_previews_per_user: value } }),
    clamp: (value) =>
      Math.min(
        MAX_PREVIEW_MAX_PREVIEWS_PER_USER,
        Math.max(MIN_PREVIEW_MAX_PREVIEWS_PER_USER, value),
      ),
  });

  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between">
        <h2 className="text-xs font-medium text-foreground">Preview capacity</h2>
        <AutosaveIndicator status={autosave.status} />
      </div>
      <Card>
        <CardContent>
          <div className="max-w-[560px] space-y-2">
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
              Limits how many previews one user can keep running at once. Higher values consume more worker capacity.
            </p>
          </div>
        </CardContent>
      </Card>
    </section>
  );
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
          title="General settings"
          description="Manage your organization."
          subtitle={(() => {
            const formattedUpdatedAt = formatUpdatedAt(settings?.data?.updated_at);
            return formattedUpdatedAt ? `Updated at ${formattedUpdatedAt}` : undefined;
          })()}
        />
        <AuditLogTrigger
          filters={{ resource_type: "settings" }}
          title="Settings activity"
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

        {user?.role === "admin" && <NetworkAccessSettings />}
        {user?.role === "admin" && <PreviewCapacitySettings />}
        {user?.role === "admin" && <PRAuthorshipSettings />}
      </div>
    </PageContainer>
  );
}
