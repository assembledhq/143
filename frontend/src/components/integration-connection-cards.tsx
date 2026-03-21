import { useState } from "react";
import { RefreshCw } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import Image from "next/image";
import { Button } from "@/components/ui/button";
import { IntegrationsCard } from "@/components/integrations-card";
import { getIntegrationByKey, type IntegrationKey } from "@/lib/integrations";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";

type IntegrationCallbacks = {
  onDisconnect?: (provider: IntegrationKey) => void;
  disconnectingProvider?: IntegrationKey | null;
  disconnectErrorProvider?: IntegrationKey | null;
  disconnectError?: string | null;
};

type SourceControlIntegrationCardProps = IntegrationCallbacks & {
  githubConnected: boolean;
  githubRepoNames?: string[];
  onConnectGitHub: () => void;
  onSyncRepos?: () => void;
  isSyncing?: boolean;
};

type AdditionalIntegrationCardsProps = IntegrationCallbacks & {
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

const DISCONNECT_DESCRIPTIONS: Record<IntegrationKey, string> = {
  github: "This will disconnect GitHub from your organization. Repositories will no longer sync and sessions won\u2019t have access to your code.",
  sentry: "This will disconnect Sentry from your organization. Error tracking data will no longer sync.",
  linear: "This will disconnect Linear from your organization. Issues will no longer sync.",
  slack: "This will disconnect Slack from your organization. Channel monitoring will stop.",
};

function IntegrationAction({
  connected,
  integrationKey,
  integrationName,
  onConnect,
  onDisconnect,
  disconnecting,
  disconnectError,
  loading,
}: {
  connected: boolean;
  integrationKey: IntegrationKey;
  integrationName: string;
  onConnect: () => void;
  onDisconnect?: (provider: IntegrationKey) => void;
  disconnecting?: boolean;
  disconnectError?: string | null;
  loading?: boolean;
}) {
  const [confirmOpen, setConfirmOpen] = useState(false);

  if (connected && onDisconnect) {
    return (
      <>
        <div className="flex items-center gap-2">
          {disconnectError && (
            <span className="text-xs text-destructive">{disconnectError}</span>
          )}
          <span className="text-xs text-muted-foreground">Connected</span>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setConfirmOpen(true)}
            loading={disconnecting}
            disabled={disconnecting}
            aria-label={`Disconnect ${integrationName}`}
          >
            {disconnecting ? "Disconnecting..." : "Disconnect"}
          </Button>
        </div>
        <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Disconnect {integrationName}</AlertDialogTitle>
              <AlertDialogDescription>
                {DISCONNECT_DESCRIPTIONS[integrationKey]}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction
                onClick={() => {
                  setConfirmOpen(false);
                  onDisconnect(integrationKey);
                }}
                className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              >
                Disconnect
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </>
    );
  }

  return (
    <Button
      size="sm"
      disabled={connected || loading}
      loading={loading}
      onClick={onConnect}
      aria-label={connected ? `${integrationName} Connected` : `Connect ${integrationName}`}
    >
      {connected ? "Connected" : "Connect"}
    </Button>
  );
}

