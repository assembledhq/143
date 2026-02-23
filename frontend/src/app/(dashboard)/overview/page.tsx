"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { PageHeader } from "@/components/page-header";
import { IntegrationsCard } from "@/components/integrations-card";
import { INTEGRATIONS } from "@/lib/integrations";
import type { CodexAuthStatus } from "@/lib/types";

function AgentSetupCard() {
  const [authStatus, setAuthStatus] = useState<CodexAuthStatus | null>(null);

  useEffect(() => {
    api.codexAuth.status().then((res) => setAuthStatus(res.data)).catch(() => {});
  }, []);

  if (authStatus?.status === "completed") {
    return (
      <Card className="py-0">
        <CardContent className="flex items-center justify-between gap-4 py-4">
          <div className="min-w-0 flex-1">
            <p className="text-sm font-medium text-foreground">Coding Agent</p>
            <p className="mt-0.5 text-sm text-muted-foreground">
              Codex is connected via ChatGPT.
            </p>
          </div>
          <Badge variant="secondary">Connected</Badge>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card className="py-0">
      <CardContent className="flex items-center justify-between gap-4 py-4">
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium text-foreground">Connect your coding agent</p>
          <p className="mt-0.5 text-sm text-muted-foreground">
            Sign in with ChatGPT to let Codex fix issues automatically, or configure an API key.
          </p>
        </div>
        <div className="shrink-0">
          <a href="/settings">
            <Button size="sm">Set up in Settings</Button>
          </a>
        </div>
      </CardContent>
    </Card>
  );
}

export default function Overview() {
  const [github, sentry, linear] = INTEGRATIONS;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Overview"
        description="Get started by connecting your tools."
      />

      <div className="space-y-3">
        <IntegrationsCard
          items={[
            {
              id: github.key,
              title: `Connect ${github.name}`,
              description: github.description,
              action: (
                <Button size="sm" onClick={() => api.auth.login()} aria-label="Connect GitHub">
                  Connect
                </Button>
              ),
            },
            {
              id: sentry.key,
              title: `Connect ${sentry.name}`,
              description: sentry.description,
              action: (
                <Button size="sm" onClick={() => api.auth.loginSentry()} aria-label="Connect Sentry">
                  Connect
                </Button>
              ),
            },
            {
              id: linear.key,
              title: `Connect ${linear.name}`,
              description: linear.description,
              action: <Badge variant="secondary">Coming soon</Badge>,
            },
          ]}
        />
      </div>

      <AgentSetupCard />

      <p className="text-sm text-muted-foreground">
        Once integrations are connected, 143 picks up issues, generates fixes, and opens PRs automatically.
      </p>
    </div>
  );
}
