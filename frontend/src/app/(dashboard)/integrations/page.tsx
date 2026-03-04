"use client";

import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { IntegrationsCard } from "@/components/integrations-card";
import { PageHeader } from "@/components/page-header";
import { INTEGRATIONS } from "@/lib/integrations";

export default function IntegrationsPage() {
  const [github, sentry, linear] = INTEGRATIONS;
  const queryClient = useQueryClient();

  const { data: integrationsResp } = useQuery({
    queryKey: ["integrations"],
    queryFn: () => api.integrations.list(),
  });
  const linearIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "linear" && integration.status === "active"
  );

  const connectLinearMutation = useMutation({
    mutationFn: () => api.integrations.connectLinear(),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["integrations"] });
    },
  });

  return (
    <div className="space-y-6">
      <PageHeader
        title="Integrations"
        description="Connect external services to your organization."
      />
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
            action: (
              <Button
                size="sm"
                aria-label={linearIntegration ? "Linear Connected" : "Connect Linear"}
                loading={connectLinearMutation.isPending}
                disabled={Boolean(linearIntegration) || connectLinearMutation.isPending}
                onClick={() => connectLinearMutation.mutate()}
              >
                {linearIntegration ? "Connected" : "Connect"}
              </Button>
            ),
          },
        ]}
      />
    </div>
  );
}
