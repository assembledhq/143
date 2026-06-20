"use client";

import { useState, type ReactNode } from "react";
import Link from "next/link";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { CircleHelp, ExternalLink, RefreshCw, Trash2 } from "lucide-react";
import { ApiError, api } from "@/lib/api";
import { AllIntegrationCards } from "@/components/integration-connection-cards";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Checkbox } from "@/components/ui/checkbox";
import { ErrorNotice, ErrorText } from "@/components/ui/error-notice";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
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
import type {
  GitHubRepositoryClaimCandidate,
  LinearTeamKey,
  LinearTeamRepoMapping,
  PagerDutyHealth,
  PagerDutyIncident,
  PagerDutyIntegration,
  PagerDutyServiceRepoMapping,
  Repository,
  SlackBotSettingsUpdate,
  SlackChannel,
  SlackChannelAction,
  SlackChannelSettingsUpdate,
  SlackNotificationPreset,
  SlackResponseVisibility,
  SlackRoutingMode,
  SlackUserLinkUpsert,
  User,
} from "@/lib/types";
import { getIntegrationByKey, type IntegrationKey } from "@/lib/integrations";

type SlackChannelsResp = { data: SlackChannel[] } | undefined;
const NO_DEFAULT_REPO_VALUE = "__none__";
const CARD_PILL_LIMIT = 3;
const SLACK_ACTIONS: Array<{ value: SlackChannelAction; label: string }> = [
  { value: "session", label: "Sessions" },
  { value: "preview", label: "Previews" },
  { value: "pr_request", label: "PRs" },
  { value: "human_input", label: "Human input" },
];
const SLACK_NOTIFICATION_EVENTS = [
  { value: "session.completed", label: "Session completed" },
  { value: "session.failed", label: "Session failed" },
  { value: "human_input.requested", label: "Human input requested" },
  { value: "automation.run.completed", label: "Automation completed" },
  { value: "automation.run.failed", label: "Automation failed" },
  { value: "automation.run.failure_streak", label: "Automation failure streak" },
  { value: "pr.opened", label: "PR opened" },
  { value: "preview.ready", label: "Preview ready" },
  { value: "preview.failed", label: "Preview failed" },
  { value: "preview.stale", label: "Preview stale" },
  { value: "preview.*", label: "All preview events" },
] as const;

// Coalesce multi-toggle bursts: the later selection wins. Hoisted so every
// `useAutosave` caller sharing `queryKeys.integrations.slackChannels` passes
// the same referential identity - `useAutosave` throws in dev when two
// callers register different coalesce fns against the same queryKey.
const coalesceSlackChannels = (_a: string[], b: string[]): string[] => b;

function slackRoutingLabel(value?: SlackRoutingMode): string {
  switch (value) {
    case "answer_only":
      return "Answer only";
    case "start_work":
      return "Start work";
    case "auto":
    default:
      return "Auto";
  }
}

function slackVisibilityLabel(value?: SlackResponseVisibility): string {
  return value === "dm" ? "DM requester" : "Thread";
}

function slackPresetLabel(value?: SlackNotificationPreset): string {
  switch (value) {
    case "quiet":
      return "Quiet";
    case "verbose":
      return "Verbose";
    case "custom":
      return "Custom";
    case "balanced":
    default:
      return "Balanced";
  }
}

function slackNotificationEvents(settings?: { notification_subscriptions?: Record<string, unknown> }): string[] {
  const events = settings?.notification_subscriptions?.events;
  return Array.isArray(events) ? events.filter((event): event is string => typeof event === "string") : [];
}

function slackNotificationAutomations(settings?: { notification_subscriptions?: Record<string, unknown> }): string[] {
  const automations = settings?.notification_subscriptions?.automations;
  return Array.isArray(automations) ? automations.filter((automation): automation is string => typeof automation === "string") : [];
}

function slackNotificationSlackUsers(settings?: { notification_subscriptions?: Record<string, unknown> }): string[] {
  const slackUserIDs = settings?.notification_subscriptions?.slack_user_ids;
  return Array.isArray(slackUserIDs) ? slackUserIDs.filter((userID): userID is string => typeof userID === "string") : [];
}

function splitCommaSeparatedIDs(value: string): string[] {
  return value.split(",").map((item) => item.trim()).filter(Boolean);
}

function slackHealthSymptomLabel(symptom: string): string {
  switch (symptom) {
    case "no_events_observed_check_event_subscriptions_and_signing_secret":
      return "No Slack events observed. Check event subscriptions and signing secret.";
    default:
      return symptom.replaceAll("_", " ");
  }
}

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
          <ErrorText className="text-sm">
            {error instanceof Error ? error.message : "Failed to load GitHub repositories."}
          </ErrorText>
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
              <ErrorNotice
                title={claimError instanceof Error ? claimError.message : "Failed to claim repository."}
                action={
                  needsGitHubUserAuth
                    ? {
                        label: "Connect GitHub account",
                        onClick: () => api.githubStatus.connect(),
                      }
                    : undefined
                }
              />
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

