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

type SlackChannel = { id: string; name: string; selected: boolean };
type SlackChannelsResp = { data: SlackChannel[] } | undefined;

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
    // Coalesce multi-toggle bursts: the later selection wins.
    coalesce: (_a, b) => b,
    debounceMs: 400,
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
  const { data: integrationsResp } = useQuery({
    queryKey: ["integrations"],
    queryFn: () => api.integrations.list(),
  });
  const { data: reposResp } = useQuery({
    queryKey: ["repositories"],
    queryFn: () => api.repositories.list(),
  });
  const disconnectMutation = useDisconnectIntegration();

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

  const githubIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "github" && integration.status === "active"
  );
  const sentryIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "sentry" && integration.status === "active"
  );
  const linearIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "linear" && integration.status === "active"
  );
  const slackIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "slack" && integration.status === "active"
  );
  const notionIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "notion" && integration.status === "active"
  );

  const connectedRepoNames = (reposResp?.data ?? [])
    .filter((r) => r.status === "active")
    .map((r) => r.full_name);

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="Integrations"
          description="Connect external services to your organization."
        />
      <AllIntegrationCards
        githubConnected={Boolean(githubIntegration)}
        githubRepoNames={connectedRepoNames}
        sentryConnected={Boolean(sentryIntegration)}
        linearConnected={Boolean(linearIntegration)}
        linearLoading={false}
        slackConnected={Boolean(slackIntegration)}
        notionConnected={Boolean(notionIntegration)}
        notionLoading={notionConnectMutation.isPending}
        onConnectGitHub={() => api.integrations.loginGitHub()}
        onConnectSentry={() => api.auth.loginSentry()}
        onConnectLinear={() => api.integrations.loginLinear()}
        onConnectSlack={() => api.integrations.loginSlack()}
        onConnectNotion={() => {
          setNotionError(null);
          setNotionToken("");
          setNotionDialogOpen(true);
        }}
        onDisconnect={(provider) => disconnectMutation.mutate(provider)}
        disconnectingProvider={disconnectMutation.isPending ? disconnectMutation.variables : null}
        disconnectErrorProvider={disconnectMutation.isError ? disconnectMutation.variables ?? null : null}
        disconnectError={disconnectMutation.isError ? "Failed to disconnect." : null}
      />
      {slackIntegration && <SlackChannelPicker />}
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
    </PageContainer>
  );
}
