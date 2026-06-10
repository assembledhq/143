"use client";

import { useState, type ReactNode } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { CircleHelp, ExternalLink, RefreshCw, Trash2 } from "lucide-react";
import { ApiError, api } from "@/lib/api";
import { AllIntegrationCards } from "@/components/integration-connection-cards";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import {
  Command,
  CommandCheckItem,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandList,
} from "@/components/ui/command";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
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
import { useAutosave } from "@/hooks/useAutosave";
import { useDisconnectIntegration } from "@/hooks/use-disconnect-integration";
import { queryKeys } from "@/lib/query-keys";
import { useAuth } from "@/hooks/use-auth";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { GitHubRepositoryClaimCandidate, LinearTeamKey, LinearTeamRepoMapping, Repository } from "@/lib/types";
import { getIntegrationByKey, type IntegrationKey } from "@/lib/integrations";

type SlackChannel = { id: string; name: string; selected: boolean };
type SlackChannelsResp = { data: SlackChannel[] } | undefined;
const NO_DEFAULT_REPO_VALUE = "__none__";
const CARD_PILL_LIMIT = 3;

// Coalesce multi-toggle bursts: the later selection wins. Hoisted so every
// `useAutosave` caller sharing `queryKeys.integrations.slackChannels` passes
// the same referential identity - `useAutosave` throws in dev when two
// callers register different coalesce fns against the same queryKey.
const coalesceSlackChannels = (_a: string[], b: string[]): string[] => b;

function claimStatusLabel(repo: GitHubRepositoryClaimCandidate): string {
  switch (repo.status) {
    case "owned_by_current_org":
      return "Connected";
    case "owned_by_other_org":
      return repo.owner_org_name ? `Owned by ${repo.owner_org_name}` : "Owned by another org";
    case "disconnected_in_current_org":
      return "Disconnected";
    case "unclaimed":
    default:
      return "Available";
  }
}

function SummaryPills({ values, empty }: { values: string[]; empty: string }) {
  if (values.length === 0) {
    return <p className="mt-1.5 text-xs text-muted-foreground">{empty}</p>;
  }
  const visible = values.slice(0, CARD_PILL_LIMIT);
  const hiddenCount = values.length - visible.length;
  return (
    <div className="mt-1.5 flex min-w-0 flex-wrap gap-1.5">
      {visible.map((value) => (
        <Badge key={value} variant="secondary" className="max-w-44 truncate rounded-md text-xs">
          {value}
        </Badge>
      ))}
      {hiddenCount > 0 ? (
        <Badge variant="outline" className="rounded-md text-xs">+{hiddenCount} more</Badge>
      ) : null}
    </div>
  );
}

function GitHubCardSummary({ repositories }: { repositories: Repository[] }) {
  const activeRepos = repositories.filter((repo) => repo.status === "active").map((repo) => repo.full_name);
  return <SummaryPills values={activeRepos} empty="No repositories connected" />;
}