function SlackBotDefaults({ repositories }: { repositories: Repository[] }) {
  const queryClient = useQueryClient();
  const { data: healthResp, isLoading: healthLoading } = useQuery({
    queryKey: queryKeys.integrations.slackHealth,
    queryFn: () => api.integrations.getSlackHealth(),
    staleTime: 60_000,
  });
  const { data: settingsResp, isLoading: settingsLoading } = useQuery({
    queryKey: queryKeys.integrations.slackSettings,
    queryFn: () => api.integrations.getSlackSettings(),
    staleTime: 60_000,
  });
  const updateSettings = useMutation({
    mutationFn: (body: SlackBotSettingsUpdate) => api.integrations.updateSlackSettings(body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.integrations.slackSettings });
      queryClient.invalidateQueries({ queryKey: queryKeys.integrations.slackChannels });
    },
  });

  const settings = settingsResp?.data;
  const health = healthResp?.data;
  const activeRepos = repositories.filter((repo) => repo.status === "active");
  const repoValue = settings?.default_repository_id ?? NO_DEFAULT_REPO_VALUE;
  const actions = settings?.allowed_actions ?? ["session", "preview"];
  const selectedNotificationEvents = slackNotificationEvents(settings);
  const selectedNotificationAutomations = slackNotificationAutomations(settings);
  const selectedNotificationSlackUsers = slackNotificationSlackUsers(settings);
  const showCustomNotificationControls = (settings?.notification_preset ?? "balanced") === "custom";

  const patch = (body: SlackBotSettingsUpdate) => updateSettings.mutate(body);
  const toggleAction = (action: SlackChannelAction) => {
    const next = actions.includes(action) ? actions.filter((item) => item !== action) : [...actions, action];
    patch({ allowed_actions: next.length > 0 ? next : ["session"] });
  };
  const toggleNotificationEvent = (eventName: string) => {
    const next = selectedNotificationEvents.includes(eventName)
      ? selectedNotificationEvents.filter((item) => item !== eventName)
      : [...selectedNotificationEvents, eventName];
    patch({
      notification_preset: "custom",
      notification_subscriptions: {
        events: next,
        automations: selectedNotificationAutomations,
        slack_user_ids: selectedNotificationSlackUsers,
      },
    });
  };
  const patchNotificationSubscriptions = (body: { automations?: string[]; slack_user_ids?: string[] }) => {
    patch({
      notification_preset: "custom",
      notification_subscriptions: {
        events: selectedNotificationEvents,
        automations: body.automations ?? selectedNotificationAutomations,
        slack_user_ids: body.slack_user_ids ?? selectedNotificationSlackUsers,
      },
    });
  };

  return (
    <div className="space-y-4">
      <div className="rounded-md border border-border p-3">
        <div className="flex items-start justify-between gap-3">
          <div>
            <h3 className="text-sm font-medium">Slackbot defaults</h3>
            <p className="mt-1 text-sm text-muted-foreground">
              New channels inherit these defaults unless a channel override is configured.
            </p>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <Badge variant={health?.auth_ok ? "secondary" : "outline"}>
              {healthLoading ? "Checking" : health?.auth_ok ? "Healthy" : "Needs attention"}
            </Badge>
            {health && !health.auth_ok ? (
              <Button size="sm" variant="outline" onClick={() => api.integrations.loginSlack()}>
                Reinstall
              </Button>
            ) : null}
          </div>
        </div>
        {health && health.missing_scopes.length > 0 ? (
          <p className="mt-3 text-sm text-muted-foreground">
            Missing Slack scopes: {health.missing_scopes.join(", ")}
          </p>
        ) : null}
        {health && health.symptoms && health.symptoms.length > 0 ? (
          <div className="mt-3 space-y-1 text-sm text-muted-foreground">
            {health.symptoms.map((symptom) => (
              <p key={symptom}>{slackHealthSymptomLabel(symptom)}</p>
            ))}
          </div>
        ) : null}
      </div>

      <div className="space-y-4 rounded-md border border-border p-3">
        {settingsLoading ? (
          <p className="text-sm text-muted-foreground">Loading Slackbot defaults...</p>
        ) : (
          <>
            <div className="grid gap-2">
              <Label htmlFor="slack-default-repo">Default repository</Label>
              <Select
                value={repoValue}
                onValueChange={(value) =>
                  patch({ default_repository_id: value === NO_DEFAULT_REPO_VALUE ? null : value })
                }
                disabled={activeRepos.length === 0 || updateSettings.isPending}
              >
                <SelectTrigger id="slack-default-repo" aria-label="Slack default repository">
                  <SelectValue placeholder="Choose a repository" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NO_DEFAULT_REPO_VALUE}>No default repository</SelectItem>
                  {activeRepos.map((repo) => (
                    <SelectItem key={repo.id} value={repo.id}>{repo.full_name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="grid gap-2">
              <Label htmlFor="slack-default-branch">Default branch</Label>
              <Input
                key={settings?.default_branch ?? "unset"}
                id="slack-default-branch"
                defaultValue={settings?.default_branch ?? ""}
                placeholder="main"
                disabled={updateSettings.isPending}
                onBlur={(event) => patch({ default_branch: event.target.value.trim() || null })}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    patch({ default_branch: event.currentTarget.value.trim() || null });
                    event.currentTarget.blur();
                  }
                }}
              />
            </div>

            <div className="grid gap-3 sm:grid-cols-3">
              <div className="grid gap-2">
                <Label>Routing</Label>
                <Select
                  value={settings?.routing_mode ?? "auto"}
                  onValueChange={(value) => patch({ routing_mode: value as SlackRoutingMode })}
                  disabled={updateSettings.isPending}
                >
                  <SelectTrigger aria-label="Slack routing mode">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(["auto", "answer_only", "start_work"] as SlackRoutingMode[]).map((value) => (
                      <SelectItem key={value} value={value}>{slackRoutingLabel(value)}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="grid gap-2">
                <Label>Visibility</Label>
                <Select
                  value={settings?.response_visibility ?? "thread"}
                  onValueChange={(value) => patch({ response_visibility: value as SlackResponseVisibility })}
                  disabled={updateSettings.isPending}
                >
                  <SelectTrigger aria-label="Slack response visibility">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(["thread", "dm"] as SlackResponseVisibility[]).map((value) => (
                      <SelectItem key={value} value={value}>{slackVisibilityLabel(value)}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="grid gap-2">
                <Label>Notifications</Label>
                <Select
                  value={settings?.notification_preset ?? "balanced"}
                  onValueChange={(value) => patch({ notification_preset: value as SlackNotificationPreset })}
                  disabled={updateSettings.isPending}
                >
                  <SelectTrigger aria-label="Slack notification preset">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(["quiet", "balanced", "verbose", "custom"] as SlackNotificationPreset[]).map((value) => (
                      <SelectItem key={value} value={value}>{slackPresetLabel(value)}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>

            <div className="grid gap-2">
              <Label>Allowed actions</Label>
              <div className="flex flex-wrap gap-2">
                {SLACK_ACTIONS.map((action) => (
                  <Button
                    key={action.value}
                    type="button"
                    size="sm"
                    variant={actions.includes(action.value) ? "default" : "outline"}
                    disabled={updateSettings.isPending}
                    onClick={() => toggleAction(action.value)}
                  >
                    {action.label}
                  </Button>
                ))}
              </div>
            </div>

            {showCustomNotificationControls ? (
              <>
                <div className="grid gap-2">
                  <Label>Custom notification events</Label>
                  <div className="grid gap-2 sm:grid-cols-2">
                    {SLACK_NOTIFICATION_EVENTS.map((event) => (
                      <label key={event.value} className="flex items-center gap-2 rounded-md border border-border px-3 py-2 text-sm">
                        <Checkbox
                          aria-label={event.label}
                          checked={selectedNotificationEvents.includes(event.value)}
                          disabled={updateSettings.isPending}
                          onCheckedChange={() => toggleNotificationEvent(event.value)}
                        />
                        <span>{event.label}</span>
                      </label>
                    ))}
                  </div>
                </div>
                <div className="grid gap-3 sm:grid-cols-2">
                  <div className="grid gap-2">
                    <Label htmlFor="slack-notification-automations">Automation IDs</Label>
                    <Input
                      id="slack-notification-automations"
                      key={`automations-${selectedNotificationAutomations.join(",")}`}
                      defaultValue={selectedNotificationAutomations.join(", ")}
                      disabled={updateSettings.isPending}
                      placeholder="uuid-1, uuid-2"
                      onBlur={(event) => patchNotificationSubscriptions({ automations: splitCommaSeparatedIDs(event.currentTarget.value) })}
                    />
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="slack-notification-users">DM Slack user IDs</Label>
                    <Input
                      id="slack-notification-users"
                      key={`users-${selectedNotificationSlackUsers.join(",")}`}
                      defaultValue={selectedNotificationSlackUsers.join(", ")}
                      disabled={updateSettings.isPending}
                      placeholder="U123ABC, U456DEF"
                      onBlur={(event) => patchNotificationSubscriptions({ slack_user_ids: splitCommaSeparatedIDs(event.currentTarget.value) })}
                    />
                  </div>
                </div>
              </>
            ) : null}
          </>
        )}
      </div>
    </div>
  );
}

function SlackChannelPicker() {
  const queryClient = useQueryClient();
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
  const updateChannelSettings = useMutation({
    mutationFn: ({ channel, body }: { channel: SlackChannel; body: SlackChannelSettingsUpdate }) =>
      api.integrations.updateSlackChannelSettings(channel.id, {
        slack_channel_name: channel.name,
        channel_type: channel.type ?? "channel",
        ...body,
      }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.integrations.slackChannels }),
  });

  const channels = channelsResp?.data ?? [];
  const selectedIds = channels.filter((ch) => ch.selected).map((ch) => ch.id);
  const selected = new Set(selectedIds);
  const selectedChannels = channels.filter((ch) => ch.selected);

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
          <h3 className="text-sm font-medium">PM/context monitoring</h3>
          <p className="mt-1 text-sm text-muted-foreground">
            Select notification channels. Interactive bot behavior inherits the Slackbot defaults unless overridden.
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
                    <span className="min-w-0 flex-1 truncate font-medium">#{ch.name}</span>
                    {ch.effective_settings ? (
                      <span className="ml-auto hidden shrink-0 text-xs text-muted-foreground sm:inline">
                        {slackRoutingLabel(ch.effective_settings.routing_mode)} · {slackPresetLabel(ch.effective_settings.notification_preset)}
                      </span>
                    ) : null}
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
      {selectedChannels.length > 0 ? (
        <div className="space-y-3">
          <div>
            <h4 className="text-sm font-medium">Interactive bot channel overrides</h4>
            <p className="mt-1 text-sm text-muted-foreground">
              Leave channels inherited unless they need different routing or notifications.
            </p>
          </div>
          <div className="space-y-2">
            {selectedChannels.map((channel) => (
              <div key={channel.id} className="grid gap-3 rounded-md border border-border p-3 sm:grid-cols-[minmax(0,1fr)_10rem_10rem]">
                <div className="min-w-0">
                  <div className="truncate text-sm font-medium">#{channel.name}</div>
                  <div className="mt-1 text-xs text-muted-foreground">
                    {channel.effective_settings?.has_channel_override ? "Overrides defaults" : "Inherits defaults"}
                  </div>
                </div>
                <Select
                  value={channel.effective_settings?.routing_mode ?? "auto"}
                  disabled={updateChannelSettings.isPending}
                  onValueChange={(value) =>
                    updateChannelSettings.mutate({ channel, body: { routing_mode: value as SlackRoutingMode } })
                  }
                >
                  <SelectTrigger aria-label={`Routing for #${channel.name}`}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(["auto", "answer_only", "start_work"] as SlackRoutingMode[]).map((value) => (
                      <SelectItem key={value} value={value}>{slackRoutingLabel(value)}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Select
                  value={channel.effective_settings?.notification_preset ?? "balanced"}
                  disabled={updateChannelSettings.isPending}
                  onValueChange={(value) =>
                    updateChannelSettings.mutate({ channel, body: { notification_preset: value as SlackNotificationPreset } })
                  }
                >
                  <SelectTrigger aria-label={`Notifications for #${channel.name}`}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(["quiet", "balanced", "verbose", "custom"] as SlackNotificationPreset[]).map((value) => (
                      <SelectItem key={value} value={value}>{slackPresetLabel(value)}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  );
}

function SlackUserLinkingSection() {
  const queryClient = useQueryClient();
  const [selectedUserID, setSelectedUserID] = useState("");
  const [slackUserID, setSlackUserID] = useState("");
  const [slackEmail, setSlackEmail] = useState("");
  const [slackDisplayName, setSlackDisplayName] = useState("");
  const { data: membersResp } = useQuery({
    queryKey: queryKeys.team.members,
    queryFn: () => api.team.listMembers(),
    staleTime: 60_000,
  });
  const { data: linksResp, isLoading } = useQuery({
    queryKey: queryKeys.integrations.slackUserLinks,
    queryFn: () => api.integrations.listSlackUserLinks(),
    staleTime: 30_000,
  });
  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: queryKeys.integrations.slackUserLinks });
  };
  const upsertLink = useMutation({
    mutationFn: (body: SlackUserLinkUpsert) => api.integrations.upsertSlackUserLink(body),
    onSuccess: () => {
      invalidate();
      setSlackUserID("");
      setSlackEmail("");
      setSlackDisplayName("");
    },
  });
  const deleteLink = useMutation({
    mutationFn: (id: string) => api.integrations.deleteSlackUserLink(id),
    onSuccess: invalidate,
  });
  const members = membersResp?.data ?? [];
  const links = linksResp?.data ?? [];
  const selectedUser = members.find((member: User) => member.id === selectedUserID);
  const canAdd = selectedUserID !== "" && slackUserID.trim() !== "" && !upsertLink.isPending;
  const submit = () => {
    if (!canAdd) return;
    upsertLink.mutate({
      user_id: selectedUserID,
      slack_user_id: slackUserID.trim(),
      slack_email: slackEmail.trim() || undefined,
      slack_display_name: slackDisplayName.trim() || selectedUser?.name || undefined,
    });
  };

  return (
    <div className="space-y-3 rounded-md border border-border p-3">
      <h3 className="text-sm font-medium">User linking</h3>
      <p className="mt-1 text-sm text-muted-foreground">
        Slack users can link themselves from Slack App Home. Admins can also map Slack users to 143 members.
      </p>
      <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <div className="grid gap-2">
          <Label>143 user</Label>
          <Select value={selectedUserID} onValueChange={setSelectedUserID}>
            <SelectTrigger aria-label="143 user">
              <SelectValue placeholder="Choose a member" />
            </SelectTrigger>
            <SelectContent>
              {members.map((member: User) => (
                <SelectItem key={member.id} value={member.id}>{member.name || member.email}</SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="grid gap-2">
          <Label htmlFor="slack-user-id">Slack user ID</Label>
          <Input id="slack-user-id" value={slackUserID} onChange={(event) => setSlackUserID(event.target.value)} placeholder="U123ABC" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="slack-email">Slack email</Label>
          <Input id="slack-email" value={slackEmail} onChange={(event) => setSlackEmail(event.target.value)} placeholder="person@example.com" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="slack-display-name">Slack display name</Label>
          <Input id="slack-display-name" value={slackDisplayName} onChange={(event) => setSlackDisplayName(event.target.value)} placeholder="Slack name" />
        </div>
      </div>
      <Button size="sm" onClick={submit} disabled={!canAdd}>Add link</Button>
      <div className="space-y-2">
        {isLoading ? (
          <p className="text-sm text-muted-foreground">Loading linked users...</p>
        ) : links.length === 0 ? (
          <p className="text-sm text-muted-foreground">No Slack users are linked yet.</p>
        ) : (
          links.map((link) => {
            const member = members.find((candidate: User) => candidate.id === link.user_id);
            const label = link.slack_display_name || link.slack_email || link.slack_user_id;
            return (
              <div key={link.id} className="flex items-center justify-between gap-3 rounded-md border border-border p-2">
                <div className="min-w-0">
                  <div className="truncate text-sm font-medium">{label}</div>
                  <div className="truncate text-xs text-muted-foreground">
                    {member ? `${member.name || member.email} · ${link.slack_user_id}` : link.slack_user_id}
                  </div>
                </div>
                <Button
                  size="icon"
                  variant="ghost"
                  aria-label={`Delete Slack link for ${label}`}
                  disabled={deleteLink.isPending}
                  onClick={() => deleteLink.mutate(link.id)}
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              </div>
            );
          })
        )}
      </div>
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
            <ErrorText>Failed to update Linear agent routing.</ErrorText>
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

function PagerDutyCardSummary({ integration }: { integration: PagerDutyIntegration }) {
  const { data: mappingsResp, isLoading } = useQuery({
    queryKey: queryKeys.integrations.pagerDutyMappings(integration.id),
    queryFn: () => api.integrations.listPagerDutyMappings(integration.id),
    enabled: Boolean(integration.id),
    staleTime: 60_000,
  });

  if (isLoading) {
    return <p className="mt-1.5 text-xs text-muted-foreground">Loading routing summary...</p>;
  }

  const mappingCount = mappingsResp?.data?.length ?? 0;
  const account = integration.account_subdomain ? `${integration.account_subdomain}.pagerduty.com` : "PagerDuty";
  return (
    <p className="mt-1.5 text-xs text-muted-foreground">
      {account} · {mappingCount} service route{mappingCount === 1 ? "" : "s"}
    </p>
  );
}

function PagerDutyRoutingSettings({
  integration,
  repositories,
  onReplaceCredentials,
}: {
  integration?: PagerDutyIntegration;
  repositories: Repository[];
  onReplaceCredentials: () => void;
}) {
  const queryClient = useQueryClient();
  const [serviceID, setServiceID] = useState("");
  const [serviceName, setServiceName] = useState("");
  const [mappingRepoID, setMappingRepoID] = useState("");
  const [baseBranch, setBaseBranch] = useState("");
  const activeRepositories = repositories.filter((repo) => repo.status === "active");
  const integrationID = integration?.id ?? "";
  const webhookQuery = integration?.integration_id
    ? `integration_id=${encodeURIComponent(integration.integration_id)}&pagerduty_integration_id=${encodeURIComponent(integration.id)}`
    : "";
  const webhookPath = webhookQuery
    ? `/api/v1/webhooks/pagerduty?${webhookQuery}`
    : "";
  const actionPath = webhookQuery
    ? `/api/v1/webhooks/pagerduty/start-session?${webhookQuery}`
    : "";
  const workflowRunPath = "/api/v1/automations/{automation_id}/run";

  const { data: mappingsResp, isLoading } = useQuery({
    queryKey: queryKeys.integrations.pagerDutyMappings(integrationID),
    queryFn: () => api.integrations.listPagerDutyMappings(integrationID),
    enabled: Boolean(integrationID),
    staleTime: 60_000,
  });
  const { data: incidentsResp, isLoading: incidentsLoading } = useQuery({
    queryKey: ["integrations", "pagerduty", "incidents", integrationID],
    queryFn: () =>
      api.integrations.listPagerDutyIncidents({
        integration_id: integrationID,
        limit: 5,
      }),
    enabled: Boolean(integrationID),
    staleTime: 30_000,
  });
  const testConnection = useMutation({
    mutationFn: () => api.integrations.testPagerDuty(integrationID),
  });
  const updateSettings = useMutation({
    mutationFn: (body: {
      default_repository_id?: string | null;
      writeback_enabled?: boolean;
      auto_create_webhook?: boolean;
    }) => api.integrations.updatePagerDuty(body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.integrations.pagerDuty });
      queryClient.invalidateQueries({ queryKey: ["integrations"] });
    },
  });
  const startIncidentSession = useMutation({
    mutationFn: (incident: PagerDutyIncident) =>
      api.integrations.startPagerDutyIncidentSession(incident.incident_id, {
        pagerduty_integration_id: incident.pagerduty_integration_id,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["integrations", "pagerduty", "incidents", integrationID],
      });
    },
  });
  const upsertMapping = useMutation({
    mutationFn: () =>
      api.integrations.upsertPagerDutyMapping({
        pagerduty_integration_id: integrationID,
        pagerduty_service_id: serviceID.trim(),
        pagerduty_service_name: serviceName.trim(),
        repository_id: mappingRepoID,
        base_branch: baseBranch.trim() || undefined,
      }),
    onSuccess: () => {
      setServiceID("");
      setServiceName("");
      setMappingRepoID("");
      setBaseBranch("");
      queryClient.invalidateQueries({ queryKey: queryKeys.integrations.pagerDutyMappings(integrationID) });
      queryClient.invalidateQueries({ queryKey: queryKeys.integrations.pagerDuty });
    },
  });

  if (!integration) {
    return (
      <div className="space-y-3">
        <h3 className="text-sm font-medium">PagerDuty routing</h3>
        <p className="text-sm text-muted-foreground">
          Connect PagerDuty before configuring service-to-repository routing.
        </p>
        <Button size="sm" onClick={onReplaceCredentials}>Connect PagerDuty</Button>
      </div>
    );
  }

  const mappings = mappingsResp?.data ?? [];
  const incidents = incidentsResp?.data ?? [];
  const health = testConnection.data?.data;
  const canSave = integrationID !== "" && serviceID.trim() !== "" && serviceName.trim() !== "" && mappingRepoID !== "" && !upsertMapping.isPending;

  return (
    <div className="space-y-5">
      <div className="space-y-3 rounded-md border border-border p-3">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
          <div className="space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="text-sm font-medium">Connection health</h3>
              <Badge variant={integration.status === "active" ? "secondary" : "outline"}>
                {integration.status}
              </Badge>
            </div>
            <p className="text-sm text-muted-foreground">
              {integration.account_subdomain
                ? `${integration.account_subdomain}.pagerduty.com`
                : "PagerDuty account"}
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              variant="outline"
              loading={testConnection.isPending}
              disabled={testConnection.isPending}
              onClick={() => testConnection.mutate()}
            >
              Test PagerDuty connection
            </Button>
            <Button size="sm" variant="outline" onClick={onReplaceCredentials}>
              {integration.status === "degraded" ? "Reauthorize PagerDuty" : "Replace credentials"}
            </Button>
          </div>
        </div>
        {integration.last_error ? (
          <ErrorText>{integration.last_error}</ErrorText>
        ) : null}
        {health ? <PagerDutyHealthResult health={health} /> : null}
        {testConnection.isError ? (
          <ErrorText>Failed to test PagerDuty connection.</ErrorText>
        ) : null}
      </div>

      <div className="space-y-3 rounded-md border border-border p-3">
        <div>
          <h3 className="text-sm font-medium">Defaults</h3>
          <p className="mt-1 text-sm text-muted-foreground">
            Choose fallback routing and writeback behavior for PagerDuty sessions and automations.
          </p>
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label>Default repository</Label>
            <Select
              value={integration.default_repository_id ?? NO_DEFAULT_REPO_VALUE}
              onValueChange={(value) =>
                updateSettings.mutate({
                  default_repository_id:
                    value === NO_DEFAULT_REPO_VALUE ? null : value,
                })
              }
              disabled={activeRepositories.length === 0 || updateSettings.isPending}
            >
              <SelectTrigger aria-label="Default PagerDuty repository">
                <SelectValue placeholder="Repository" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NO_DEFAULT_REPO_VALUE}>No default repository</SelectItem>
                {activeRepositories.map((repo) => (
                  <SelectItem key={repo.id} value={repo.id}>{repo.full_name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-3">
            <div className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2">
              <Label htmlFor="pagerduty-writeback" className="min-w-0">
                PagerDuty writeback
              </Label>
              <Switch
                id="pagerduty-writeback"
                aria-label="PagerDuty writeback"
                checked={integration.writeback_enabled}
                disabled={updateSettings.isPending}
                onCheckedChange={(checked) =>
                  updateSettings.mutate({ writeback_enabled: checked })
                }
              />
            </div>
            <div className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2">
              <Label htmlFor="pagerduty-auto-webhook" className="min-w-0">
                Auto-create webhook
              </Label>
              <Switch
                id="pagerduty-auto-webhook"
                aria-label="PagerDuty auto-create webhook"
                checked={integration.auto_create_webhook === true}
                disabled={updateSettings.isPending}
                onCheckedChange={(checked) =>
                  updateSettings.mutate({ auto_create_webhook: checked })
                }
              />
            </div>
          </div>
        </div>
        {updateSettings.isError ? (
          <ErrorText>Failed to update PagerDuty settings.</ErrorText>
        ) : null}
      </div>

      <div className="space-y-3">
        <h3 className="text-sm font-medium">Webhook delivery</h3>
        <p className="text-sm text-muted-foreground">
          Use this endpoint when configuring the PagerDuty webhook subscription for this integration.
        </p>
        <Input readOnly value={webhookPath} aria-label="PagerDuty webhook URL" />
        <div className="space-y-2">
          <p className="text-sm text-muted-foreground">
            Use this endpoint for a PagerDuty Custom Incident Action named Start 143 session.
          </p>
          <Input readOnly value={actionPath} aria-label="PagerDuty start session action URL" />
        </div>
        <div className="space-y-2">
          <p className="text-sm text-muted-foreground">
            Use this endpoint from PagerDuty Incident Workflow Web API actions to run a specific event automation.
          </p>
          <Input readOnly value={workflowRunPath} aria-label="PagerDuty automation workflow run URL" />
        </div>
      </div>

      <div className="space-y-3 rounded-md border border-border p-3">
        <div>
          <h3 className="text-sm font-medium">Recent incidents</h3>
          <p className="mt-1 text-sm text-muted-foreground">
            Mirrored PagerDuty incidents can start a focused agent session.
          </p>
        </div>
        {incidentsLoading ? (
          <p className="text-sm text-muted-foreground">Loading incidents...</p>
        ) : incidents.length === 0 ? (
          <p className="text-sm text-muted-foreground">No PagerDuty incidents mirrored yet.</p>
        ) : (
          <div className="space-y-2">
            {incidents.map((incident) => (
              <PagerDutyIncidentRow
                key={incident.id || incident.incident_id}
                incident={incident}
                repositoryName={pagerDutyIncidentRepositoryName(
                  incident,
                  mappings,
                  activeRepositories,
                  integration.default_repository_id,
                )}
                starting={startIncidentSession.isPending}
                onStart={() => startIncidentSession.mutate(incident)}
              />
            ))}
          </div>
        )}
        {startIncidentSession.isError ? (
          <ErrorText>Failed to start a PagerDuty incident session.</ErrorText>
        ) : null}
      </div>

      <div className="space-y-3 rounded-md border border-border p-3">
        <div>
          <h3 className="text-sm font-medium">Service routing</h3>
          <p className="mt-1 text-sm text-muted-foreground">
            Route incidents from PagerDuty service IDs into the repository the responding agent should use.
          </p>
        </div>
        {isLoading ? (
          <p className="text-sm text-muted-foreground">Loading service routes...</p>
        ) : mappings.length === 0 ? (
          <p className="text-sm text-muted-foreground">No service routes configured yet.</p>
        ) : (
          <div className="space-y-2">
            {mappings.map((mapping: PagerDutyServiceRepoMapping) => (
              <div key={mapping.id} className="grid gap-2 rounded-md border border-border px-3 py-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
                <div className="min-w-0">
                  <div className="truncate text-sm font-medium">{mapping.pagerduty_service_name || mapping.pagerduty_service_id}</div>
                  <div className="truncate text-xs text-muted-foreground">{mapping.pagerduty_service_id}</div>
                </div>
                <div className="min-w-0 text-sm">
                  <div className="truncate">{repoName(activeRepositories, mapping.repository_id)}</div>
                  <div className="truncate text-xs text-muted-foreground">{mapping.base_branch || "Repository default branch"}</div>
                </div>
              </div>
            ))}
          </div>
        )}

        <div className="grid gap-2 border-t border-border pt-4 sm:grid-cols-[1fr_1fr_1.3fr_0.8fr_auto]">
          <Input
            aria-label="PagerDuty service ID"
            placeholder="Service ID"
            value={serviceID}
            onChange={(event) => setServiceID(event.target.value)}
          />
          <Input
            aria-label="PagerDuty service name"
            placeholder="Service name"
            value={serviceName}
            onChange={(event) => setServiceName(event.target.value)}
          />
          <Select value={mappingRepoID} onValueChange={setMappingRepoID} disabled={activeRepositories.length === 0}>
            <SelectTrigger aria-label="PagerDuty route repository">
              <SelectValue placeholder="Repository" />
            </SelectTrigger>
            <SelectContent>
              {activeRepositories.map((repo) => (
                <SelectItem key={repo.id} value={repo.id}>{repo.full_name}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Input
            aria-label="PagerDuty route base branch"
            placeholder="Branch"
            value={baseBranch}
            onChange={(event) => setBaseBranch(event.target.value)}
          />
          <Button
            type="button"
            disabled={!canSave}
            loading={upsertMapping.isPending}
            onClick={() => upsertMapping.mutate()}
          >
            Save
          </Button>
        </div>
        {activeRepositories.length === 0 ? (
          <p className="text-xs text-muted-foreground">Connect a GitHub repository before routing PagerDuty incidents.</p>
        ) : null}
        {upsertMapping.isError ? <ErrorText>Failed to update PagerDuty service routing.</ErrorText> : null}
      </div>
    </div>
  );
}

function PagerDutyHealthResult({ health }: { health: PagerDutyHealth }) {
  const healthy = health.auth_ok && health.credential_configured;
  return (
    <div className="space-y-2 rounded-md bg-muted/30 px-3 py-2">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={healthy ? "secondary" : "destructive"}>
          {healthy ? "Connection healthy" : "Connection needs attention"}
        </Badge>
        <span className="text-xs text-muted-foreground">
          Webhook secret {health.webhook_secret_configured ? "configured" : "missing"}
        </span>
      </div>
      <dl className="grid gap-x-4 gap-y-1 text-xs sm:grid-cols-[auto_1fr]">
        <dt className="text-muted-foreground">Last checked</dt>
        <dd>{formatIntegrationTimestamp(health.last_health_check_at)}</dd>
        <dt className="text-muted-foreground">Last synced</dt>
        <dd>{formatIntegrationTimestamp(health.last_synced_at)}</dd>
        <dt className="text-muted-foreground">Writeback</dt>
        <dd>{health.writeback_enabled ? "Enabled" : "Disabled"}</dd>
        <dt className="text-muted-foreground">Webhook setup</dt>
        <dd>{health.auto_create_webhook ? "Automatic" : "Manual"}</dd>
        <dt className="text-muted-foreground">Webhook failures</dt>
        <dd>{health.recent_webhook_failures ?? 0} in the last 24h</dd>
        {health.latest_webhook_error ? (
          <>
            <dt className="text-muted-foreground">Latest webhook error</dt>
            <dd>{health.latest_webhook_error}</dd>
          </>
        ) : null}
      </dl>
      {health.last_error ? <ErrorText>{health.last_error}</ErrorText> : null}
      {health.symptoms.length > 0 ? (
        <ul className="list-disc space-y-1 pl-4 text-xs text-muted-foreground">
          {health.symptoms.map((symptom) => (
            <li key={symptom}>{symptom}</li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function PagerDutyIncidentRow({
  incident,
  repositoryName,
  starting,
  onStart,
}: {
  incident: PagerDutyIncident;
  repositoryName: string;
  starting: boolean;
  onStart: () => void;
}) {
  const serviceLabel = incident.service_name || incident.service_id || "Unknown service";
  const repositoryMapped = repositoryName !== "Unmapped";
  const teams = incident.team_ids?.filter(Boolean) ?? [];
  return (
    <div className="grid gap-3 rounded-md border border-border px-3 py-2 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-start">
      <div className="min-w-0 space-y-2">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="truncate text-sm font-medium">{incident.title}</span>
          {incident.incident_number ? <Badge variant="outline">#{incident.incident_number}</Badge> : null}
          <Badge variant="outline">{incident.status}</Badge>
          {incident.urgency ? <Badge variant="secondary">{incident.urgency}</Badge> : null}
          {incident.priority_name ? <Badge variant="secondary">{incident.priority_name}</Badge> : null}
        </div>
        <div className="space-y-0.5 text-xs text-muted-foreground">
          <p className="truncate">Service: {serviceLabel}</p>
          <p className={repositoryMapped ? "truncate" : "truncate text-destructive"}>Repository: {repositoryName}</p>
          {incident.escalation_policy_name ? <p className="truncate">Escalation: {incident.escalation_policy_name}</p> : null}
          {teams.length > 0 ? <p className="truncate">Teams: {teams.join(", ")}</p> : null}
          {incident.latest_note ? <p className="line-clamp-2">Latest note: {incident.latest_note}</p> : null}
        </div>
      </div>
      <div className="flex flex-wrap gap-2 sm:justify-end">
        {incident.html_url ? (
          <Button asChild size="sm" variant="ghost">
            <a href={incident.html_url} target="_blank" rel="noreferrer" aria-label="Open incident in PagerDuty">
              <ExternalLink aria-hidden="true" />
              Open
            </a>
          </Button>
        ) : null}
        <Button
          type="button"
          size="sm"
          variant="outline"
          loading={starting}
          disabled={starting}
          onClick={onStart}
          aria-label={`Start session for ${incident.title}`}
        >
          Start session
        </Button>
      </div>
    </div>
  );
}

function pagerDutyIncidentRepositoryName(
  incident: PagerDutyIncident,
  mappings: PagerDutyServiceRepoMapping[],
  repositories: Repository[],
  defaultRepositoryID?: string,
): string {
  const serviceID = incident.service_id?.trim();
  if (serviceID) {
    const mapping = mappings.find((candidate) => candidate.enabled && candidate.pagerduty_service_id === serviceID);
    if (mapping) {
      return repoName(repositories, mapping.repository_id);
    }
  }
  if (defaultRepositoryID) {
    return repoName(repositories, defaultRepositoryID);
  }
  return "Unmapped";
}

function formatIntegrationTimestamp(value?: string): string {
  if (!value) return "Never";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
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
  githubAccountLogin,
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
  onReplacePagerDutyCredentials,
  mezmoBaseURL,
  pagerDutyIntegration,
}: {
  provider: IntegrationKey | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  connected: Partial<Record<IntegrationKey, boolean>>;
  repositories: Repository[];
  githubInstallationId?: number;
  githubAccountLogin?: string;
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
  onReplacePagerDutyCredentials: () => void;
  mezmoBaseURL?: string;
  pagerDutyIntegration?: PagerDutyIntegration;
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
            <>
              {isConnected ? (
                <div className="rounded-md border border-border p-3">
                  <div className="text-xs font-medium uppercase text-muted-foreground">Team access</div>
                  <p className="mt-2 text-sm text-muted-foreground">
                    Members of {githubAccountLogin || "this GitHub organization"} can now join this workspace automatically. Manage auto-join in Team settings.
                  </p>
                  <Button asChild variant="link" size="sm" className="mt-1 h-auto p-0 text-sm">
                    <Link href="/settings/team">Open Team settings</Link>
                  </Button>
                </div>
              ) : null}
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
            </>
          ) : null}
          {provider === "linear" ? <LinearAgentRoutingSettings repositoriesOverride={repositories} /> : null}
          {provider === "slack" ? (
            <>
              <SlackBotDefaults repositories={repositories} />
              <SlackChannelPicker />
              <SlackUserLinkingSection />
            </>
          ) : null}
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
              </dl>
              <p className="text-sm text-muted-foreground">
                Replace the Mezmo service key or base URL used for production log queries.
              </p>
              <Button size="sm" variant="outline" onClick={onReplaceMezmoCredentials}>Replace credentials</Button>
            </div>
          ) : null}
          {provider === "pagerduty" ? (
            <PagerDutyRoutingSettings
              integration={pagerDutyIntegration}
              repositories={repositories}
              onReplaceCredentials={onReplacePagerDutyCredentials}
            />
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
  const { data: pagerDutyResp } = useQuery({
    queryKey: queryKeys.integrations.pagerDuty,
    queryFn: () => api.integrations.listPagerDuty(),
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
    mutationFn: ({ apiKey, baseUrl }: { apiKey: string; baseUrl: string }) =>
      api.integrations.connectMezmo(apiKey, baseUrl),
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
  const pagerDutyIntegration = pagerDutyResp?.data?.find(
    (integration) => integration.status === "active" || integration.status === "degraded"
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
    pagerduty: Boolean(pagerDutyIntegration),
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
        pagerdutyConnected={Boolean(pagerDutyIntegration)}
        pagerdutyLoading={false}
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
        onConnectPagerDuty={() => {
          api.integrations.loginPagerDuty();
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
              Production log queries are enabled
            </p>
          ) : undefined,
          pagerduty: pagerDutyIntegration ? (
            <PagerDutyCardSummary integration={pagerDutyIntegration} />
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
        githubAccountLogin={githubIntegration?.github_account_login}
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
        onReplacePagerDutyCredentials={() => {
          api.integrations.loginPagerDuty();
        }}
        mezmoBaseURL={mezmoIntegration?.mezmo_base_url}
        pagerDutyIntegration={pagerDutyIntegration}
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
            Open Mezmo, select the right organization, then go to Settings &gt;
            Organization &gt; API Keys. Create a service key there so agents can query production logs.{" "}
            <a
              href="https://app.mezmo.com/"
              target="_blank"
              rel="noopener noreferrer"
              className="underline"
            >
              Open Mezmo
            </a>
            . Base URL is optional; leave it blank to use the default Mezmo API host.
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
        ]}
        submitting={mezmoConnectMutation.isPending}
        error={mezmoError}
        onSubmit={(values) =>
          mezmoConnectMutation.mutate({ apiKey: values.apiKey, baseUrl: values.baseUrl })
        }
      />

    </PageContainer>
  );
}
