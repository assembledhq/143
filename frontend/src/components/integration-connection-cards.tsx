import { useState, type ReactNode } from "react";
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
  onManageIntegration?: (provider: IntegrationKey) => void;
  disconnectingProvider?: IntegrationKey | null;
  disconnectErrorProvider?: IntegrationKey | null;
  disconnectError?: string | null;
};

export type GithubRepoChip = {
  id: string;
  full_name: string;
  status: string;
};

type SourceControlIntegrationCardProps = IntegrationCallbacks & {
  githubConnected: boolean;
  githubRepos?: GithubRepoChip[];
  onConnectGitHub: () => void;
  onManageGitHub?: () => void;
  onSyncRepos?: () => void;
  isSyncing?: boolean;
  githubSummary?: ReactNode;
};

// IntegrationAuthErrorInfo mirrors the shape the backend returns on
// Integration.auth_error: a controlled reason string plus the timestamp
// the failure was last observed. Surfaced as an amber banner attached to
// the relevant integration card so the user sees "you need to reconnect"
// rather than a generic "session prepare failed" downstream.
export type IntegrationAuthErrorInfo = {
  reason: string;
  at: string;
};

type AdditionalIntegrationCardsProps = IntegrationCallbacks & {
  sentryConnected: boolean;
  linearConnected: boolean;
  linearLoading: boolean;
  linearAuthError?: IntegrationAuthErrorInfo | null;
  slackConnected: boolean;
  notionConnected: boolean;
  notionLoading?: boolean;
  circleciConnected: boolean;
  circleciLoading?: boolean;
  summaries?: Partial<Record<IntegrationKey, ReactNode>>;
  onConnectSentry: () => void;
  onConnectLinear: () => void;
  onConnectSlack: () => void;
  onConnectNotion: () => void;
  onConnectCircleCI: () => void;
};

// readOnly hides connect/disconnect buttons on every card and the per-repo
// disconnect/reconnect chips. Used to render the integrations page for
// non-admins, who can see what's connected but cannot change it.
type ReadOnlyProps = { readOnly?: boolean };

type AllIntegrationCardsProps =
  SourceControlIntegrationCardProps & AdditionalIntegrationCardsPropsWithReadOnly;

type AdditionalIntegrationCardsPropsWithReadOnly =
  AdditionalIntegrationCardsProps & ReadOnlyProps;

function RepoSummaryPill({ repo }: { repo: GithubRepoChip }) {
  return (
    <Badge variant="secondary" className="max-w-44 truncate rounded-md text-xs">
      {repo.full_name}
    </Badge>
  );
}

function ConnectedReposList({
  repos,
}: {
  repos: GithubRepoChip[];
}) {
  const active = repos.filter((r) => r.status === "active");
  if (active.length === 0) {
    return (
      <p className="mt-1.5 text-xs text-muted-foreground">
        {repos.length > 0 ? "No active repositories" : "No repositories connected"}
      </p>
    );
  }
  const visible = active.slice(0, 3);
  const hiddenCount = active.length - visible.length;

  return (
    <div className="mt-1.5 flex min-w-0 flex-wrap gap-1.5">
      {visible.map((repo) => (
        <RepoSummaryPill key={repo.id} repo={repo} />
      ))}
      {hiddenCount > 0 ? (
        <Badge variant="outline" className="rounded-md text-xs">
          +{hiddenCount} more
        </Badge>
      ) : null}
    </div>
  );
}

