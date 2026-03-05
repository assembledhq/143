"use client";

import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { AllIntegrationCards } from "@/components/integration-connection-cards";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";

export default function IntegrationsPage() {
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
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="Integrations"
          description="Connect external services to your organization."
        />
        <AllIntegrationCards
          linearConnected={Boolean(linearIntegration)}
          linearLoading={connectLinearMutation.isPending}
          onConnectGitHub={() => api.auth.login()}
          onConnectSentry={() => api.auth.loginSentry()}
          onConnectLinear={() => connectLinearMutation.mutate()}
        />
      </div>
    </PageContainer>
  );
}
