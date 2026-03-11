"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { AllIntegrationCards } from "@/components/integration-connection-cards";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";

export default function IntegrationsPage() {
  const { data: integrationsResp } = useQuery({
    queryKey: ["integrations"],
    queryFn: () => api.integrations.list(),
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

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="Integrations"
          description="Connect external services to your organization."
        />
      <AllIntegrationCards
        githubConnected={Boolean(githubIntegration)}
        sentryConnected={Boolean(sentryIntegration)}
        linearConnected={Boolean(linearIntegration)}
        linearLoading={false}
        onConnectGitHub={() => api.integrations.loginGitHub()}
        onConnectSentry={() => api.auth.loginSentry()}
        onConnectLinear={() => api.integrations.loginLinear()}
      />
      </div>
    </PageContainer>
  );
}