// IntegrationAuthErrorAlert renders the amber "your token was rejected"
// banner the integrations card slots in via `extra`. Reason + timestamp
// only — the actual Reconnect button lives in the row's main action slot
// (IntegrationAction with authErrored=true) so there's a single CTA per
// row instead of two.
function IntegrationAuthErrorAlert({ info }: { info: IntegrationAuthErrorInfo }) {
  // Render the absolute timestamp in an unambiguous, locale-stable format
  // (YYYY-MM-DD HH:MM:SS in the viewer's local timezone) so operators
  // helping each other across regions can compare notes without
  // 5/2/2026-vs-02/05/2026 confusion. Local time still — the user is
  // already implicitly in their tz when they read the page.
  let observedAt = info.at;
  try {
    const d = new Date(info.at);
    if (!Number.isNaN(d.getTime())) {
      const fmt = new Intl.DateTimeFormat("sv-SE", {
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
        hour12: false,
      });
      observedAt = fmt.format(d);
    }
  } catch {
    // fall back to the raw string
  }
  return (
    <div
      role="alert"
      className="mt-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
    >
      <div className="font-medium">Reconnect required</div>
      <p className="mt-0.5 text-amber-700/90 dark:text-amber-200/90">{info.reason}</p>
      <p className="mt-0.5 text-xs text-amber-700/70 dark:text-amber-200/70">
        Last seen at {observedAt}
      </p>
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
  circleci: "This will disconnect CircleCI from your organization. Flaky-test data will no longer be available to agents.",
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
  readOnly,
  authErrored,
}: {
  connected: boolean;
  integrationKey: IntegrationKey;
  integrationName: string;
  onConnect: () => void;
  onDisconnect?: (provider: IntegrationKey) => void;
  disconnecting?: boolean;
  disconnectError?: string | null;
  loading?: boolean;
  readOnly?: boolean;
  /**
   * When true, the row's primary action becomes a "Reconnect <Provider>"
   * button instead of "Connect"/"Disconnect". Set whenever the backend
   * surfaces Integration.auth_error so the user has a single clear CTA
   * (the amber banner above the row carries the reason; this is the action).
   */
  authErrored?: boolean;
}) {
  const [confirmOpen, setConfirmOpen] = useState(false);

  if (readOnly) {
    return (
      <Badge variant={authErrored ? "outline" : connected ? "secondary" : "outline"} className="text-xs">
        {authErrored ? "Reconnect required" : connected ? "Connected" : "Not connected"}
      </Badge>
    );
  }

  if (authErrored) {
    // Single CTA in the auth-errored state: skip the disconnect / connected
    // / connect branches below so the user sees one unambiguous "Reconnect"
    // button. onConnect drives the OAuth flow which the API handler treats
    // as a reconnect (ensureIntegration resets status and clears markers).
    return (
      <Button
        size="sm"
        variant="default"
        loading={loading}
        disabled={loading}
        onClick={onConnect}
        aria-label={`Reconnect ${integrationName}`}
      >
        {`Reconnect ${integrationName}`}
      </Button>
    );
  }

  if (connected && onDisconnect === undefined && loading === undefined) {
    return (
      <Button
        size="sm"
        disabled
        aria-label={`${integrationName} Connected`}
      >
        Connected
      </Button>
    );
  }

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
  githubSummary,
  onManageGitHub,
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
          summary: githubConnected ? githubSummary ?? <ConnectedReposList repos={githubRepos} /> : undefined,
          action: (
            <div className="flex items-center gap-1.5">
              {githubConnected && onSyncRepos && !onManageGitHub && (
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
              {githubConnected && onManageGitHub ? (
                <div className="flex items-center gap-2">
                  <Badge variant="secondary" className="text-xs">Connected</Badge>
                  <Button size="sm" variant="outline" onClick={onManageGitHub} aria-label="Manage GitHub">
                    Manage
                  </Button>
                </div>
              ) : (
                <IntegrationAction
                  connected={githubConnected}
                  integrationKey="github"
                  integrationName={github.name}
                  onConnect={onConnectGitHub}
                  onDisconnect={onDisconnect}
                  disconnecting={disconnectingProvider === "github"}
                  disconnectError={disconnectErrorProvider === "github" ? disconnectError : null}
                />
              )}
            </div>
          ),
        },
      ]}
    />
  );
}

// Describes one optional integration card. Used by both AdditionalIntegrationCards
// (the setup-checklist row) and AllIntegrationCards (the full settings page),
// which kept drifting apart whenever a new provider was added.
type OptionalIntegrationDescriptor = {
  key: IntegrationKey;
  connected: boolean;
  loading?: boolean;
  authError?: IntegrationAuthErrorInfo | null;
  summary?: ReactNode;
  onConnect: () => void;
};

