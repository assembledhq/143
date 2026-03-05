import { GitBranch, Bug, SquareKanban } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { IntegrationsCard } from "@/components/integrations-card";
import { INTEGRATIONS } from "@/lib/integrations";

type SourceControlIntegrationCardProps = {
  onConnectGitHub: () => void;
};

type AdditionalIntegrationCardsProps = {
  linearConnected: boolean;
  linearLoading: boolean;
  onConnectSentry: () => void;
  onConnectLinear: () => void;
};

type AllIntegrationCardsProps = SourceControlIntegrationCardProps & AdditionalIntegrationCardsProps;

export function SourceControlIntegrationCard({ onConnectGitHub }: SourceControlIntegrationCardProps) {
  const github = INTEGRATIONS[0];

  return (
    <IntegrationsCard
      items={[
        {
          id: github.key,
          title: github.name,
          description: github.description,
          icon: <GitBranch className="h-5 w-5" />,
          badge: <Badge variant="outline" className="text-xs">Required</Badge>,
          action: (
            <Button size="sm" onClick={onConnectGitHub} aria-label="Connect GitHub">
              Connect
            </Button>
          ),
        },
      ]}
    />
  );
}

export function AdditionalIntegrationCards({
  linearConnected,
  linearLoading,
  onConnectSentry,
  onConnectLinear,
}: AdditionalIntegrationCardsProps) {
  const sentry = INTEGRATIONS[1];
  const linear = INTEGRATIONS[2];

  return (
    <IntegrationsCard
      items={[
        {
          id: sentry.key,
          title: sentry.name,
          description: sentry.description,
          icon: <Bug className="h-5 w-5" />,
          badge: <Badge variant="secondary" className="text-xs">Optional</Badge>,
          action: (
            <Button
              size="sm"
              onClick={onConnectSentry}
              aria-label="Connect Sentry"
            >
              Connect
            </Button>
          ),
        },
        {
          id: linear.key,
          title: linear.name,
          description: linear.description,
          icon: <SquareKanban className="h-5 w-5" />,
          badge: <Badge variant="secondary" className="text-xs">Optional</Badge>,
          action: (
            <Button
              size="sm"
              aria-label={linearConnected ? "Linear Connected" : "Connect Linear"}
              loading={linearLoading}
              disabled={linearConnected || linearLoading}
              onClick={onConnectLinear}
            >
              {linearConnected ? "Connected" : "Connect"}
            </Button>
          ),
        },
      ]}
    />
  );
}

export function AllIntegrationCards({
  onConnectGitHub,
  onConnectSentry,
  onConnectLinear,
  linearConnected,
  linearLoading,
}: AllIntegrationCardsProps) {
  const github = INTEGRATIONS[0];
  const sentry = INTEGRATIONS[1];
  const linear = INTEGRATIONS[2];

  return (
    <IntegrationsCard
      items={[
        {
          id: github.key,
          title: github.name,
          description: github.description,
          icon: <GitBranch className="h-5 w-5" />,
          badge: <Badge variant="outline" className="text-xs">Required</Badge>,
          action: (
            <Button size="sm" onClick={onConnectGitHub} aria-label="Connect GitHub">
              Connect
            </Button>
          ),
        },
        {
          id: sentry.key,
          title: sentry.name,
          description: sentry.description,
          icon: <Bug className="h-5 w-5" />,
          badge: <Badge variant="secondary" className="text-xs">Optional</Badge>,
          action: (
            <Button
              size="sm"
              onClick={onConnectSentry}
              aria-label="Connect Sentry"
            >
              Connect
            </Button>
          ),
        },
        {
          id: linear.key,
          title: linear.name,
          description: linear.description,
          icon: <SquareKanban className="h-5 w-5" />,
          badge: <Badge variant="secondary" className="text-xs">Optional</Badge>,
          action: (
            <Button
              size="sm"
              aria-label={linearConnected ? "Linear Connected" : "Connect Linear"}
              loading={linearLoading}
              disabled={linearConnected || linearLoading}
              onClick={onConnectLinear}
            >
              {linearConnected ? "Connected" : "Connect"}
            </Button>
          ),
        },
      ]}
    />
  );
}
