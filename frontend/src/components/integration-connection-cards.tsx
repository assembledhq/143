import { Badge } from "@/components/ui/badge";
import Image from "next/image";
import { Button } from "@/components/ui/button";
import { IntegrationsCard } from "@/components/integrations-card";
import { getIntegrationByKey } from "@/lib/integrations";

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

export function SourceControlIntegrationCard({ onConnectGitHub }: SourceControlIntegrationCardProps) {
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
            <Button size="sm" onClick={onConnectGitHub} aria-label="Connect GitHub">
              Connect
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
