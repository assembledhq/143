"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { Card, CardContent } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { AuditLogTrigger } from "@/components/audit/audit-log-trigger";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { DebouncedInput } from "@/components/debounced-fields";
import { useAuth } from "@/hooks/use-auth";
import { useOrgSettingsAutosave } from "@/hooks/use-org-settings-autosave";
import type { Organization, OrgSettings, SingleResponse } from "@/lib/types";

const PR_AUTHORSHIP_OPTIONS = [
  { value: "user_preferred", label: "User preferred", description: "Use the user's GitHub token when available, fall back to the 143 app" },
  { value: "app_only", label: "App only", description: "Always create PRs as the 143 GitHub App" },
  { value: "user_required", label: "User required", description: "Require users to connect GitHub before creating PRs" },
] as const;

function PRAuthorshipSettings() {
  const { data: settingsResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });

  const { data: githubAccountStatus } = useQuery({
    queryKey: ["github-status"],
    queryFn: () => api.githubStatus.get(),
  });

  const settings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;
  const currentAuthorship = settings.pr_authorship ?? "user_preferred";
  const currentDraftDefault = settings.pr_draft_default ?? false;
  const currentAutoArchive = settings.auto_archive_on_pr_close ?? false;
  const requireBuilderReview = settings.builder_permissions?.require_review_before_pr ?? true;

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

        <AuditLogTrigger
          filters={{ resource_type: "settings" }}
          title="Settings activity"
          variant="footer"
        />
      </div>
    </PageContainer>
  );
}
