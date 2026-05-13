"use client";

import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
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
import {
  useDisconnectRepository,
  useReconnectRepository,
} from "@/hooks/use-repository-connection";
import { useAuth } from "@/hooks/use-auth";

type SlackChannel = { id: string; name: string; selected: boolean };
type SlackChannelsResp = { data: SlackChannel[] } | undefined;

// Coalesce multi-toggle bursts: the later selection wins. Hoisted so every
// `useAutosave` caller sharing `queryKeys.integrations.slackChannels` passes
// the same referential identity - `useAutosave` throws in dev when two
// callers register different coalesce fns against the same queryKey.
const coalesceSlackChannels = (_a: string[], b: string[]): string[] => b;

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

export default function IntegrationsPage() {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";
  const { data: integrationsResp } = useQuery({
    queryKey: ["integrations"],
    queryFn: () => api.integrations.list(),
  });
  // Fetch disconnected repos too so the user has a "Reconnect" affordance —
  // without this, a user-disconnected repo becomes a ghost with no discoverable
  // path back to active.
  const { data: reposResp } = useQuery({
    queryKey: ["repositories", { includeDisconnected: true }],
    queryFn: () => api.repositories.list({ includeDisconnected: true }),
  });
  const disconnectMutation = useDisconnectIntegration();
  const disconnectRepoMutation = useDisconnectRepository();
  const reconnectRepoMutation = useReconnectRepository();

  // Notion token dialog state.
  const [notionDialogOpen, setNotionDialogOpen] = useState(false);
  const [notionToken, setNotionToken] = useState("");
  const [notionError, setNotionError] = useState<string | null>(null);

  const notionConnectMutation = useMutation({
    mutationFn: (token: string) => api.integrations.connectNotion(token),
    onSuccess: () => {
      setNotionDialogOpen(false);
      setNotionToken("");
      setNotionError(null);
      queryClient.invalidateQueries({ queryKey: ["integrations"] });
    },
    onError: (err: Error) => {
      setNotionError(err.message || "Failed to connect Notion. Check your token.");
    },
  });

  // CircleCI token + project slug dialog state. CircleCI doesn't expose the
  // v2 Insights API via OAuth, so we use a paste-the-token form like Notion.
  const [circleciDialogOpen, setCircleciDialogOpen] = useState(false);
  const [circleciToken, setCircleciToken] = useState("");
  const [circleciProjectSlug, setCircleciProjectSlug] = useState("");
  const [circleciError, setCircleciError] = useState<string | null>(null);

  const circleciConnectMutation = useMutation({
    mutationFn: ({ token, projectSlug }: { token: string; projectSlug: string }) =>
      api.integrations.connectCircleCI(token, projectSlug),
    onSuccess: () => {
      setCircleciDialogOpen(false);
      setCircleciToken("");
      setCircleciProjectSlug("");
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

  const githubRepos = (reposResp?.data ?? []).map((r) => ({
    id: r.id,
    full_name: r.full_name,
    status: r.status,
  }));
  const pendingRepoID = disconnectRepoMutation.isPending
    ? (disconnectRepoMutation.variables ?? null)
    : reconnectRepoMutation.isPending
      ? (reconnectRepoMutation.variables ?? null)
      : null;

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
        githubRepos={githubRepos}
        onDisconnectRepo={(id) => disconnectRepoMutation.mutate(id)}
        onReconnectRepo={(id) => reconnectRepoMutation.mutate(id)}
        pendingRepoID={pendingRepoID}
        sentryConnected={Boolean(sentryIntegration)}
        linearConnected={Boolean(linearIntegration)}
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
          setNotionToken("");
          setNotionDialogOpen(true);
        }}
        onConnectCircleCI={() => {
          setCircleciError(null);
          setCircleciToken("");
          setCircleciProjectSlug("");
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

      <AlertDialog open={notionDialogOpen} onOpenChange={setNotionDialogOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Connect Notion</AlertDialogTitle>
            <AlertDialogDescription>
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
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="notion-token">Integration Token</Label>
            <Input
              id="notion-token"
              type="password"
              placeholder="ntn_..."
              value={notionToken}
              onChange={(e) => {
                setNotionToken(e.target.value);
                setNotionError(null);
              }}
            />
            {notionError && (
              <p className="text-xs text-destructive">{notionError}</p>
            )}
          </div>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <Button
              onClick={() => notionConnectMutation.mutate(notionToken)}
              disabled={!notionToken.trim() || notionConnectMutation.isPending}
              loading={notionConnectMutation.isPending}
            >
              Connect
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={circleciDialogOpen} onOpenChange={setCircleciDialogOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Connect CircleCI</AlertDialogTitle>
            <AlertDialogDescription>
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
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="grid gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="circleci-token">Personal API Token</Label>
              <Input
                id="circleci-token"
                type="password"
                placeholder="CCI-..."
                value={circleciToken}
                onChange={(e) => {
                  setCircleciToken(e.target.value);
                  setCircleciError(null);
                }}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="circleci-project-slug">Project Slug</Label>
              <Input
                id="circleci-project-slug"
                type="text"
                placeholder="gh/your-org/your-repo"
                value={circleciProjectSlug}
                onChange={(e) => {
                  setCircleciProjectSlug(e.target.value);
                  setCircleciError(null);
                }}
              />
            </div>
            {circleciError && (
              <p className="text-xs text-destructive">{circleciError}</p>
            )}
          </div>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <Button
              onClick={() =>
                circleciConnectMutation.mutate({
                  token: circleciToken.trim(),
                  projectSlug: circleciProjectSlug.trim(),
                })
              }
              disabled={
                !circleciToken.trim() ||
                !circleciProjectSlug.trim() ||
                circleciConnectMutation.isPending
              }
              loading={circleciConnectMutation.isPending}
            >
              Connect
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </PageContainer>
  );
}
