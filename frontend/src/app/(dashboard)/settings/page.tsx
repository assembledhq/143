"use client";

import Link from "next/link";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { Card, CardContent } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { SectionGroup } from "@/components/section-group";
import { SettingsLastActivity } from "@/components/settings/settings-last-activity";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { DebouncedInput } from "@/components/debounced-fields";
import { useAuth } from "@/hooks/use-auth";
import { useOrgSettingsAutosave } from "@/hooks/use-org-settings-autosave";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import type { AutomaticFollowThroughOrgSettings, Organization, OrgSettings, SingleResponse } from "@/lib/types";

const PR_AUTHORSHIP_OPTIONS = [
  { value: "user_preferred", label: "User preferred", description: "Use the user's GitHub token when available, fall back to the 143 app" },
  { value: "app_only", label: "App only", description: "Always create PRs as the 143 GitHub App" },
  { value: "user_required", label: "User required", description: "Require users to connect GitHub before creating PRs" },
] as const;

type RepairAutomationKey = "resolve_conflicts_when_idle" | "fix_tests_when_idle";

const REPAIR_AUTOMATION_COPY: Record<RepairAutomationKey, { title: string; description: string; confirm: string }> = {
  resolve_conflicts_when_idle: {
    title: "Resolve conflicts when idle",
    description: "Start the existing conflict repair flow when an idle session has an open PR with merge conflicts.",
    confirm: "143 will be allowed to commit and push conflict-resolution work to the linked PR branch when this policy is active.",
  },
  fix_tests_when_idle: {
    title: "Fix failing tests when idle",
    description: "Start the existing test-repair flow when an idle session has an open PR with failing checks.",
    confirm: "143 will be allowed to commit and push test-repair work to the linked PR branch when this policy is active.",
  },
};

function sessionAutomationPatch(
  current: AutomaticFollowThroughOrgSettings,
  patch: Partial<AutomaticFollowThroughOrgSettings>,
): Partial<OrgSettings> {
  return {
    session_automation: {
      automatic_follow_through: {
        ...current,
        ...patch,
      },
    },
  };
}

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
  const currentAutoArchive = settings.auto_archive_on_pr_close ?? true;
  const automaticFollowThrough = settings.session_automation?.automatic_follow_through ?? {};
  const [repairEnableCandidate, setRepairEnableCandidate] = useState<RepairAutomationKey | null>(null);

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
  const saveAutomaticFollowThrough = (patch: Partial<AutomaticFollowThroughOrgSettings>) => {
    save({ settings: sessionAutomationPatch(automaticFollowThrough, patch) });
  };
  const setRepairAutomation = (key: RepairAutomationKey, enabled: boolean) => {
    if (enabled && !automaticFollowThrough[key]) {
      setRepairEnableCandidate(key);
      return;
    }
    saveAutomaticFollowThrough({ [key]: enabled });
  };
  const confirmRepairAutomation = () => {
    if (!repairEnableCandidate) return;
    saveAutomaticFollowThrough({ [repairEnableCandidate]: true });
    setRepairEnableCandidate(null);
  };

  return (
    <>
    <section className="space-y-3">
      <div className="flex items-center justify-between">
        <h2 className="text-xs font-medium text-foreground">Session automation</h2>
        <AutosaveIndicator status={status} />
      </div>
      <Card>
        <CardContent className="space-y-5">
          <div className="space-y-4 border-t border-border pt-4">
            <div className="space-y-1">
              <h3 className="text-xs font-medium text-foreground">Branch-writing repair</h3>
              <p className="text-xs text-muted-foreground">
                These policies use the same repair actions as the manual buttons when an open PR is blocked and the session is idle.
              </p>
            </div>
            {Object.entries(REPAIR_AUTOMATION_COPY).map(([key, copy]) => {
              const repairKey = key as RepairAutomationKey;
              return (
                <div key={repairKey} className="flex items-start justify-between gap-4">
                  <div className="space-y-1">
                    <Label htmlFor={`session-automation-${repairKey}`}>{copy.title}</Label>
                    <p className="text-xs text-muted-foreground">{copy.description}</p>
                  </div>
                  <Switch
                    id={`session-automation-${repairKey}`}
                    checked={automaticFollowThrough[repairKey] ?? false}
                    onCheckedChange={(checked) => setRepairAutomation(repairKey, checked)}
                    aria-label={copy.title}
                  />
                </div>
              );
            })}
          </div>
        </CardContent>
      </Card>
      <AlertDialog open={repairEnableCandidate !== null} onOpenChange={(open) => {
        if (!open) setRepairEnableCandidate(null);
      }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Enable automatic branch repair?</AlertDialogTitle>
            <AlertDialogDescription>
              {repairEnableCandidate ? REPAIR_AUTOMATION_COPY[repairEnableCandidate].confirm : ""}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={confirmRepairAutomation}>Enable</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>

    <section className="space-y-3">
      <div className="flex items-center justify-between">
        <h2 className="text-xs font-medium text-foreground">Pull requests</h2>
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
        </CardContent>
      </Card>
    </section>
    </>
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

        <SectionGroup
          title="Organization"
          description="The workspace name shown throughout 143."
          action={user?.role === "admin" ? <AutosaveIndicator status={autosave.status} /> : undefined}
          variant="bordered"
        >
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
        </SectionGroup>

        {user?.role === "admin" && <PRAuthorshipSettings />}

        <SettingsLastActivity
          scopes={{ resource_type: "settings" }}
          title="Settings activity"
        />
      </div>
    </PageContainer>
  );
}
