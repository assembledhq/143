"use client";

import { useState, useEffect } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { AllIntegrationCards } from "@/components/integration-connection-cards";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

function SlackChannelPicker() {
  const queryClient = useQueryClient();
  const { data: channelsResp, isLoading } = useQuery({
    queryKey: ["slack-channels"],
    queryFn: () => api.integrations.listSlackChannels(),
  });

  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [initialized, setInitialized] = useState(false);

  // Initialize selection from server state once loaded.
  useEffect(() => {
    if (channelsResp?.data && !initialized) {
      const preSelected = new Set(
        channelsResp.data.filter((ch: { selected: boolean; id: string }) => ch.selected).map((ch: { id: string }) => ch.id)
      );
      setSelected(preSelected);
      setInitialized(true);
    }
  }, [channelsResp?.data, initialized]);

  const mutation = useMutation({
    mutationFn: (channelIds: string[]) => api.integrations.updateSlackChannels(channelIds),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["slack-channels"] });
    },
  });

  const toggle = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  };

  const save = () => {
    mutation.mutate(Array.from(selected));
  };

  if (isLoading) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Slack Channels</CardTitle>
          <CardDescription>Loading channels...</CardDescription>
        </CardHeader>
      </Card>
    );
  }

  const channels = channelsResp?.data ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Monitored Slack Channels</CardTitle>
        <CardDescription>
          Select which channels the PM agent should monitor for actionable conversations.
        </CardDescription>
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
            <div className="flex items-center justify-between pt-2">
              <p className="text-xs text-muted-foreground">
                {selected.size} channel{selected.size !== 1 ? "s" : ""} selected
              </p>
              <Button
                size="sm"
                onClick={save}
                loading={mutation.isPending}
                disabled={mutation.isPending}
              >
                Save
              </Button>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

export default function IntegrationsPage() {
  const { data: integrationsResp } = useQuery({
    queryKey: ["integrations"],
    queryFn: () => api.integrations.list(),
  });
  const { data: reposResp } = useQuery({
    queryKey: ["repositories"],
    queryFn: () => api.repositories.list(),
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
        onConnectGitHub={() => api.integrations.loginGitHub()}
        onConnectSentry={() => api.auth.loginSentry()}
        onConnectLinear={() => api.integrations.loginLinear()}
        onConnectSlack={() => api.integrations.loginSlack()}
      />
      {slackIntegration && <SlackChannelPicker />}
      </div>
    </PageContainer>
  );
}
