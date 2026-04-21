import { useState } from "react";
import { RefreshCw, X } from "lucide-react";
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

export type GithubRepoChip = {
  id: string;
  full_name: string;
  status: string;
};

type RepoCallbacks = {
  onDisconnectRepo?: (repoID: string) => void;
  onReconnectRepo?: (repoID: string) => void;
  pendingRepoID?: string | null;
};

type SourceControlIntegrationCardProps = IntegrationCallbacks & RepoCallbacks & {
  githubConnected: boolean;
  githubRepos?: GithubRepoChip[];
  onConnectGitHub: () => void;
  onSyncRepos?: () => void;
  isSyncing?: boolean;
};

type AdditionalIntegrationCardsProps = IntegrationCallbacks & {
  sentryConnected: boolean;
  linearConnected: boolean;
  linearLoading: boolean;
  slackConnected: boolean;
  notionConnected: boolean;
  notionLoading?: boolean;
  onConnectSentry: () => void;
  onConnectLinear: () => void;
  onConnectSlack: () => void;
  onConnectNotion: () => void;
};

type AllIntegrationCardsProps = SourceControlIntegrationCardProps & AdditionalIntegrationCardsProps;

// ActiveRepoChip renders one active repo with a trailing × button that opens a
// confirmation dialog before disconnecting. The × is only shown when a handler
// is provided so read-only callers (e.g. the GitHub card rendered before the
// user has the disconnect wiring) still display cleanly.
function ActiveRepoChip({
  repo,
  onDisconnect,
  pending,
}: {
  repo: GithubRepoChip;
  onDisconnect?: (id: string) => void;
  pending: boolean;
}) {
  const [confirmOpen, setConfirmOpen] = useState(false);

  return (
    <>
      <span
        className="inline-flex items-center gap-1 rounded-md bg-muted px-2 py-0.5 text-xs font-medium text-muted-foreground"
      >
        {repo.full_name}
        {onDisconnect && (
          <button
            type="button"
            onClick={() => setConfirmOpen(true)}
            disabled={pending}
            aria-label={`Disconnect ${repo.full_name}`}
            className="ml-0.5 rounded-sm p-0.5 text-muted-foreground/70 transition hover:bg-destructive/10 hover:text-destructive disabled:opacity-50"
          >
            <X className="h-3 w-3" />
          </button>
        )}
      </span>
      <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Disconnect {repo.full_name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Existing sessions and runs for this repo will remain visible, but
              you won&rsquo;t be able to start new ones. You can reconnect at
              any time from this page.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                setConfirmOpen(false);
                onDisconnect?.(repo.id);
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

function DisconnectedRepoChip({
  repo,
  onReconnect,
  pending,
}: {
  repo: GithubRepoChip;
  onReconnect?: (id: string) => void;
  pending: boolean;
}) {
  return (
    <span className="inline-flex items-center gap-1 rounded-md border border-dashed border-muted-foreground/30 bg-transparent px-2 py-0.5 text-xs font-medium text-muted-foreground/70 line-through">
      {repo.full_name}
      {onReconnect && (
        <button
          type="button"
          onClick={() => onReconnect(repo.id)}
          disabled={pending}
          aria-label={`Reconnect ${repo.full_name}`}
          className="ml-0.5 rounded-sm px-1 py-0 text-xs font-semibold uppercase tracking-wide text-primary no-underline hover:bg-primary/10 disabled:opacity-50"
        >
          Reconnect
        </button>
      )}
    </span>
  );
}

function ConnectedReposList({
  repos,
  onDisconnectRepo,
  onReconnectRepo,
  pendingRepoID,
}: {
  repos: GithubRepoChip[];
} & RepoCallbacks) {
  if (repos.length === 0) return null;
  const active = repos.filter((r) => r.status === "active");
  const disconnected = repos.filter((r) => r.status !== "active");

  return (
    <div className="mt-1.5 space-y-1.5">
      {active.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {active.map((repo) => (
            <ActiveRepoChip
              key={repo.id}
              repo={repo}
              onDisconnect={onDisconnectRepo}
              pending={pendingRepoID === repo.id}
            />
          ))}
        </div>
      )}
      {disconnected.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {disconnected.map((repo) => (
            <DisconnectedRepoChip
              key={repo.id}
              repo={repo}
              onReconnect={onReconnectRepo}
              pending={pendingRepoID === repo.id}
            />
          ))}
        </div>
      )}
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
  notion: "This will disconnect Notion from your organization. Product docs will no longer sync.",
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
  githubRepos = [],
  onConnectGitHub,
  onDisconnect,
  disconnectingProvider,
  disconnectErrorProvider,
  disconnectError,
  onSyncRepos,
  isSyncing,
  onDisconnectRepo,
  onReconnectRepo,
  pendingRepoID,
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
          extra: githubConnected ? (
            <ConnectedReposList
              repos={githubRepos}
              onDisconnectRepo={onDisconnectRepo}
              onReconnectRepo={onReconnectRepo}
              pendingRepoID={pendingRepoID}
            />
          ) : undefined,
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
  notionConnected,
  notionLoading,
  onConnectSentry,
  onConnectLinear,
  onConnectSlack,
  onConnectNotion,
  onDisconnect,
  disconnectingProvider,
  disconnectErrorProvider,
  disconnectError,
}: AdditionalIntegrationCardsProps) {
  const sentry = getIntegrationByKey("sentry");
  const linear = getIntegrationByKey("linear");
  const slack = getIntegrationByKey("slack");
  const notion = getIntegrationByKey("notion");

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
        {
          id: notion.key,
          title: notion.name,
          description: notion.description,
          logo: <IntegrationLogo name={notion.name} src={notion.logoSrc} />,
          badge: <Badge variant="secondary" className="text-xs">Optional</Badge>,
          action: (
            <IntegrationAction
              connected={notionConnected}
              integrationKey="notion"
              integrationName={notion.name}
              onConnect={onConnectNotion}
              onDisconnect={onDisconnect}
              disconnecting={disconnectingProvider === "notion"}
              disconnectError={disconnectErrorProvider === "notion" ? disconnectError : null}
              loading={notionLoading}
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
  onConnectNotion,
  onDisconnect,
  disconnectingProvider,
  disconnectErrorProvider,
  disconnectError,
  githubConnected,
  githubRepos = [],
  sentryConnected,
  linearConnected,
  linearLoading,
  slackConnected,
  notionConnected,
  notionLoading,
  onDisconnectRepo,
  onReconnectRepo,
  pendingRepoID,
}: AllIntegrationCardsProps) {
  const github = getIntegrationByKey("github");
  const sentry = getIntegrationByKey("sentry");
  const linear = getIntegrationByKey("linear");
  const slack = getIntegrationByKey("slack");
  const notion = getIntegrationByKey("notion");

  return (
    <IntegrationsCard
      items={[
        {
          id: github.key,
          title: github.name,
          description: github.description,
          logo: <IntegrationLogo name={github.name} src={github.logoSrc} />,
          badge: <Badge variant="outline" className="text-xs">Required</Badge>,
          extra: githubConnected ? (
            <ConnectedReposList
              repos={githubRepos}
              onDisconnectRepo={onDisconnectRepo}
              onReconnectRepo={onReconnectRepo}
              pendingRepoID={pendingRepoID}
            />
          ) : undefined,
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
        {
          id: notion.key,
          title: notion.name,
          description: notion.description,
          logo: <IntegrationLogo name={notion.name} src={notion.logoSrc} />,
          badge: <Badge variant="secondary" className="text-xs">Optional</Badge>,
          action: (
            <IntegrationAction
              connected={notionConnected}
              integrationKey="notion"
              integrationName={notion.name}
              onConnect={onConnectNotion}
              onDisconnect={onDisconnect}
              disconnecting={disconnectingProvider === "notion"}
              disconnectError={disconnectErrorProvider === "notion" ? disconnectError : null}
              loading={notionLoading}
            />
          ),
        },
      ]}
    />
  );
}
