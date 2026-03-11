import { Badge } from "@/components/ui/badge";
import Image from "next/image";
import { Button } from "@/components/ui/button";
import { IntegrationsCard } from "@/components/integrations-card";
import { getIntegrationByKey } from "@/lib/integrations";

type SourceControlIntegrationCardProps = {
  githubConnected: boolean;
  onConnectGitHub: () => void;
};

type AdditionalIntegrationCardsProps = {
  sentryConnected: boolean;
  linearConnected: boolean;
  linearLoading: boolean;
  onConnectSentry: () => void;
  onConnectLinear: () => void;
};

type AllIntegrationCardsProps = SourceControlIntegrationCardProps & AdditionalIntegrationCardsProps;

function IntegrationLogo({ name, src }: { name: string; src: string }) {
  return (
    <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-muted">
      <Image
        src={src}
        alt={`${name} logo`}
        className="h-5 w-5 object-contain"
        width={20}
        height={20}
        unoptimized
      />
    </div>
  );
}

export function SourceControlIntegrationCard({ githubConnected, onConnectGitHub }: SourceControlIntegrationCardProps) {
  const github = getIntegrationByKey("github");

  return (
    <IntegrationsCard
      items={[
        {
          id: github.key,
          title: github.name,
          description: github.description,
          logo: <IntegrationLogo name={github.name} src={github.logoSrc} />,
          badge: <Badge variant="outline" className="text-xs">Required</Badge>,
          action: (
            <Button
              size="sm"
              disabled={githubConnected}
              onClick={onConnectGitHub}
              aria-label={githubConnected ? "GitHub Connected" : "Connect GitHub"}
            >
              {githubConnected ? "Connected" : "Connect"}
            </Button>
          ),
        },
      ]}
    />
  );
}

export function AdditionalIntegrationCards({
  sentryConnected,
  linearConnected,
  linearLoading,
  onConnectSentry,
  onConnectLinear,
}: AdditionalIntegrationCardsProps) {
  const sentry = getIntegrationByKey("sentry");
  const linear = getIntegrationByKey("linear");

  return (
    <IntegrationsCard
      items={[
        {
          id: sentry.key,
          title: sentry.name,
          description: sentry.description,
          logo: <IntegrationLogo name={sentry.name} src={sentry.logoSrc} />,
          badge: <Badge variant="secondary" className="text-xs">Optional</Badge>,
          action: (
            <Button
              size="sm"
              disabled={sentryConnected}
              onClick={onConnectSentry}
              aria-label={sentryConnected ? "Sentry Connected" : "Connect Sentry"}
            >
              {sentryConnected ? "Connected" : "Connect"}
            </Button>
          ),
        },
        {
          id: linear.key,
          title: linear.name,
          description: linear.description,
          logo: <IntegrationLogo name={linear.name} src={linear.logoSrc} />,
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
  githubConnected,
  sentryConnected,
  linearConnected,
  linearLoading,
}: AllIntegrationCardsProps) {
  const github = getIntegrationByKey("github");
  const sentry = getIntegrationByKey("sentry");
  const linear = getIntegrationByKey("linear");

  return (
    <IntegrationsCard
      items={[
        {
          id: github.key,
          title: github.name,
          description: github.description,
          logo: <IntegrationLogo name={github.name} src={github.logoSrc} />,
          badge: <Badge variant="outline" className="text-xs">Required</Badge>,
          action: (
            <Button
              size="sm"
              disabled={githubConnected}
              onClick={onConnectGitHub}
              aria-label={githubConnected ? "GitHub Connected" : "Connect GitHub"}
            >
              {githubConnected ? "Connected" : "Connect"}
            </Button>
          ),
        },
        {
          id: sentry.key,
          title: sentry.name,
          description: sentry.description,
          logo: <IntegrationLogo name={sentry.name} src={sentry.logoSrc} />,
          badge: <Badge variant="secondary" className="text-xs">Optional</Badge>,
          action: (
            <Button
              size="sm"
              disabled={sentryConnected}
              onClick={onConnectSentry}
              aria-label={sentryConnected ? "Sentry Connected" : "Connect Sentry"}
            >
              {sentryConnected ? "Connected" : "Connect"}
            </Button>
          ),
        },
        {
          id: linear.key,
          title: linear.name,
          description: linear.description,
          logo: <IntegrationLogo name={linear.name} src={linear.logoSrc} />,
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
