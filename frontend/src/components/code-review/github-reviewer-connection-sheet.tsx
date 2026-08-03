"use client";

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Check, FolderGit2, Github, LockKeyhole, RefreshCw, Search } from "lucide-react";
import { EmptyState } from "@/components/empty-state";
import { StatusLabel } from "@/components/status-label";
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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { ErrorNotice } from "@/components/ui/error-notice";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { useGitHubRepositoryClaims } from "@/hooks/use-github-repository-claims";
import { api, ApiError } from "@/lib/api";
import { notify } from "@/lib/notify";
import { queryKeys } from "@/lib/query-keys";
import type { CodeReviewGitHubTriggerResponse, GitHubRepositoryClaimCandidate } from "@/lib/types";

interface RepositorySetupFailure {
  message: string;
  connected: boolean;
  code?: string;
}

class RepositorySetupError extends Error {
  connected: boolean;
  code?: string;

  constructor(error: unknown, connected: boolean) {
    super(error instanceof Error ? error.message : "GitHub reviewer setup failed.");
    this.name = "RepositorySetupError";
    this.connected = connected;
    this.code = error instanceof ApiError ? error.code : undefined;
  }
}

export function GitHubReviewerConnectionSheet({
  open,
  onOpenChange,
  canManage,
  githubConnected,
  githubStatusLoading,
  githubStatusError,
  onRetryGithubStatus,
  installationId,
  accountConnected,
  accountNeedsReconnect,
  accountStatusLoading,
  accountStatusError,
  onRetryAccountStatus,
  triggerStatuses,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  canManage: boolean;
  githubConnected: boolean;
  githubStatusLoading: boolean;
  githubStatusError?: string;
  onRetryGithubStatus: () => void;
  installationId?: number;
  accountConnected: boolean;
  accountNeedsReconnect: boolean;
  accountStatusLoading: boolean;
  accountStatusError?: string;
  onRetryAccountStatus: () => void;
  triggerStatuses: CodeReviewGitHubTriggerResponse[];
}) {
  const queryClient = useQueryClient();
  const [search, setSearch] = useState("");
  const [transferCandidate, setTransferCandidate] = useState<GitHubRepositoryClaimCandidate | null>(null);
  const [completedGitHubIDs, setCompletedGitHubIDs] = useState<ReadonlySet<number>>(() => new Set());
  const [connectedRepositoryIDs, setConnectedRepositoryIDs] = useState<Readonly<Partial<Record<number, string>>>>({});
  const [failures, setFailures] = useState<Readonly<Record<number, RepositorySetupFailure>>>({});
  const [accountAuthFailed, setAccountAuthFailed] = useState(false);
  const { candidatesQuery, claimMutation } = useGitHubRepositoryClaims({
    installationId,
    enabled: open && githubConnected,
  });
  const triggerByRepositoryID = useMemo(
    () => new Map(triggerStatuses.map((status) => [status.repository_id, status])),
    [triggerStatuses],
  );
  const candidates = useMemo(() => {
    const query = search.trim().toLocaleLowerCase();
    const filtered = (candidatesQuery.data?.data ?? []).filter((candidate) =>
      query === "" || candidate.full_name.toLocaleLowerCase().includes(query),
    );
    return filtered.slice().sort((left, right) => {
      const leftReady = completedGitHubIDs.has(left.github_id) || triggerByRepositoryID.get(left.repository_id ?? "")?.status === "ready";
      const rightReady = completedGitHubIDs.has(right.github_id) || triggerByRepositoryID.get(right.repository_id ?? "")?.status === "ready";
      if (leftReady !== rightReady) return leftReady ? 1 : -1;
      return left.full_name.localeCompare(right.full_name);
    });
  }, [candidatesQuery.data?.data, completedGitHubIDs, search, triggerByRepositoryID]);

  useEffect(() => {
    if (open) return;
    setSearch("");
    setTransferCandidate(null);
    setCompletedGitHubIDs(new Set());
    setConnectedRepositoryIDs({});
    setFailures({});
    setAccountAuthFailed(false);
  }, [open]);

  const setupMutation = useMutation({
    mutationFn: async ({ candidate, allowTransfer }: { candidate: GitHubRepositoryClaimCandidate; allowTransfer: boolean }) => {
      let repositoryID = connectedRepositoryIDs[candidate.github_id]
        ?? (candidate.status === "owned_by_current_org" ? candidate.repository_id : undefined);
      let connected = !!repositoryID;
      try {
        if (!repositoryID) {
          await claimMutation.mutateAsync({ githubId: candidate.github_id, allowTransfer });
          connected = true;
          const repositories = await api.repositories.list();
          repositoryID = repositories.data.find((repository) => repository.github_id === candidate.github_id)?.id;
        }
        if (!repositoryID) {
          throw new Error("The repository connected, but 143 could not resolve it for reviewer setup. Refresh and try again.");
        }
        setConnectedRepositoryIDs((current) => ({ ...current, [candidate.github_id]: repositoryID }));
        const setup = await api.codeReviews.setupGitHubTrigger(repositoryID);
        return { candidate, repositoryID, reviewer: setup.data.team_reviewer };
      } catch (error) {
        throw new RepositorySetupError(error, connected);
      }
    },
    onMutate: ({ candidate }) => {
      setFailures((current) => Object.fromEntries(
        Object.entries(current).filter(([githubID]) => Number(githubID) !== candidate.github_id),
      ));
    },
    onSuccess: ({ candidate, repositoryID, reviewer }) => {
      setTransferCandidate(null);
      setCompletedGitHubIDs((current) => new Set([...current, candidate.github_id]));
      void queryClient.invalidateQueries({ queryKey: queryKeys.codeReviews.githubTriggers });
      void queryClient.invalidateQueries({ queryKey: queryKeys.codeReviews.githubTrigger(repositoryID) });
      void queryClient.invalidateQueries({ queryKey: queryKeys.repositories.all });
      notify.success(`GitHub reviewer added to ${candidate.full_name}`, {
        description: `Team members can now request ${reviewer ?? "the 143 reviewer"} on pull requests.`,
      });
      void api.codeReviews.policyEvent({
        event: "code_review_github_setup_completed",
        scope: "repository",
        configured: true,
      }).catch((error) => console.error("Failed to record GitHub reviewer setup event", error));
    },
    onError: (error, { candidate }) => {
      setTransferCandidate(null);
      const failure = error instanceof RepositorySetupError
        ? { message: error.message, connected: error.connected, code: error.code }
        : { message: "GitHub reviewer setup failed.", connected: false };
      if (failure.code === "GITHUB_USER_AUTH_REQUIRED") {
        setAccountAuthFailed(true);
        void queryClient.invalidateQueries({ queryKey: ["github-status"] });
      }
      setFailures((current) => ({ ...current, [candidate.github_id]: failure }));
      void api.codeReviews.policyEvent({
        event: "code_review_github_setup_failed",
        scope: "repository",
        configured: false,
      }).catch((eventError) => console.error("Failed to record GitHub reviewer setup failure event", eventError));
    },
  });

  const connectAccount = () => {
    api.githubStatus.connect(undefined, "/code-reviews?tab=policy&add_repository=1");
  };

  const manageDisabledReason = !canManage
    ? "Only organization administrators can add GitHub reviewer connections."
    : githubStatusLoading || accountStatusLoading
      ? "143 is checking your GitHub connection."
      : githubStatusError || accountStatusError
        ? "Retry the GitHub connection check before setting up a reviewer."
    : !accountConnected || accountNeedsReconnect || accountAuthFailed
      ? "Connect your GitHub account first so 143 can create the reviewer team and verify repository access."
      : undefined;

  return (
    <>
      <Sheet open={open} onOpenChange={(nextOpen) => {
        if (!nextOpen && setupMutation.isPending) return;
        onOpenChange(nextOpen);
      }}>
        <SheetContent className="flex w-[calc(100vw-1rem)] flex-col gap-5 overflow-hidden p-0 sm:max-w-xl">
          <SheetHeader className="border-b border-border px-5 pb-4 pt-5 pr-12">
            <SheetTitle>Add GitHub reviewer</SheetTitle>
            <SheetDescription>
              Connect a repository and add the 143 reviewer entry point without leaving the review policy.
            </SheetDescription>
          </SheetHeader>

          <div className="flex min-h-0 flex-1 flex-col gap-4 px-5 pb-5">
            {githubStatusLoading ? (
              <div className="flex items-center gap-2 py-8 text-sm text-muted-foreground" role="status">
                <RefreshCw className="h-4 w-4 animate-spin" aria-hidden="true" />
                Checking the GitHub App connection…
              </div>
            ) : githubStatusError ? (
              <ErrorNotice
                title="GitHub App status could not be loaded"
                description={githubStatusError}
                action={{ label: "Retry", onClick: onRetryGithubStatus }}
              />
            ) : !githubConnected || !installationId ? (
              <EmptyState
                variant="inline"
                icon={Github}
                title="Connect the GitHub App first"
                description="Install the GitHub App for this organization, then choose repositories for the reviewer."
                action={canManage ? { label: "Connect GitHub App", onClick: () => api.integrations.loginGitHub("/code-reviews?tab=policy&add_repository=1") } : undefined}
              />
            ) : (
              <>
                {accountStatusLoading ? (
                  <Card>
                    <CardContent className="flex items-center gap-2 p-3 text-sm text-muted-foreground" role="status">
                      <RefreshCw className="h-4 w-4 animate-spin" aria-hidden="true" />
                      Checking your GitHub account…
                    </CardContent>
                  </Card>
                ) : accountStatusError ? (
                  <ErrorNotice
                    title="GitHub account status could not be loaded"
                    description={accountStatusError}
                    action={{ label: "Retry", onClick: onRetryAccountStatus }}
                  />
                ) : !accountConnected || accountNeedsReconnect || accountAuthFailed ? (
                  <Card className="border-attention/30 bg-attention/10">
                    <CardContent className="flex flex-wrap items-center gap-3 p-3">
                      <LockKeyhole className="h-4 w-4 text-attention" aria-hidden="true" />
                      <div className="min-w-0 flex-1">
                        <p className="text-sm font-medium text-foreground">
                          {accountNeedsReconnect || accountAuthFailed ? "Reconnect your GitHub account" : "Connect your GitHub account"}
                        </p>
                        <p className="text-xs text-muted-foreground">
                          143 uses your authorization to create the reviewer team and confirm repository access.
                        </p>
                      </div>
                      {canManage ? (
                        <Button size="sm" onClick={connectAccount}>
                          {accountNeedsReconnect || accountAuthFailed ? "Reconnect account" : "Connect account"}
                        </Button>
                      ) : null}
                    </CardContent>
                  </Card>
                ) : null}

                <div className="relative">
                  <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
                  <Input
                    aria-label="Search GitHub repositories"
                    placeholder="Search repositories…"
                    value={search}
                    onChange={(event) => setSearch(event.target.value)}
                    className="pl-9"
                  />
                </div>

                <ScrollArea className="min-h-0 flex-1">
                  <div className="pr-3">
                    {candidatesQuery.isLoading ? (
                      <div className="flex items-center gap-2 py-8 text-sm text-muted-foreground" role="status">
                        <RefreshCw className="h-4 w-4 animate-spin" aria-hidden="true" />
                        Loading GitHub repositories…
                      </div>
                    ) : candidatesQuery.isError && !candidatesQuery.data ? (
                      <ErrorNotice
                        title="GitHub repositories could not be loaded"
                        description={candidatesQuery.error instanceof Error ? candidatesQuery.error.message : "Try again in a moment."}
                        action={{ label: "Retry", onClick: () => void candidatesQuery.refetch() }}
                      />
                    ) : (
                      <>
                        {candidatesQuery.isRefetchError ? (
                          <div className="mb-3">
                            <ErrorNotice
                              title="GitHub repositories could not be refreshed"
                              description={candidatesQuery.error instanceof Error ? candidatesQuery.error.message : "The last loaded repository list is still available below."}
                              action={{ label: "Retry refresh", onClick: () => void candidatesQuery.refetch() }}
                            />
                          </div>
                        ) : null}
                        {candidates.length === 0 ? (
                          <EmptyState
                            variant="inline"
                            icon={FolderGit2}
                            title={search ? "No repositories match your search" : "No repositories are available"}
                            description={search ? "Try another repository name." : "Grant the GitHub App access to another repository, then refresh this list."}
                            action={search ? { label: "Clear search", onClick: () => setSearch("") } : { label: "Manage GitHub App access", href: "/settings/integrations" }}
                          />
                        ) : (
                          <div className="space-y-2" aria-label="Available GitHub repositories">
                            <p className="text-xs tabular-nums text-muted-foreground">
                              {candidates.length} repositor{candidates.length === 1 ? "y" : "ies"}
                            </p>
                            {candidates.map((candidate) => {
                              const trigger = triggerByRepositoryID.get(candidate.repository_id ?? "");
                              const ready = completedGitHubIDs.has(candidate.github_id) || trigger?.status === "ready";
                              const transfer = candidate.status === "owned_by_other_org";
                              const connected = candidate.status === "owned_by_current_org" || !!connectedRepositoryIDs[candidate.github_id];
                              const disconnected = candidate.status === "disconnected_in_current_org";
                              const pending = setupMutation.isPending && setupMutation.variables?.candidate.github_id === candidate.github_id;
                              const failure = failures[candidate.github_id];
                              const actionLabel = connected ? "Set up reviewer" : transfer ? "Transfer…" : disconnected ? "Reconnect & set up" : "Connect & set up";
                              const retryLabel = failure?.connected
                                ? "Retry setup"
                                : transfer
                                  ? "Retry transfer…"
                                  : disconnected
                                    ? "Retry reconnect"
                                    : "Retry connection";
                              return (
                                <Card key={candidate.github_id} className="rounded-lg">
                                  <CardContent className="flex flex-wrap items-start gap-3 p-3">
                                    <div className="min-w-0 flex-1">
                                      <div className="flex min-w-0 flex-wrap items-center gap-2">
                                        <p className="truncate text-sm font-medium text-foreground">{candidate.full_name}</p>
                                        {candidate.private ? <Badge variant="outline">Private</Badge> : null}
                                      </div>
                                      <div className="mt-1.5">
                                        {ready ? (
                                          <StatusLabel label="Ready" tone="success" icon={<Check className="h-3.5 w-3.5" />} indicator="icon" />
                                        ) : transfer ? (
                                          <StatusLabel label={candidate.can_transfer ? "Other workspace" : "Owned by another workspace"} tone="attention" />
                                        ) : connected ? (
                                          <StatusLabel label="Connected" tone="neutral" />
                                        ) : disconnected ? (
                                          <StatusLabel label="Disconnected" tone="attention" />
                                        ) : (
                                          <StatusLabel label="Available" tone="neutral" />
                                        )}
                                      </div>
                                      {failure ? (
                                        <p className="mt-2 text-xs text-destructive" role="alert">
                                          {failure.connected ? "Repository connected, but reviewer setup failed. " : ""}{failure.message}
                                        </p>
                                      ) : null}
                                    </div>
                                    {!ready ? (
                                      <DisabledTooltip disabled={!!manageDisabledReason || (transfer && !candidate.can_transfer)} content={transfer && !candidate.can_transfer ? "An administrator in the owning workspace must transfer this repository." : manageDisabledReason}>
                                        <Button
                                          size="sm"
                                          variant={transfer ? "outline" : "default"}
                                          loading={pending}
                                          disabled={!!manageDisabledReason || setupMutation.isPending || (transfer && !candidate.can_transfer)}
                                          onClick={() => {
                                            if (transfer) setTransferCandidate(candidate);
                                            else setupMutation.mutate({ candidate, allowTransfer: false });
                                          }}
                                        >
                                          {pending ? "Setting up…" : failure ? retryLabel : actionLabel}
                                        </Button>
                                      </DisabledTooltip>
                                    ) : null}
                                  </CardContent>
                                </Card>
                              );
                            })}
                          </div>
                        )}
                      </>
                    )}
                  </div>
                </ScrollArea>

                <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border pt-4">
                  <Button asChild variant="ghost" size="sm">
                    <Link href="/settings/integrations">Manage all GitHub settings</Link>
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={setupMutation.isPending}
                    onClick={() => onOpenChange(false)}
                  >
                    Done
                  </Button>
                </div>
              </>
            )}
          </div>
        </SheetContent>
      </Sheet>

      <AlertDialog
        open={!!transferCandidate}
        onOpenChange={(nextOpen) => {
          if (!nextOpen && !setupMutation.isPending) setTransferCandidate(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Transfer {transferCandidate?.full_name}?</AlertDialogTitle>
            <AlertDialogDescription>
              This disconnects the repository from {transferCandidate?.owner_org_name ?? "its current workspace"} and connects it here. Existing sessions and settings do not move.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={setupMutation.isPending}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              disabled={!transferCandidate || setupMutation.isPending}
              onClick={(event) => {
                event.preventDefault();
                if (transferCandidate) setupMutation.mutate({ candidate: transferCandidate, allowTransfer: true });
              }}
            >
              {setupMutation.isPending ? "Transferring…" : "Transfer and set up"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
