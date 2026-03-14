import { Badge } from "@/components/ui/badge";
import Image from "next/image";
import { Button } from "@/components/ui/button";
import { IntegrationsCard } from "@/components/integrations-card";
import { getIntegrationByKey } from "@/lib/integrations";

type SourceControlIntegrationCardProps = {
  githubConnected: boolean;
  githubRepoNames?: string[];
  onConnectGitHub: () => void;
};

type AdditionalIntegrationCardsProps = {
  sentryConnected: boolean;
  linearConnected: boolean;
  linearLoading: boolean;
  slackConnected: boolean;
  onConnectSentry: () => void;
  onConnectLinear: () => void;
  onConnectSlack: () => void;
};

type AllIntegrationCardsProps = SourceControlIntegrationCardProps & AdditionalIntegrationCardsProps;

function ConnectedReposList({ repoNames }: { repoNames: string[] }) {
  if (repoNames.length === 0) return null;
  return (
    <div className="mt-1.5 flex flex-wrap gap-1.5">
      {repoNames.map((name) => (
        <span
          key={name}
          className="inline-flex items-center rounded-md bg-muted px-2 py-0.5 text-xs font-medium text-muted-foreground"
        >
          {name}
        </span>
      ))}
    </div>
  );
}

function IntegrationLogo({ name, src }: { name: string; src: string }) {
  return (
    <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-muted/50 dark:bg-white/5 ring-1 ring-border/50 transition-transform duration-200 group-hover:scale-105">
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

export function SourceControlIntegrationCard({ githubConnected, githubRepoNames = [], onConnectGitHub }: SourceControlIntegrationCardProps) {
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
          extra: githubConnected ? <ConnectedReposList repoNames={githubRepoNames} /> : undefined,
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
  slackConnected,
  onConnectSentry,
  onConnectLinear,
  onConnectSlack,
}: AdditionalIntegrationCardsProps) {
  const sentry = getIntegrationByKey("sentry");
  const linear = getIntegrationByKey("linear");
  const slack = getIntegrationByKey("slack");

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
        {
          id: slack.key,
          title: slack.name,
          description: slack.description,
          logo: <IntegrationLogo name={slack.name} src={slack.logoSrc} />,
          badge: <Badge variant="secondary" className="text-xs">Optional</Badge>,
          action: (
            <Button
              size="sm"
              disabled={slackConnected}
              onClick={onConnectSlack}
              aria-label={slackConnected ? "Slack Connected" : "Connect Slack"}
            >
              {slackConnected ? "Connected" : "Connect"}
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
  onConnectSlack,
  githubConnected,
  githubRepoNames = [],
  sentryConnected,
  linearConnected,
  linearLoading,
  slackConnected,
}: AllIntegrationCardsProps) {
  const github = getIntegrationByKey("github");
  const sentry = getIntegrationByKey("sentry");
  const linear = getIntegrationByKey("linear");
  const slack = getIntegrationByKey("slack");

  return (
    <IntegrationsCard
      items={[
        {
          id: github.key,
          title: github.name,
          description: github.description,
          logo: <IntegrationLogo name={github.name} src={github.logoSrc} />,
          badge: <Badge variant="outline" className="text-xs">Required</Badge>,
          extra: githubConnected ? <ConnectedReposList repoNames={githubRepoNames} /> : undefined,
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
        {
          id: slack.key,
          title: slack.name,
          description: slack.description,
          logo: <IntegrationLogo name={slack.name} src={slack.logoSrc} />,
          badge: <Badge variant="secondary" className="text-xs">Optional</Badge>,
          action: (
            <Button
              size="sm"
              disabled={slackConnected}
              onClick={onConnectSlack}
              aria-label={slackConnected ? "Slack Connected" : "Connect Slack"}
            >
              {slackConnected ? "Connected" : "Connect"}
            </Button>
          ),
        },
      ]}
    />
  );
}
