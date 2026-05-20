"use client";

import { useState, type ReactNode } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { ApiError, api } from "@/lib/api";
import { AllIntegrationCards } from "@/components/integration-connection-cards";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
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
import { Badge } from "@/components/ui/badge";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { GitHubRepositoryClaimCandidate, LinearTeamRepoMapping, Repository } from "@/lib/types";

type SlackChannel = { id: string; name: string; selected: boolean };
type SlackChannelsResp = { data: SlackChannel[] } | undefined;
const NO_DEFAULT_REPO_VALUE = "__none__";

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

function GitHubRepositoryClaims({
  installationId,
  enabled,
}: {
  installationId?: number;
  enabled: boolean;
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
      queryClient.invalidateQueries({ queryKey: queryKeys.integrations.githubRepositories(installationId) });
    },
  });

  if (!enabled || !installationId) return null;

  const repos = data?.data ?? [];
  const actionable = repos.filter((repo) =>
    repo.status === "unclaimed" || repo.status === "disconnected_in_current_org" || (repo.status === "owned_by_other_org" && repo.can_transfer)
  );
  const claimError = claimMutation.error;
  const needsGitHubUserAuth = claimError instanceof ApiError && claimError.code === "GITHUB_USER_AUTH_REQUIRED";

  return (
    <>
      <div className="mt-3">
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
            {repos.map((repo) => {
              const transfer = repo.status === "owned_by_other_org";
              const canClaim = repo.status === "unclaimed" || repo.status === "disconnected_in_current_org" || (transfer && repo.can_transfer);
              const pending = claimMutation.isPending && claimMutation.variables?.githubId === repo.github_id;
              return (
                <div key={repo.github_id} className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2">
                  <div className="min-w-0">
                    <div className="truncate text-sm font-medium">{repo.full_name}</div>
                    <div className="mt-1 flex items-center gap-2">
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
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">Slack Channels</CardTitle>
          <CardDescription>Loading channels...</CardDescription>
        </CardHeader>
      </Card>
    );
  }

  const toggle = (id: string) => {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    save(Array.from(next));
  };

  return (
    <Card>
      <CardHeader>
        <div className="flex items-start justify-between gap-3">
          <div>
            <CardTitle className="text-sm">Monitored Slack Channels</CardTitle>
            <CardDescription>
              Select which channels the PM agent should monitor for actionable conversations.
            </CardDescription>
          </div>
          <AutosaveIndicator status={status} />
        </div>
      </CardHeader>
      <CardContent>
        {channels.length === 0 ? (
          <p className="text-sm text-muted-foreground">No channels found.</p>
        ) : (
          <div className="space-y-3">
            <div className="grid gap-2 max-h-64 overflow-y-auto">
              {channels.map((ch) => (
                <label
                  key={ch.id}
                  className="flex items-center gap-2 rounded-md border px-3 py-2 cursor-pointer hover:bg-muted/50 transition-colors"
                >
                  <input
                    type="checkbox"
                    checked={selected.has(ch.id)}
                    onChange={() => toggle(ch.id)}
                    className="h-4 w-4 rounded border-input"
                  />
                  <span className="text-sm font-medium">#{ch.name}</span>
                </label>
              ))}
            </div>
            <p className="text-xs text-muted-foreground">
              {selected.size} channel{selected.size !== 1 ? "s" : ""} selected
            </p>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function repoName(repositories: Repository[], repoID?: string): string {
  return repositories.find((repo) => repo.id === repoID)?.full_name ?? repoID ?? "Unknown repository";
}

function LinearAgentRoutingSettings() {
  const queryClient = useQueryClient();
  const [teamID, setTeamID] = useState("");
  const [projectID, setProjectID] = useState("");
  const [mappingRepoID, setMappingRepoID] = useState("");

  const { data: statusResp, isLoading: statusLoading } = useQuery({
    queryKey: queryKeys.integrations.linearAgentStatus,
    queryFn: () => api.integrations.getLinearAgentStatus(),
  });
  const { data: mappingsResp, isLoading: mappingsLoading } = useQuery({
    queryKey: queryKeys.integrations.linearAgentMappings,
    queryFn: () => api.integrations.listLinearAgentMappings(),
  });
  const { data: repositoriesResp } = useQuery({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
  });

  const repositories = (repositoriesResp?.data ?? []).filter((repo) => repo.status === "active");
  const status = statusResp?.data;
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
                    <div className="truncate text-sm font-medium">{mapping.linear_team_id}</div>
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
            <Input
              aria-label="Linear team ID"
              placeholder="Linear team ID"
              value={teamID}
              onChange={(event) => setTeamID(event.target.value)}
            />
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

type TokenDialogField = {
  id: string;
  label: string;
  placeholder?: string;
  type?: "text" | "password";
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
  const ready = fields.every((f) => trimmedValues[f.id] !== "");

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
              <Label htmlFor={f.id}>{f.label}</Label>
              <Input
                id={f.id}
                type={f.type ?? "password"}
                placeholder={f.placeholder}
                value={values[f.id] ?? ""}
                onChange={(e) => setValues((prev) => ({ ...prev, [f.id]: e.target.value }))}
              />
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
  const { data: integrationsResp } = useQuery({
    queryKey: ["integrations"],
    queryFn: () => api.integrations.list(),
  });
  const disconnectMutation = useDisconnectIntegration();

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
        githubExtra={
          isAdmin && githubConnected ? (
            <GitHubRepositoryClaims
              installationId={githubIntegration?.github_installation_id}
              enabled={githubConnected}
            />
          ) : undefined
        }
        sentryConnected={Boolean(sentryIntegration)}
        linearConnected={Boolean(linearIntegration)}
        linearExtra={linearIntegration && isAdmin ? <LinearAgentRoutingSettings /> : undefined}
        linearLoading={false}
        linearAuthError={linearAuthError}
        slackConnected={Boolean(slackIntegration)}
        notionConnected={Boolean(notionIntegration)}
        notionLoading={notionConnectMutation.isPending}
        circleciConnected={Boolean(circleciIntegration)}
        circleciLoading={circleciConnectMutation.isPending}
        onConnectGitHub={() => api.integrations.loginGitHub()}
        onConnectSentry={() => api.auth.loginSentry()}
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
        onDisconnect={(provider) => disconnectMutation.mutate(provider)}
        disconnectingProvider={disconnectMutation.isPending ? disconnectMutation.variables : null}
        disconnectErrorProvider={disconnectMutation.isError ? disconnectMutation.variables ?? null : null}
        disconnectError={disconnectMutation.isError ? "Failed to disconnect." : null}
        readOnly={!isAdmin}
      />
      {slackIntegration && isAdmin && <SlackChannelPicker />}
      </div>

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
          { id: "projectSlug", label: "Project Slug", placeholder: "gh/your-org/your-repo", type: "text" },
        ]}
        submitting={circleciConnectMutation.isPending}
        error={circleciError}
        onSubmit={(values) =>
          circleciConnectMutation.mutate({ token: values.token, projectSlug: values.projectSlug })
        }
      />
    </PageContainer>
  );
}
