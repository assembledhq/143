"use client";

import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { PageHeader } from "@/components/page-header";
import { IntegrationsCard } from "@/components/integrations-card";
import { INTEGRATIONS } from "@/lib/integrations";

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

      <p className="text-sm text-muted-foreground">
        Once integrations are connected, 143 picks up issues, generates fixes, and opens PRs automatically.
      </p>
    </div>
  );
}