function buildOptionalIntegrationItems(
  descriptors: OptionalIntegrationDescriptor[],
  shared: IntegrationCallbacks & ReadOnlyProps,
) {
  return descriptors.map((d) => {
    const meta = getIntegrationByKey(d.key);
    return {
      id: meta.key,
      title: meta.name,
      description: meta.description,
      logo: <IntegrationLogo name={meta.name} src={meta.logoSrc} />,
      badge: <Badge variant="secondary" className="text-xs">Optional</Badge>,
      summary: (
        <>
          {d.authError ? <IntegrationAuthErrorAlert info={d.authError} /> : null}
          {d.summary}
        </>
      ),
      action: d.connected && !d.authError && shared.onManageIntegration && !shared.readOnly ? (
        <div className="flex items-center gap-2">
          <Badge variant="secondary" className="text-xs">Connected</Badge>
          <Button size="sm" variant="outline" onClick={() => shared.onManageIntegration?.(d.key)} aria-label={`Manage ${meta.name}`}>
            Manage
          </Button>
        </div>
      ) : (
        <IntegrationAction
          connected={d.connected}
          integrationKey={d.key}
          integrationName={meta.name}
          onConnect={d.onConnect}
          onDisconnect={shared.onDisconnect}
          disconnecting={shared.disconnectingProvider === d.key}
          disconnectError={shared.disconnectErrorProvider === d.key ? shared.disconnectError : null}
          loading={d.loading}
          readOnly={shared.readOnly}
          authErrored={Boolean(d.authError)}
        />
      ),
    };
  });
}

function optionalDescriptorsFromProps(
  p: AdditionalIntegrationCardsPropsWithReadOnly,
): OptionalIntegrationDescriptor[] {
  return [
    { key: "sentry", connected: p.sentryConnected, summary: p.summaries?.sentry, onConnect: p.onConnectSentry },
    { key: "linear", connected: p.linearConnected, loading: p.linearLoading, authError: p.linearAuthError ?? null, summary: p.summaries?.linear, onConnect: p.onConnectLinear },
    { key: "slack", connected: p.slackConnected, summary: p.summaries?.slack, onConnect: p.onConnectSlack },
    { key: "notion", connected: p.notionConnected, loading: p.notionLoading, summary: p.summaries?.notion, onConnect: p.onConnectNotion },
    { key: "circleci", connected: p.circleciConnected, loading: p.circleciLoading, summary: p.summaries?.circleci, onConnect: p.onConnectCircleCI },
  ];
}

export function AdditionalIntegrationCards(props: AdditionalIntegrationCardsPropsWithReadOnly) {
  return (
    <IntegrationsCard items={buildOptionalIntegrationItems(optionalDescriptorsFromProps(props), props)} />
  );
}

export function AllIntegrationCards(props: AllIntegrationCardsProps) {
  const github = getIntegrationByKey("github");
  const githubItem = {
    id: github.key,
    title: github.name,
    description: github.description,
    logo: <IntegrationLogo name={github.name} src={github.logoSrc} />,
    badge: <Badge variant="outline" className="text-xs">Required</Badge>,
    summary: props.githubConnected ? props.githubSummary ?? <ConnectedReposList repos={props.githubRepos ?? []} /> : undefined,
    action: props.githubConnected && props.onManageGitHub && !props.readOnly ? (
      <div className="flex items-center gap-2">
        <Badge variant="secondary" className="text-xs">Connected</Badge>
        <Button size="sm" variant="outline" onClick={props.onManageGitHub} aria-label="Manage GitHub">
          Manage
        </Button>
      </div>
    ) : (
      <IntegrationAction
        connected={props.githubConnected}
        integrationKey="github"
        integrationName={github.name}
        onConnect={props.onConnectGitHub}
        onDisconnect={props.onDisconnect}
        disconnecting={props.disconnectingProvider === "github"}
        disconnectError={props.disconnectErrorProvider === "github" ? props.disconnectError : null}
        readOnly={props.readOnly}
      />
    ),
  };
  return (
    <IntegrationsCard items={[githubItem, ...buildOptionalIntegrationItems(optionalDescriptorsFromProps(props), props)]} />
  );
}
