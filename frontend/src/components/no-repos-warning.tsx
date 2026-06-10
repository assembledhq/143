"use client";

import { useEffect } from "react";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, RefreshCw, Check } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { useGitHubRepoSync } from "@/hooks/use-github-repo-sync";
import { cn } from "@/lib/utils";

/** Error codes that mean the user needs to reinstall the GitHub App. */
const REINSTALL_ERROR_CODES = new Set([
  "MISSING_INSTALLATION_ID",
  "INVALID_CONFIG",
  "GITHUB_APP_NOT_CONFIGURED",
]);

function isReinstallError(err: unknown): boolean {
  return (
    err != null &&
    typeof err === "object" &&
    "code" in err &&
    typeof (err as { code: unknown }).code === "string" &&
    REINSTALL_ERROR_CODES.has((err as { code: string }).code)
  );
}

interface NoReposWarningProps {
  showDisconnectedState?: boolean;
  compact?: boolean;
}

export function NoReposWarning({
  showDisconnectedState = false,
  compact = false,
}: NoReposWarningProps) {
  const { data: integrationsResp } = useQuery({
    queryKey: ["integrations"],
    queryFn: () => api.integrations.list(),
  });

  const { data: reposResp } = useQuery({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
  });

  const hasGitHub = Boolean(
    integrationsResp?.data?.find(
      (i) => i.provider === "github" && i.status === "active"
    )
  );
  const githubIntegration = integrationsResp?.data?.find(
    (i) => i.provider === "github" && i.status === "active"
  );
  const githubAppInstalled = Boolean(githubIntegration?.github_app_installed);
  const repoSelectionRequired = Boolean(githubIntegration?.github_repo_selection_required);
  const repos = reposResp?.data ?? [];
  const hasRepos = repos.length > 0;

  const { sync, isSyncing, syncResult, syncError, autoSyncIfNeeded } =
    useGitHubRepoSync();

  const needsReinstall = isReinstallError(syncError);
  const shouldChooseRepos =
    githubAppInstalled ||
    repoSelectionRequired ||
    ((syncResult?.repos_seen ?? 0) > 0 && syncResult?.repos_synced === 0);

  useEffect(() => {
    autoSyncIfNeeded(hasGitHub && !githubAppInstalled, hasRepos);
  }, [hasGitHub, githubAppInstalled, hasRepos, autoSyncIfNeeded]);

  if (!hasGitHub) {
    if (!showDisconnectedState) return null;

    return (
      <div
        className={cn(
          "rounded-lg border border-warning/20 bg-warning/5 px-4 py-3",
          compact ? "space-y-3" : "flex items-start gap-3",
        )}
      >
        <div className={cn("flex gap-3", compact && "items-start")}>
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warning" />
          <div className="min-w-0 flex-1">
            <Badge variant="secondary" className="mb-2">
              GitHub setup required
            </Badge>
            <p className="text-xs text-warning">
              Connect GitHub before creating sessions or projects. Until a repository is linked, the agent won&apos;t have any code to work with.
            </p>
          </div>
        </div>
        <Button size="sm" variant="outline" asChild className={cn("gap-1.5", compact && "w-full")}>
          <Link href="/settings/integrations">Open integrations</Link>
        </Button>
      </div>
    );
  }

  // Don't render if repos exist (and no recent sync result to show)
  if (hasRepos && !syncResult && githubAppInstalled) return null;

  // After a successful sync that found repos, show success briefly then hide
  if (syncResult && syncResult.repos_synced > 0 && hasRepos) {
    return (
      <div className="flex items-center gap-3 rounded-lg border border-success/20 bg-success/5 px-4 py-3">
        <Check className="h-4 w-4 shrink-0 text-success" />
        <p className="text-xs text-success">
          {syncResult.repos_synced} repositor{syncResult.repos_synced === 1 ? "y" : "ies"} synced.
        </p>
      </div>
    );
  }

  // If repos now exist (from query refetch), hide the warning
  if (hasRepos) return null;

  return (
    <div className="flex items-center gap-3 rounded-lg border border-warning/20 bg-warning/5 px-4 py-3">
      <AlertTriangle className="h-4 w-4 shrink-0 text-warning" />
      <div className="flex-1 min-w-0">
        <p className="text-xs text-warning">
          {shouldChooseRepos
            ? "GitHub is connected, but no repositories are claimed for this organization. Choose repositories in integrations before creating sessions or projects."
            : "GitHub is connected but no repositories are synced. Sessions won't have access to your code."}
        </p>
        {syncError && needsReinstall && (
          <p className="mt-1 text-xs text-warning">
            The GitHub App installation ID is missing. Please reconnect GitHub to fix this.
          </p>
        )}
        {syncError && !needsReinstall && (
          <p className="mt-1 text-xs text-destructive">
            {syncError instanceof Error ? syncError.message : "Sync failed. Please try again."}
          </p>
        )}
        {syncResult && syncResult.repos_synced === 0 && !shouldChooseRepos && (
          <p className="mt-1 text-xs text-warning">
            No repositories found. Make sure the GitHub App has access to at least one repository.
          </p>
        )}
      </div>
      {needsReinstall ? (
        <Button
          size="sm"
          variant="outline"
          onClick={async () => {
            try {
              await api.integrations.disconnect("github");
            } catch (err) {
              console.error("Failed to disconnect GitHub before reconnect:", err);
            }
            api.integrations.loginGitHub();
          }}
          className="shrink-0 gap-1.5"
        >
          Reconnect GitHub
        </Button>
      ) : shouldChooseRepos ? (
        <Button size="sm" variant="outline" asChild className="shrink-0 gap-1.5">
          <Link href="/settings/integrations?select_repos=1">Choose repositories</Link>
        </Button>
      ) : (
        <Button
          size="sm"
          variant="outline"
          onClick={sync}
          disabled={isSyncing}
          className="shrink-0 gap-1.5"
        >
          <RefreshCw className={`h-3.5 w-3.5 ${isSyncing ? "animate-spin" : ""}`} />
          {isSyncing ? "Syncing..." : "Sync repositories"}
        </Button>
      )}
    </div>
  );
}