export function SourceControlIntegrationCard({
  githubConnected,
  githubRepoNames = [],
  onConnectGitHub,
  onDisconnect,
  disconnectingProvider,
  disconnectErrorProvider,
  disconnectError,
  onSyncRepos,
  isSyncing,
}: SourceControlIntegrationCardProps) {
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
            <div className="flex items-center gap-1.5">
              {githubConnected && onSyncRepos && (
                <Button
                  size="icon"
                  variant="ghost"
                  className="h-8 w-8"
                  onClick={onSyncRepos}
                  disabled={isSyncing}
                  aria-label="Sync repositories"
                >
                  <RefreshCw className={`h-3.5 w-3.5 ${isSyncing ? "animate-spin" : ""}`} />
                </Button>
              )}
              <IntegrationAction
                connected={githubConnected}
                integrationKey="github"
                integrationName={github.name}
                onConnect={onConnectGitHub}
                onDisconnect={onDisconnect}
                disconnecting={disconnectingProvider === "github"}
                disconnectError={disconnectErrorProvider === "github" ? disconnectError : null}
              />
            </div>
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
  onDisconnect,
  disconnectingProvider,
  disconnectErrorProvider,
  disconnectError,
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
            <IntegrationAction
              connected={sentryConnected}
              integrationKey="sentry"
              integrationName={sentry.name}
              onConnect={onConnectSentry}
              onDisconnect={onDisconnect}
              disconnecting={disconnectingProvider === "sentry"}
              disconnectError={disconnectErrorProvider === "sentry" ? disconnectError : null}
            />
          ),
        },
        {
          id: linear.key,
          title: linear.name,
          description: linear.description,
          logo: <IntegrationLogo name={linear.name} src={linear.logoSrc} />,
          badge: <Badge variant="secondary" className="text-xs">Optional</Badge>,
          action: (
            <IntegrationAction
              connected={linearConnected}
              integrationKey="linear"
              integrationName={linear.name}
              onConnect={onConnectLinear}
              onDisconnect={onDisconnect}
              disconnecting={disconnectingProvider === "linear"}
              disconnectError={disconnectErrorProvider === "linear" ? disconnectError : null}
              loading={linearLoading}
            />
          ),
        },
        {
          id: slack.key,
          title: slack.name,
          description: slack.description,
          logo: <IntegrationLogo name={slack.name} src={slack.logoSrc} />,
          badge: <Badge variant="secondary" className="text-xs">Optional</Badge>,
          action: (
            <IntegrationAction
              connected={slackConnected}
              integrationKey="slack"
              integrationName={slack.name}
              onConnect={onConnectSlack}
              onDisconnect={onDisconnect}
              disconnecting={disconnectingProvider === "slack"}
              disconnectError={disconnectErrorProvider === "slack" ? disconnectError : null}
            />
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
  onDisconnect,
  disconnectingProvider,
  disconnectErrorProvider,
  disconnectError,
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
            <IntegrationAction
              connected={githubConnected}
              integrationKey="github"
              integrationName={github.name}
              onConnect={onConnectGitHub}
              onDisconnect={onDisconnect}
              disconnecting={disconnectingProvider === "github"}
              disconnectError={disconnectErrorProvider === "github" ? disconnectError : null}
            />
          ),
        },
        {
          id: sentry.key,
          title: sentry.name,
          description: sentry.description,
          logo: <IntegrationLogo name={sentry.name} src={sentry.logoSrc} />,
          badge: <Badge variant="secondary" className="text-xs">Optional</Badge>,
          action: (
            <IntegrationAction
              connected={sentryConnected}
              integrationKey="sentry"
              integrationName={sentry.name}
              onConnect={onConnectSentry}
              onDisconnect={onDisconnect}
              disconnecting={disconnectingProvider === "sentry"}
              disconnectError={disconnectErrorProvider === "sentry" ? disconnectError : null}
            />
          ),
        },
        {
          id: linear.key,
          title: linear.name,
          description: linear.description,
          logo: <IntegrationLogo name={linear.name} src={linear.logoSrc} />,
          badge: <Badge variant="secondary" className="text-xs">Optional</Badge>,
          action: (
            <IntegrationAction
              connected={linearConnected}
              integrationKey="linear"
              integrationName={linear.name}
              onConnect={onConnectLinear}
              onDisconnect={onDisconnect}
              disconnecting={disconnectingProvider === "linear"}
              disconnectError={disconnectErrorProvider === "linear" ? disconnectError : null}
              loading={linearLoading}
            />
          ),
        },
        {
          id: slack.key,
          title: slack.name,
          description: slack.description,
          logo: <IntegrationLogo name={slack.name} src={slack.logoSrc} />,
          badge: <Badge variant="secondary" className="text-xs">Optional</Badge>,
          action: (
            <IntegrationAction
              connected={slackConnected}
              integrationKey="slack"
              integrationName={slack.name}
              onConnect={onConnectSlack}
              onDisconnect={onDisconnect}
              disconnecting={disconnectingProvider === "slack"}
              disconnectError={disconnectErrorProvider === "slack" ? disconnectError : null}
            />
          ),
        },
      ]}
    />
  );
}
