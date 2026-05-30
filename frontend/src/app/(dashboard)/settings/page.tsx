"use client";

import { useState } from "react";
import { Check, Plus, Trash2 } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { AuditLogTrigger } from "@/components/audit/audit-log-trigger";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { DebouncedInput } from "@/components/debounced-fields";
import { useAuth } from "@/hooks/use-auth";
import { useAutosave } from "@/hooks/useAutosave";
import { useAutosaveNumericField } from "@/hooks/useAutosaveNumericField";
import {
  applyOrgSettingsPatch,
  coalesceSettingsPatch,
  type SettingsPatch,
} from "@/lib/settings-autosave";
import { notify as toast } from "@/lib/notify";
import type { MembershipsResponse, Organization, OrgSettings, SingleResponse, VerifiedDomain } from "@/lib/types";

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

function DomainAccessSettings() {
  const [domain, setDomain] = useState("");
  const queryClient = useQueryClient();
  const { data: domainsResponse, isLoading } = useQuery({
    queryKey: queryKeys.settings.domains,
    queryFn: () => api.settings.domains.list(),
  });
  const domains = domainsResponse?.data ?? [];

  const invalidateDomains = () => {
    void queryClient.invalidateQueries({ queryKey: queryKeys.settings.domains });
  };

  const createMutation = useMutation({
    mutationFn: () => api.settings.domains.create({ domain, auto_join_role: "member" }),
    onSuccess: () => {
      setDomain("");
      invalidateDomains();
      toast.success("Domain challenge created");
    },
    onError: () => toast.error("Couldn't add domain."),
  });
  const verifyMutation = useMutation({
    mutationFn: (id: string) => api.settings.domains.verify(id),
    onSuccess: () => {
      invalidateDomains();
      toast.success("Domain verified");
    },
    onError: () => toast.error("TXT record not found yet."),
  });
  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.settings.domains.delete(id),
    onSuccess: invalidateDomains,
    onError: () => toast.error("Couldn't remove domain."),
  });

  const submitDomain = () => {
    if (!domain.trim()) return;
    createMutation.mutate();
  };

  return (
    <section className="space-y-3">
      <div>
        <h2 className="text-xs font-medium text-foreground">Domain access</h2>
        <p className="mt-1 text-xs text-muted-foreground">
          Verify a company email domain so Google users with matching verified emails join this organization automatically.
        </p>
      </div>
      <Card>
        <CardContent className="space-y-4">
          <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
            <div className="min-w-0 flex-1 space-y-2">
              <Label htmlFor="verified-domain">Domain</Label>
              <Input
                id="verified-domain"
                value={domain}
                onChange={(event) => setDomain(event.target.value)}
                placeholder="example.com"
                onKeyDown={(event) => {
                  if (event.key === "Enter") submitDomain();
                }}
              />
            </div>
            <Button
              type="button"
              onClick={submitDomain}
              disabled={!domain.trim() || createMutation.isPending}
            >
              <Plus className="h-4 w-4" />
              Add domain
            </Button>
          </div>

          <div className="space-y-3">
            {isLoading && <p className="text-xs text-muted-foreground">Loading domains...</p>}
            {!isLoading && domains.length === 0 && (
              <p className="text-xs text-muted-foreground">No verified domains yet.</p>
            )}
            {domains.map((item: VerifiedDomain) => (
              <div key={item.id} className="rounded-md border border-border p-3">
                <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                  <div className="min-w-0 space-y-2">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium text-foreground">{item.domain}</span>
                      <Badge variant={item.status === "verified" ? "default" : "secondary"}>
                        {item.status}
                      </Badge>
                    </div>
                    <div className="space-y-1 text-xs text-muted-foreground">
                      <p>Host: <span className="font-mono text-foreground">{item.verification_host}</span></p>
                      <p>TXT: <span className="break-all font-mono text-foreground">{item.verification_record}</span></p>
                    </div>
                  </div>
                  <div className="flex shrink-0 gap-2">
                    {item.status !== "verified" && (
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() => verifyMutation.mutate(item.id)}
                        disabled={verifyMutation.isPending}
                      >
                        <Check className="h-4 w-4" />
                        Verify
                      </Button>
                    )}
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      aria-label={`Remove ${item.domain}`}
                      onClick={() => deleteMutation.mutate(item.id)}
                      disabled={deleteMutation.isPending}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                </div>
              </div>
            ))}
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

        {user?.role === "admin" && <DomainAccessSettings />}
        {user?.role === "admin" && <PreviewCapacitySettings />}
        {user?.role === "admin" && <PRAuthorshipSettings />}
      </div>
    </PageContainer>
  );
}
