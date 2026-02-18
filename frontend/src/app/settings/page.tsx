"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { PageHeader } from "@/components/page-header";
import { IntegrationsCard } from "@/components/integrations-card";
import { INTEGRATIONS } from "@/lib/integrations";
import type { Organization, SingleResponse } from "@/lib/types";

export default function SettingsPage() {
  const [github, sentry, linear] = INTEGRATIONS;

  const { data: settings } = useQuery<SingleResponse<Organization>>({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });

  return (
    <div className="space-y-8">
      <PageHeader
        title="Settings"
        description="Manage your organization and integrations."
      />

      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">General</h2>
        <Card>
          <CardContent>
            <div className="space-y-2">
              <Label htmlFor="org-name">Organization Name</Label>
              <Input
                id="org-name"
                value={settings?.data?.name ?? ""}
                disabled
                className="bg-muted"
              />
            </div>
          </CardContent>
        </Card>
      </section>

      <section className="space-y-3">
        <h2 className="text-[13px] font-medium text-foreground">Integrations</h2>
        <IntegrationsCard
          items={[
            {
              id: github.key,
              title: github.name,
              description: github.description,
              action: (
                <Button size="sm" onClick={() => api.auth.login()} aria-label="Connect GitHub">
                  Connect
                </Button>
              ),
            },
            {
              id: sentry.key,
              title: sentry.name,
              description: sentry.description,
              action: <Badge variant="secondary">Coming soon</Badge>,
            },
            {
              id: linear.key,
              title: linear.name,
              description: linear.description,
              action: <Badge variant="secondary">Coming soon</Badge>,
            },
          ]}
        />
      </section>
    </div>
  );
}