function ConnectedRepositoryControls({
  repositories,
  onDisconnectRepo,
  onReconnectRepo,
  pendingRepoID,
}: {
  repositories: Repository[];
  onDisconnectRepo: (repoID: string) => void;
  onReconnectRepo: (repoID: string) => void;
  pendingRepoID?: string | null;
}) {
  if (repositories.length === 0) return null;
  return (
    <div className="space-y-2">
      <div className="text-xs font-medium uppercase text-muted-foreground">Current repository links</div>
      <div className="space-y-2">
        {repositories.map((repo) => {
          const active = repo.status === "active";
          const pending = pendingRepoID === repo.id;
          return (
            <div key={repo.id} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 rounded-md border border-border px-3 py-2">
              <div className="min-w-0">
                <div className="truncate text-sm font-medium">{repo.full_name}</div>
                <div className="mt-1">
                  <Badge variant={active ? "secondary" : "outline"} className="text-xs">
                    {active ? "Connected" : "Disconnected"}
                  </Badge>
                </div>
              </div>
              {active ? (
                <Button
                  size="sm"
                  variant="outline"
                  loading={pending}
                  disabled={pending}
                  onClick={() => onDisconnectRepo(repo.id)}
                  aria-label={`Disconnect ${repo.full_name}`}
                >
                  Disconnect
                </Button>
              ) : (
                <Button
                  size="sm"
                  variant="outline"
                  loading={pending}
                  disabled={pending}
                  onClick={() => onReconnectRepo(repo.id)}
                  aria-label={`Reconnect ${repo.full_name}`}
                >
                  Reconnect
                </Button>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function GitHubRepositoryClaims({
  installationId,
  enabled,
  repositories,
  onDisconnectRepo,
  onReconnectRepo,
  pendingRepoID,
  onSyncRepos,
  isSyncing,
}: {
  installationId?: number;
  enabled: boolean;
  repositories: Repository[];
  onDisconnectRepo: (repoID: string) => void;
  onReconnectRepo: (repoID: string) => void;
  pendingRepoID?: string | null;
  onSyncRepos: () => void;
  isSyncing: boolean;
}) {
  const queryClient = useQueryClient();
  const [transferRepo, setTransferRepo] = useState<GitHubRepositoryClaimCandidate | null>(null);
  const { data, isLoading, error } = useQuery({
    queryKey: queryKeys.integrations.githubRepositories(installationId),
    queryFn: () => api.integrations.listGitHubRepositories(installationId),
    enabled: enabled && !!installationId,
  });
  const claimMutation = useMutation({
    mutationFn: ({ githubId, allowTransfer }: { githubId: number; allowTransfer: boolean }) =>
      api.integrations.claimGitHubRepositories(installationId ?? 0, [githubId], allowTransfer),
    onSuccess: () => {
      setTransferRepo(null);
      queryClient.invalidateQueries({ queryKey: queryKeys.integrations.all });
      queryClient.invalidateQueries({ queryKey: queryKeys.repositories.all });
      queryClient.invalidateQueries({ queryKey: ["repositories", "integrations", "include-disconnected"] });
      queryClient.invalidateQueries({ queryKey: queryKeys.integrations.githubRepositories(installationId) });
    },
  });

  if (!enabled || !installationId) return null;

  const repos = data?.data ?? [];
  const actionable = repos.filter((repo) =>
    repo.status === "unclaimed" || repo.status === "disconnected_in_current_org" || (repo.status === "owned_by_other_org" && repo.can_transfer)
  );
  const connectedCount = repos.filter((repo) => repo.status === "owned_by_current_org").length;
  const claimError = claimMutation.error;
  const needsGitHubUserAuth = claimError instanceof ApiError && claimError.code === "GITHUB_USER_AUTH_REQUIRED";

  return (
    <>
      <div className="space-y-4">
        <div className="flex items-center justify-between gap-3">
          <div>
            <h3 className="text-sm font-medium">Repository access</h3>
            <p className="mt-1 text-sm text-muted-foreground">
              Choose which repositories this organization can use for sessions, automation, and PR creation.
            </p>
          </div>
          <Button
            size="icon"
            variant="ghost"
            className="h-8 w-8"
            onClick={onSyncRepos}
            disabled={isSyncing}
            aria-label="Sync repositories"
          >
            <RefreshCw className={`h-3.5 w-3.5 ${isSyncing ? "animate-spin" : ""}`} />
          </Button>
        </div>
        <ConnectedRepositoryControls
          repositories={repositories}
          onDisconnectRepo={onDisconnectRepo}
          onReconnectRepo={onReconnectRepo}
          pendingRepoID={pendingRepoID}
        />
        {isLoading ? (
          <p className="text-sm text-muted-foreground">Loading repositories...</p>
        ) : error ? (
          <p className="text-sm text-destructive">
            {error instanceof Error ? error.message : "Failed to load GitHub repositories."}
          </p>
        ) : repos.length === 0 ? (
          <p className="text-sm text-muted-foreground">No repositories are available to this GitHub App installation.</p>
        ) : (
          <div className="space-y-2">
            <p className="text-xs text-muted-foreground">
              {repos.length} repositor{repos.length === 1 ? "y" : "ies"} · {connectedCount} connected · {actionable.length} available
            </p>
            <div
              data-testid="github-repository-grid"
              className="grid grid-cols-[repeat(auto-fit,minmax(12rem,1fr))] gap-2"
            >
              {repos.map((repo) => {
                const transfer = repo.status === "owned_by_other_org";
                const canClaim = repo.status === "unclaimed" || repo.status === "disconnected_in_current_org" || (transfer && repo.can_transfer);
                const pending = claimMutation.isPending && claimMutation.variables?.githubId === repo.github_id;
                return (
                  <div key={repo.github_id} className="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-2 rounded-md border border-border px-3 py-2">
                    <div className="min-w-0">
                      <div className="truncate text-sm font-medium">{repo.full_name}</div>
                      <div className="mt-1 flex min-w-0 flex-wrap items-center gap-1.5">
                        <Badge variant={repo.status === "owned_by_current_org" ? "secondary" : "outline"} className="text-xs">
                          {claimStatusLabel(repo)}
                        </Badge>
                        {repo.private && <span className="text-xs text-muted-foreground">Private</span>}
                      </div>
                    </div>
                    {canClaim ? (
                      <Button
                        size="sm"
                        variant={transfer ? "outline" : "default"}
                        loading={pending}
                        disabled={pending}
                        onClick={() => {
                          if (transfer) setTransferRepo(repo);
                          else claimMutation.mutate({ githubId: repo.github_id, allowTransfer: false });
                        }}
                      >
                        {transfer ? "Transfer" : "Claim"}
                      </Button>
                    ) : null}
                  </div>
                );
              })}
            </div>
            {claimMutation.isError && (
              <div className="flex flex-col items-start gap-2 rounded-md border border-destructive/30 bg-destructive/5 p-3">
                <p className="text-sm text-destructive">
                  {claimError instanceof Error ? claimError.message : "Failed to claim repository."}
                </p>
                {needsGitHubUserAuth && (
                  <Button size="sm" variant="outline" onClick={() => api.githubStatus.connect()}>
                    Connect GitHub account
                  </Button>
                )}
              </div>
            )}
            {actionable.length === 0 && (
              <p className="text-xs text-muted-foreground">All available repositories are already accounted for.</p>
            )}
          </div>
        )}
      </div>
      <AlertDialog open={!!transferRepo} onOpenChange={(open) => !open && setTransferRepo(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Transfer {transferRepo?.full_name}?</AlertDialogTitle>
            <AlertDialogDescription>
              This will disconnect the repository from {transferRepo?.owner_org_name ?? "the current owning organization"} and make this organization the active owner. Sessions, settings, automations, and learned context will not move.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={claimMutation.isPending}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              disabled={claimMutation.isPending || !transferRepo}
              onClick={() => {
                if (!transferRepo) return;
                claimMutation.mutate({ githubId: transferRepo.github_id, allowTransfer: true });
              }}
            >
              Transfer
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function SlackChannelPicker() {
  const { data: channelsResp, isLoading } = useQuery<{ data: SlackChannel[] }>({
    queryKey: queryKeys.integrations.slackChannels,
    queryFn: () => api.integrations.listSlackChannels(),
  });

  const { save, status } = useAutosave<string[]>({
    queryKey: queryKeys.integrations.slackChannels,
    mutationFn: (channelIds) => api.integrations.updateSlackChannels(channelIds),
    applyOptimistic: (prev, channelIds) => {
      const previous = prev as SlackChannelsResp;
      if (!previous?.data) return previous;
      const selectedSet = new Set(channelIds);
      return {
        ...previous,
        data: previous.data.map((ch) => ({ ...ch, selected: selectedSet.has(ch.id) })),
      };
    },
    coalesce: coalesceSlackChannels,
  });

  const channels = channelsResp?.data ?? [];
  const selectedIds = channels.filter((ch) => ch.selected).map((ch) => ch.id);
  const selected = new Set(selectedIds);

  if (isLoading) {
    return (
      <div>
        <h3 className="text-sm font-medium">Monitored Slack channels</h3>
        <p className="mt-1 text-sm text-muted-foreground">Loading channels...</p>
      </div>
    );
  }

  const toggle = (id: string) => {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    save(Array.from(next));
  };

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h3 className="text-sm font-medium">Monitored Slack channels</h3>
          <p className="mt-1 text-sm text-muted-foreground">
            Select which channels the PM agent should monitor for actionable conversations.
          </p>
        </div>
        <AutosaveIndicator status={status} />
      </div>
      {channels.length === 0 ? (
        <p className="text-sm text-muted-foreground">No channels found.</p>
      ) : (
        <div className="space-y-3">
          <Command className="rounded-md border border-border">
            <CommandInput placeholder="Search channels..." />
            <CommandList className="max-h-[24rem]">
              <CommandEmpty>No channels found.</CommandEmpty>
              <CommandGroup>
                {channels.map((ch) => (
                  <CommandCheckItem
                    key={ch.id}
                    checked={selected.has(ch.id)}
                    value={`${ch.name} #${ch.name}`}
                    aria-label={`Monitor #${ch.name}`}
                    onSelect={() => toggle(ch.id)}
                  >
                    <span className="truncate font-medium">#{ch.name}</span>
                  </CommandCheckItem>
                ))}
              </CommandGroup>
            </CommandList>
          </Command>
          <p className="text-xs text-muted-foreground">
            {selected.size} channel{selected.size !== 1 ? "s" : ""} selected
          </p>
        </div>
      )}
    </div>
  );
}

function SlackCardSummary() {
  const { data: channelsResp, isLoading } = useQuery<{ data: SlackChannel[] }>({
    queryKey: queryKeys.integrations.slackChannels,
    queryFn: () => api.integrations.listSlackChannels(),
  });
  if (isLoading) {
    return <p className="mt-1.5 text-xs text-muted-foreground">Loading channel summary...</p>;
  }
  const selected = (channelsResp?.data ?? []).filter((ch) => ch.selected);
  return (
    <div>
      <p className="mt-1.5 text-xs text-muted-foreground">
        Monitoring {selected.length} channel{selected.length === 1 ? "" : "s"}
      </p>
      <SummaryPills values={selected.map((ch) => `#${ch.name}`)} empty="No channels selected" />
    </div>
  );
}

function repoName(repositories: Repository[], repoID?: string): string {
  return repositories.find((repo) => repo.id === repoID)?.full_name ?? repoID ?? "Unknown repository";
}

function linearTeamLabel(team: LinearTeamKey): string {
  return team.team_name ? `${team.team_name} (${team.team_key})` : team.team_key;
}

function linearTeamMappingLabel(teams: LinearTeamKey[], teamKey: string): string {
  const team = teams.find((candidate) => candidate.team_key === teamKey);
  return team ? linearTeamLabel(team) : teamKey;
}

function LinearAgentRoutingSettings({ repositoriesOverride }: { repositoriesOverride?: Repository[] }) {
  const queryClient = useQueryClient();
  const [teamID, setTeamID] = useState("");
  const [projectID, setProjectID] = useState("");
  const [mappingRepoID, setMappingRepoID] = useState("");

  const { data: statusResp, isLoading: statusLoading } = useQuery({
    queryKey: queryKeys.integrations.linearAgentStatus,
    queryFn: () => api.integrations.getLinearAgentStatus(),
    staleTime: 60_000,
  });
  const { data: mappingsResp, isLoading: mappingsLoading } = useQuery({
    queryKey: queryKeys.integrations.linearAgentMappings,
    queryFn: () => api.integrations.listLinearAgentMappings(),
    staleTime: 60_000,
  });
  const { data: repositoriesResp } = useQuery({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
    enabled: !repositoriesOverride,
    staleTime: 60_000,
  });

  const repositories = (repositoriesOverride ?? repositoriesResp?.data ?? []).filter((repo) => repo.status === "active");
  const status = statusResp?.data;
  const availableTeams = status?.available_teams ?? [];
  const mappings = mappingsResp?.data ?? [];

  const updateSettings = useMutation({
    mutationFn: (body: { default_repo_id: string | null }) => api.integrations.updateLinearAgentSettings(body),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.integrations.linearAgentStatus, exact: true }),
  });
  const upsertMapping = useMutation({
    mutationFn: (body: { linear_team_id: string; linear_project_id?: string; repository_id: string }) =>
      api.integrations.upsertLinearAgentMapping(body),
    onSuccess: () => {
      setTeamID("");
      setProjectID("");
      setMappingRepoID("");
      queryClient.invalidateQueries({ queryKey: queryKeys.integrations.linearAgentMappings });
    },
  });
  const deleteMapping = useMutation({
    mutationFn: (id: string) => api.integrations.deleteLinearAgentMapping(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.integrations.linearAgentMappings }),
  });

  const defaultRepoValue = status?.default_repo_id ?? NO_DEFAULT_REPO_VALUE;
  const canAddMapping = teamID.trim() !== "" && mappingRepoID !== "";

  return (
    <div className="mt-3 space-y-4 rounded-md border border-border px-3 py-3">
      <div>
        <div className="text-sm font-medium">Linear agent routing</div>
      </div>
      <div className="space-y-4">
        {statusLoading ? (
          <p className="text-sm text-muted-foreground">Loading Linear agent settings...</p>
        ) : (
          <div className="grid gap-2">
            <Label htmlFor="linear-default-repo">Default repository</Label>
            <Select
              value={defaultRepoValue}
              onValueChange={(value) =>
                updateSettings.mutate({ default_repo_id: value === NO_DEFAULT_REPO_VALUE ? null : value })
              }
              disabled={repositories.length === 0 || updateSettings.isPending}
            >
              <SelectTrigger id="linear-default-repo" aria-label="Default repository">
                <SelectValue placeholder="Choose a default repository" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NO_DEFAULT_REPO_VALUE}>No default repository</SelectItem>
                {repositories.map((repo) => (
                  <SelectItem key={repo.id} value={repo.id}>{repo.full_name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            {repositories.length === 0 ? (
              <p className="text-xs text-muted-foreground">Connect a GitHub repository before routing Linear agent work.</p>
            ) : null}
            {!status?.agent_scopes_granted ? (
              <p className="text-xs text-muted-foreground">Reconnect Linear to grant the agent assignment and mention scopes.</p>
            ) : null}
          </div>
        )}

        <div className="space-y-2">
          <div className="text-sm font-medium">Team overrides</div>
          {mappingsLoading ? (
            <p className="text-sm text-muted-foreground">Loading team mappings...</p>
          ) : mappings.length === 0 ? (
            <p className="text-sm text-muted-foreground">No team-specific overrides yet.</p>
          ) : (
            <div className="space-y-2">
              {mappings.map((mapping: LinearTeamRepoMapping) => (
                <div key={mapping.id} className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2">
                  <div className="min-w-0">
                    <div className="truncate text-sm font-medium">{linearTeamMappingLabel(availableTeams, mapping.linear_team_id)}</div>
                    <div className="text-xs text-muted-foreground">
                      {mapping.linear_project_id ? `Project ${mapping.linear_project_id} -> ` : ""}{repoName(repositories, mapping.repository_id)}
                    </div>
                  </div>
                  <Button
                    type="button"
                    size="icon"
                    variant="ghost"
                    title="Remove mapping"
                    disabled={deleteMapping.isPending}
                    onClick={() => deleteMapping.mutate(mapping.id)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              ))}
            </div>
          )}
        </div>

        <div className="grid gap-2 border-t border-border pt-4">
          <div className="grid gap-2 sm:grid-cols-[1fr_1fr_1.3fr_auto]">
            {availableTeams.length > 0 ? (
              <Select value={teamID} onValueChange={setTeamID}>
                <SelectTrigger aria-label="Linear team">
                  <SelectValue placeholder="Linear team" />
                </SelectTrigger>
                <SelectContent>
                  {availableTeams.map((team) => (
                    <SelectItem key={`${team.integration_id}:${team.team_key}`} value={team.team_key}>
                      {linearTeamLabel(team)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : (
              <Input
                aria-label="Linear team key"
                placeholder="Linear team key"
                value={teamID}
                onChange={(event) => setTeamID(event.target.value)}
              />
            )}
            <Input
              aria-label="Linear project ID"
              placeholder="Project ID (optional)"
              value={projectID}
              onChange={(event) => setProjectID(event.target.value)}
            />
            <Select value={mappingRepoID} onValueChange={setMappingRepoID} disabled={repositories.length === 0}>
              <SelectTrigger aria-label="Override repository">
                <SelectValue placeholder="Repository" />
              </SelectTrigger>
              <SelectContent>
                {repositories.map((repo) => (
                  <SelectItem key={repo.id} value={repo.id}>{repo.full_name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button
              type="button"
              disabled={!canAddMapping || upsertMapping.isPending}
              loading={upsertMapping.isPending}
              onClick={() => upsertMapping.mutate({
                linear_team_id: teamID.trim(),
                linear_project_id: projectID.trim() || undefined,
                repository_id: mappingRepoID,
              })}
            >
              Add
            </Button>
          </div>
          {updateSettings.isError || upsertMapping.isError || deleteMapping.isError ? (
            <p className="text-xs text-destructive">Failed to update Linear agent routing.</p>
          ) : null}
        </div>
      </div>
    </div>
  );
}

function LinearRoutingSummary({ repositories }: { repositories: Repository[] }) {
  const { data: statusResp, isLoading: statusLoading } = useQuery({
    queryKey: queryKeys.integrations.linearAgentStatus,
    queryFn: () => api.integrations.getLinearAgentStatus(),
    staleTime: 60_000,
  });
  const { data: mappingsResp, isLoading: mappingsLoading } = useQuery({
    queryKey: queryKeys.integrations.linearAgentMappings,
    queryFn: () => api.integrations.listLinearAgentMappings(),
    staleTime: 60_000,
  });
  if (statusLoading || mappingsLoading) {
    return <p className="mt-1.5 text-xs text-muted-foreground">Loading routing summary...</p>;
  }

  const defaultRepo = repoName(repositories, statusResp?.data?.default_repo_id);
  const hasDefault = Boolean(statusResp?.data?.default_repo_id);
  const mappings = mappingsResp?.data ?? [];
  const overrideLabel = `${mappings.length} team override${mappings.length === 1 ? "" : "s"}`;
  return (
    <p className="mt-1.5 text-xs text-muted-foreground">
      {hasDefault ? `Default repo: ${defaultRepo}` : "No default repo"} · {overrideLabel}
    </p>
  );
}

function IntegrationDangerZone({
  provider,
  name,
  disconnecting,
  onDisconnect,
}: {
  provider: IntegrationKey;
  name: string;
  disconnecting: boolean;
  onDisconnect: (provider: IntegrationKey) => void;
}) {
  const [confirmOpen, setConfirmOpen] = useState(false);
  return (
    <div className="space-y-3 rounded-md border border-destructive/25 bg-destructive/5 p-3">
      <div>
        <h3 className="text-sm font-medium text-destructive">Danger zone</h3>
        <p className="mt-1 text-sm text-muted-foreground">
          Disconnect {name} from this organization. Existing synced records remain visible where applicable.
        </p>
      </div>
      <Button
        size="sm"
        variant="destructive"
        loading={disconnecting}
        disabled={disconnecting}
        onClick={() => setConfirmOpen(true)}
      >
        Disconnect {name}
      </Button>
      <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Disconnect {name}</AlertDialogTitle>
            <AlertDialogDescription>
              This will stop {name} sync and automation for this organization.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              disabled={disconnecting}
              onClick={() => {
                setConfirmOpen(false);
                onDisconnect(provider);
              }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Disconnect
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function IntegrationDetailSheet({
  provider,
  open,
  onOpenChange,
  connected,
  repositories,
  githubInstallationId,
  onDisconnect,
  disconnectingProvider,
  onDisconnectRepo,
  onReconnectRepo,
  pendingRepoID,
  onSyncRepos,
  isSyncingRepos,
  onReplaceNotionToken,
  onReplaceCircleCIToken,
  onReplaceMezmoCredentials,
  mezmoDataset,
  mezmoBaseURL,
}: {
  provider: IntegrationKey | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  connected: Partial<Record<IntegrationKey, boolean>>;
  repositories: Repository[];
  githubInstallationId?: number;
  onDisconnect: (provider: IntegrationKey) => void;
  disconnectingProvider?: IntegrationKey | null;
  onDisconnectRepo: (repoID: string) => void;
  onReconnectRepo: (repoID: string) => void;
  pendingRepoID?: string | null;
  onSyncRepos: () => void;
  isSyncingRepos: boolean;
  onReplaceNotionToken: () => void;
  onReplaceCircleCIToken: () => void;
  onReplaceMezmoCredentials: () => void;
  mezmoDataset?: string;
  mezmoBaseURL?: string;
}) {
  if (!provider) return null;
  const meta = getIntegrationByKey(provider);
  const isConnected = Boolean(connected[provider]);

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{meta.name}</SheetTitle>
          <SheetDescription>{meta.description}</SheetDescription>
        </SheetHeader>
        <div className="mt-6 space-y-6">
          <div className="rounded-md border border-border p-3">
            <div className="text-xs font-medium uppercase text-muted-foreground">Status</div>
            <div className="mt-2 flex items-center gap-2">
              <Badge variant={isConnected ? "secondary" : "outline"}>
                {isConnected ? "Connected" : "Not connected"}
              </Badge>
            </div>
          </div>

          {provider === "github" ? (
            <GitHubRepositoryClaims
              installationId={githubInstallationId}
              enabled={isConnected}
              repositories={repositories}
              onDisconnectRepo={onDisconnectRepo}
              onReconnectRepo={onReconnectRepo}
              pendingRepoID={pendingRepoID}
              onSyncRepos={onSyncRepos}
              isSyncing={isSyncingRepos}
            />
          ) : null}
          {provider === "linear" ? <LinearAgentRoutingSettings repositoriesOverride={repositories} /> : null}
          {provider === "slack" ? <SlackChannelPicker /> : null}
          {provider === "sentry" ? (
            <div className="space-y-2">
              <h3 className="text-sm font-medium">Connection scope</h3>
              <p className="text-sm text-muted-foreground">
                Sentry is connected for error ingestion. There are no project or environment filters to configure yet.
              </p>
            </div>
          ) : null}
          {provider === "notion" ? (
            <div className="space-y-3">
              <h3 className="text-sm font-medium">Connection settings</h3>
              <p className="text-sm text-muted-foreground">
                Replace the internal integration token when workspace access changes.
              </p>
              <Button size="sm" variant="outline" onClick={onReplaceNotionToken}>Replace token</Button>
            </div>
          ) : null}
          {provider === "circleci" ? (
            <div className="space-y-3">
              <h3 className="text-sm font-medium">Connection settings</h3>
              <p className="text-sm text-muted-foreground">
                Replace the CircleCI API token or project slug used for flaky-test context.
              </p>
              <Button size="sm" variant="outline" onClick={onReplaceCircleCIToken}>Replace credentials</Button>
            </div>
          ) : null}
          {provider === "mezmo" ? (
            <div className="space-y-3">
              <h3 className="text-sm font-medium">Connection settings</h3>
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
                <dt className="text-muted-foreground">Base URL</dt>
                <dd className="truncate">{mezmoBaseURL || "https://api.mezmo.com (default)"}</dd>
                <dt className="text-muted-foreground">Dataset</dt>
                <dd className="truncate">{mezmoDataset || "All datasets"}</dd>
              </dl>
              <p className="text-sm text-muted-foreground">
                Replace the Mezmo service key, base URL, or dataset used for production log queries.
              </p>
              <Button size="sm" variant="outline" onClick={onReplaceMezmoCredentials}>Replace credentials</Button>
            </div>
          ) : null}

          {isConnected ? (
            <>
              <Separator />
              <IntegrationDangerZone
                provider={provider}
                name={meta.name}
                disconnecting={disconnectingProvider === provider}
                onDisconnect={onDisconnect}
              />
            </>
          ) : null}
        </div>
      </SheetContent>
    </Sheet>
  );
}

type TokenDialogField = {
  id: string;
  label: string;
  placeholder?: string;
  type?: "text" | "password";
  // When true, the field may be left blank and does not gate the submit
  // button. Used for provider settings like Mezmo's base URL and dataset that
  // fall back to sensible defaults server-side.
  optional?: boolean;
  help?: ReactNode;
  tooltip?: {
    ariaLabel: string;
    content: ReactNode;
  };
};

type TokenDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: ReactNode;
  fields: TokenDialogField[];
  submitting: boolean;
  error: string | null;
  onSubmit: (values: Record<string, string>) => void;
};

// Generic paste-the-credential dialog. Shared by Notion (token only) and
// CircleCI (token + project slug); the next provider that needs a manual
// credential drops in by adding a field to its `fields` array.
function TokenDialog({ open, onOpenChange, title, description, fields, submitting, error, onSubmit }: TokenDialogProps) {
  const [values, setValues] = useState<Record<string, string>>({});

  const handleOpenChange = (next: boolean) => {
    if (!next) setValues({});
    onOpenChange(next);
  };
  const trimmedValues = Object.fromEntries(fields.map((f) => [f.id, (values[f.id] ?? "").trim()]));
  const ready = fields.every((f) => f.optional || trimmedValues[f.id] !== "");

  return (
    <AlertDialog open={open} onOpenChange={handleOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <div className="space-y-3">
          {fields.map((f) => (
            <div key={f.id} className="grid gap-1.5">
              <div className="flex items-center gap-1.5">
                <Label htmlFor={f.id}>{f.label}</Label>
                {f.tooltip ? (
                  <TooltipProvider delayDuration={150}>
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon"
                          className="h-5 w-5 rounded-full text-muted-foreground hover:text-foreground"
                          aria-label={f.tooltip.ariaLabel}
                        >
                          <CircleHelp className="h-3.5 w-3.5" />
                        </Button>
                      </TooltipTrigger>
                      <TooltipContent side="top" sideOffset={6} className="max-w-80">
                        {f.tooltip.content}
                      </TooltipContent>
                    </Tooltip>
                  </TooltipProvider>
                ) : null}
              </div>
              <Input
                id={f.id}
                type={f.type ?? "password"}
                placeholder={f.placeholder}
                value={values[f.id] ?? ""}
                onChange={(e) => setValues((prev) => ({ ...prev, [f.id]: e.target.value }))}
              />
              {f.help}
            </div>
          ))}
          {error && <p className="text-xs text-destructive">{error}</p>}
        </div>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <Button
            onClick={() => onSubmit(trimmedValues)}
            disabled={!ready || submitting}
            loading={submitting}
          >
            Connect
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

export default function IntegrationsPage() {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";
  const [selectedIntegration, setSelectedIntegration] = useState<IntegrationKey | null>(null);
  const { data: integrationsResp } = useQuery({
    queryKey: ["integrations"],
    queryFn: () => api.integrations.list(),
  });
  const { data: repositoriesResp } = useQuery({
    queryKey: ["repositories", "integrations", "include-disconnected"],
    queryFn: () => api.repositories.list({ includeDisconnected: true }),
  });
  const disconnectMutation = useDisconnectIntegration();
  const repositoryStatusMutation = useMutation({
    mutationFn: ({ repoID, action }: { repoID: string; action: "disconnect" | "reconnect" }) =>
      action === "disconnect" ? api.repositories.disconnect(repoID) : api.repositories.reconnect(repoID),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["repositories", "integrations", "include-disconnected"] });
      queryClient.invalidateQueries({ queryKey: queryKeys.repositories.all });
    },
  });
  const syncReposMutation = useMutation({
    mutationFn: () => api.integrations.syncGitHub(),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["repositories", "integrations", "include-disconnected"] });
      queryClient.invalidateQueries({ queryKey: queryKeys.repositories.all });
      queryClient.invalidateQueries({ queryKey: queryKeys.integrations.all });
      queryClient.invalidateQueries({ queryKey: queryKeys.integrations.githubRepositories(githubIntegration?.github_installation_id) });
    },
  });

  const [notionDialogOpen, setNotionDialogOpen] = useState(false);
  const [notionError, setNotionError] = useState<string | null>(null);
  const notionConnectMutation = useMutation({
    mutationFn: (token: string) => api.integrations.connectNotion(token),
    onSuccess: () => {
      setNotionDialogOpen(false);
      setNotionError(null);
      queryClient.invalidateQueries({ queryKey: ["integrations"] });
    },
    onError: (err: Error) => {
      setNotionError(err.message || "Failed to connect Notion. Check your token.");
    },
  });

  const [circleciDialogOpen, setCircleciDialogOpen] = useState(false);
  const [circleciError, setCircleciError] = useState<string | null>(null);
  const circleciConnectMutation = useMutation({
    mutationFn: ({ token, projectSlug }: { token: string; projectSlug: string }) =>
      api.integrations.connectCircleCI(token, projectSlug),
    onSuccess: () => {
      setCircleciDialogOpen(false);
      setCircleciError(null);
      queryClient.invalidateQueries({ queryKey: ["integrations"] });
    },
    onError: (err: Error) => {
      setCircleciError(err.message || "Failed to connect CircleCI. Check your token and project slug.");
    },
  });

  const [mezmoDialogOpen, setMezmoDialogOpen] = useState(false);
  const [mezmoError, setMezmoError] = useState<string | null>(null);
  const mezmoConnectMutation = useMutation({
    mutationFn: ({ apiKey, baseUrl, dataset }: { apiKey: string; baseUrl: string; dataset: string }) =>
      api.integrations.connectMezmo(apiKey, baseUrl, dataset),
    onSuccess: () => {
      setMezmoDialogOpen(false);
      setMezmoError(null);
      queryClient.invalidateQueries({ queryKey: ["integrations"] });
    },
    onError: (err: Error) => {
      setMezmoError(err.message || "Failed to connect Mezmo. Check your service key.");
    },
  });

  const githubIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "github" && integration.status === "active"
  );
  const githubConnected = Boolean(githubIntegration);
  const sentryIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "sentry" && integration.status === "active"
  );
  const linearIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "linear" && integration.status === "active"
  );
  // The auth-error banner needs to fire even when status !== "active" — the
  // worker flips Linear to "error" on a 401, which would otherwise look
  // identical to "never connected" through the linearConnected flag below
  // and the user would never learn their token expired.
  const linearAuthErrorRow = integrationsResp?.data?.find(
    (integration) => integration.provider === "linear" && integration.auth_error
  );
  const linearAuthError = linearAuthErrorRow?.auth_error ?? null;
  const slackIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "slack" && integration.status === "active"
  );
  const notionIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "notion" && integration.status === "active"
  );
  const circleciIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "circleci" && integration.status === "active"
  );
  const mezmoIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "mezmo" && integration.status === "active"
  );
  const repositories = repositoriesResp?.data ?? [];
  const activeRepositories = repositories.filter((repo) => repo.status === "active");
  const connected = {
    github: githubConnected,
    sentry: Boolean(sentryIntegration),
    linear: Boolean(linearIntegration),
    slack: Boolean(slackIntegration),
    notion: Boolean(notionIntegration),
    circleci: Boolean(circleciIntegration),
    mezmo: Boolean(mezmoIntegration),
  } satisfies Partial<Record<IntegrationKey, boolean>>;

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="Integrations"
          description="Connect external services to your organization."
        />
      {!isAdmin && (
        <div className="rounded-md bg-muted px-3 py-2 text-xs text-muted-foreground">
          Only admins can connect or disconnect integrations.
        </div>
      )}
      <AllIntegrationCards
        githubConnected={githubConnected}
        githubRepos={activeRepositories.map((repo) => ({ id: repo.id, full_name: repo.full_name, status: repo.status }))}
        githubSummary={githubConnected ? <GitHubCardSummary repositories={repositories} /> : undefined}
        sentryConnected={Boolean(sentryIntegration)}
        linearConnected={Boolean(linearIntegration)}
        linearLoading={false}
        linearAuthError={linearAuthError}
        slackConnected={Boolean(slackIntegration)}
        notionConnected={Boolean(notionIntegration)}
        notionLoading={notionConnectMutation.isPending}
        circleciConnected={Boolean(circleciIntegration)}
        circleciLoading={circleciConnectMutation.isPending}
        mezmoConnected={Boolean(mezmoIntegration)}
        mezmoLoading={mezmoConnectMutation.isPending}
        onConnectGitHub={() => api.integrations.loginGitHub()}
        onConnectSentry={() => api.integrations.loginSentry()}
        onConnectLinear={() => api.integrations.loginLinear()}
        onConnectSlack={() => api.integrations.loginSlack()}
        onConnectNotion={() => {
          setNotionError(null);
          setNotionDialogOpen(true);
        }}
        onConnectCircleCI={() => {
          setCircleciError(null);
          setCircleciDialogOpen(true);
        }}
        onConnectMezmo={() => {
          setMezmoError(null);
          setMezmoDialogOpen(true);
        }}
        onManageGitHub={isAdmin ? () => setSelectedIntegration("github") : undefined}
        onManageIntegration={isAdmin ? (provider) => setSelectedIntegration(provider) : undefined}
        summaries={{
          linear: linearIntegration ? <LinearRoutingSummary repositories={repositories} /> : undefined,
          slack: slackIntegration ? <SlackCardSummary /> : undefined,
          notion: notionIntegration ? (
            <p className="mt-1.5 text-xs text-muted-foreground">
              {notionIntegration.notion_workspace_name ? `Workspace: ${notionIntegration.notion_workspace_name}` : "Workspace knowledge sync is enabled"}
            </p>
          ) : undefined,
          circleci: circleciIntegration ? (
            <p className="mt-1.5 text-xs text-muted-foreground">
              {circleciIntegration.circleci_project_slug ? `Project: ${circleciIntegration.circleci_project_slug}` : "Flaky-test context is enabled"}
            </p>
          ) : undefined,
          mezmo: mezmoIntegration ? (
            <p className="mt-1.5 text-xs text-muted-foreground">
              {mezmoIntegration.mezmo_dataset ? `Dataset: ${mezmoIntegration.mezmo_dataset}` : "Production log queries are enabled"}
            </p>
          ) : undefined,
        }}
        onDisconnect={(provider) => disconnectMutation.mutate(provider)}
        disconnectingProvider={disconnectMutation.isPending ? disconnectMutation.variables : null}
        disconnectErrorProvider={disconnectMutation.isError ? disconnectMutation.variables ?? null : null}
        disconnectError={disconnectMutation.isError ? "Failed to disconnect." : null}
        readOnly={!isAdmin}
      />
      </div>

      <IntegrationDetailSheet
        provider={selectedIntegration}
        open={selectedIntegration !== null}
        onOpenChange={(open) => {
          if (!open) setSelectedIntegration(null);
        }}
        connected={connected}
        repositories={repositories}
        githubInstallationId={githubIntegration?.github_installation_id}
        onDisconnect={(provider) => disconnectMutation.mutate(provider, { onSuccess: () => setSelectedIntegration(null) })}
        disconnectingProvider={disconnectMutation.isPending ? disconnectMutation.variables : null}
        onDisconnectRepo={(repoID) => repositoryStatusMutation.mutate({ repoID, action: "disconnect" })}
        onReconnectRepo={(repoID) => repositoryStatusMutation.mutate({ repoID, action: "reconnect" })}
        pendingRepoID={repositoryStatusMutation.isPending ? repositoryStatusMutation.variables?.repoID : null}
        onSyncRepos={() => syncReposMutation.mutate()}
        isSyncingRepos={syncReposMutation.isPending}
        onReplaceNotionToken={() => {
          setNotionError(null);
          setNotionDialogOpen(true);
        }}
        onReplaceCircleCIToken={() => {
          setCircleciError(null);
          setCircleciDialogOpen(true);
        }}
        onReplaceMezmoCredentials={() => {
          setMezmoError(null);
          setMezmoDialogOpen(true);
        }}
        mezmoDataset={mezmoIntegration?.mezmo_dataset}
        mezmoBaseURL={mezmoIntegration?.mezmo_base_url}
      />

      <TokenDialog
        open={notionDialogOpen}
        onOpenChange={setNotionDialogOpen}
        title="Connect Notion"
        description={
          <>
            Enter your Notion internal integration token. You can create one at{" "}
            <a
              href="https://www.notion.so/my-integrations"
              target="_blank"
              rel="noopener noreferrer"
              className="underline"
            >
              notion.so/my-integrations
            </a>
            . Make sure to share the pages you want accessible with the integration.
          </>
        }
        fields={[{ id: "token", label: "Integration Token", placeholder: "ntn_..." }]}
        submitting={notionConnectMutation.isPending}
        error={notionError}
        onSubmit={(values) => notionConnectMutation.mutate(values.token)}
      />

      <TokenDialog
        open={circleciDialogOpen}
        onOpenChange={setCircleciDialogOpen}
        title="Connect CircleCI"
        description={
          <>
            Paste a CircleCI personal API token and the VCS-prefixed project slug
            (for example <code>gh/your-org/your-repo</code>). Create a token at{" "}
            <a
              href="https://app.circleci.com/settings/user/tokens"
              target="_blank"
              rel="noopener noreferrer"
              className="underline"
            >
              app.circleci.com/settings/user/tokens
            </a>
            . The token needs read access to the project.
          </>
        }
        fields={[
          { id: "token", label: "Personal API Token", placeholder: "CCI-..." },
          {
            id: "projectSlug",
            label: "Project Slug",
            placeholder: "gh/your-org/your-repo",
            type: "text",
            tooltip: {
              ariaLabel: "Where to find the CircleCI project slug",
              content: "Use the API project slug from CircleCI. OAuth projects usually look like gh/org/repo; GitHub App projects can use a circleci/... slug.",
            },
            help: (
              <div className="rounded-md border border-border bg-muted/50 px-3 py-2">
                <p className="text-xs text-muted-foreground">
                  In CircleCI, open Projects, find your repository, then open Project Settings. Copy the slug from the settings overview.
                </p>
                <Button asChild variant="link" size="sm" className="mt-1 h-auto p-0 text-xs">
                  <a href="https://app.circleci.com/projects" target="_blank" rel="noopener noreferrer">
                    Open CircleCI projects
                    <ExternalLink className="h-3 w-3" />
                  </a>
                </Button>
              </div>
            ),
          },
        ]}
        submitting={circleciConnectMutation.isPending}
        error={circleciError}
        onSubmit={(values) =>
          circleciConnectMutation.mutate({ token: values.token, projectSlug: values.projectSlug })
        }
      />

      <TokenDialog
        open={mezmoDialogOpen}
        onOpenChange={setMezmoDialogOpen}
        title="Connect Mezmo"
        description={
          <>
            Paste a Mezmo service key so agents can query your production logs.
            Create one under{" "}
            <a
              href="https://app.mezmo.com/profile/api"
              target="_blank"
              rel="noopener noreferrer"
              className="underline"
            >
              Organization → API Keys
            </a>
            . Base URL and dataset are optional — leave them blank to use the
            Mezmo defaults.
          </>
        }
        fields={[
          { id: "apiKey", label: "Service Key", placeholder: "Mezmo service key" },
          {
            id: "baseUrl",
            label: "Base URL (optional)",
            placeholder: "https://api.mezmo.com",
            type: "text",
            optional: true,
            tooltip: {
              ariaLabel: "When to set a custom Mezmo base URL",
              content: "Only needed for self-hosted or regional Mezmo deployments. Leave blank to use https://api.mezmo.com.",
            },
          },
          {
            id: "dataset",
            label: "Dataset (optional)",
            placeholder: "e.g. production",
            type: "text",
            optional: true,
          },
        ]}
        submitting={mezmoConnectMutation.isPending}
        error={mezmoError}
        onSubmit={(values) =>
          mezmoConnectMutation.mutate({ apiKey: values.apiKey, baseUrl: values.baseUrl, dataset: values.dataset })
        }
      />
    </PageContainer>
  );
}
