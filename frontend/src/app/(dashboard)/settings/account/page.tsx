"use client";

import { type ReactNode, useMemo, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, KeyRound, Sparkles, Check, Eye, EyeOff, Shield } from "lucide-react";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import { useAuth } from "@/hooks/use-auth";
import { AGENT_TYPES, KEY_PLACEHOLDERS, sourceLabel, sourceBadgeVariant, providerDisplayName } from "@/lib/agent-constants";
import {
  PI_INHERITED_PROVIDERS,
  countInheritedProvidersConfigured,
  hasAnyInheritedProviderConfigured,
} from "@/lib/agents";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup } from "@/components/ui/radio-group";
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
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { RadioCard } from "@/components/radio-card";
import { ThemeSelect } from "@/components/theme-select";
import { CodexDeviceCodeModal } from "@/components/codex-device-code-modal";
import type {
  UserCredentialSummary,
  ResolvedCredential,
  ListResponse,
} from "@/lib/types";

/* ------------------------------------------------------------------ */
/*  GitHub PR Connection                                              */
/* ------------------------------------------------------------------ */

function GitHubPRConnection() {
  const queryClient = useQueryClient();
  const { data: ghStatus, isLoading } = useQuery({
    queryKey: ["github-status"],
    queryFn: () => api.githubStatus.get(),
  });
  const disconnectMutation = useMutation({
    mutationFn: () => api.githubStatus.disconnect(),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["github-status"] }),
  });

  if (isLoading) return null;

  return (
    <section className="space-y-3">
      <h2 className="text-xs font-medium text-foreground">Pull requests</h2>
      <Card>
        <CardContent>
          <div className="flex items-center justify-between">
            <div className="space-y-0.5">
              <Label>GitHub connection for PRs</Label>
              <p className="text-xs text-muted-foreground">
                {ghStatus?.connected && ghStatus?.has_repo_scope
                  ? `Connected as @${ghStatus.github_login} — PRs will be authored by you`
                  : ghStatus?.connected && !ghStatus?.has_repo_scope
                    ? `Connected as @${ghStatus.github_login} — missing repo access, reconnect to author PRs`
                    : "Connect your GitHub account to create PRs under your name"}
              </p>
            </div>
            <div className="flex items-center gap-2">
              {ghStatus?.connected ? (
                <>
                  {!ghStatus.has_repo_scope && (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => api.githubStatus.connect()}
                    >
                      Reconnect
                    </Button>
                  )}
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => disconnectMutation.mutate()}
                    disabled={disconnectMutation.isPending}
                  >
                    Disconnect
                  </Button>
                </>
              ) : (
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => api.githubStatus.connect()}
                >
                  Connect GitHub
                </Button>
              )}
            </div>
          </div>
        </CardContent>
      </Card>
    </section>
  );
}

/* ------------------------------------------------------------------ */
/*  Page                                                              */
/* ------------------------------------------------------------------ */

export default function AccountPage() {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";

  /* ---------- Personal credentials queries ---------- */

  const { data: personalResp, isSuccess: personalCredsLoaded } = useQuery<ListResponse<UserCredentialSummary>>({
    queryKey: ["user-credentials", "personal"],
    queryFn: () => api.userCredentials.listPersonal(),
  });
  const personalCreds = useMemo(() => personalResp?.data ?? [], [personalResp?.data]);

  const { data: resolvedResp } = useQuery<ListResponse<ResolvedCredential>>({
    queryKey: ["user-credentials", "resolved"],
    queryFn: () => api.userCredentials.listResolved(),
  });
  const resolved = useMemo(() => resolvedResp?.data ?? [], [resolvedResp?.data]);

  /* ---------- Personal credentials state ---------- */

  const initialPersonalAgent = useMemo(() => {
    if (!personalCredsLoaded) return null;
    const configured = AGENT_TYPES.find(
      (a) =>
        !a.inheritsProviderKeys &&
        personalCreds.some((c) => c.provider === a.providerKey && c.configured),
    );
    return configured?.key ?? "codex";
  }, [personalCreds, personalCredsLoaded]);

  const [personalAgentType, setPersonalAgentType] = useState<string | null>(null);
  const effectivePersonalAgentType = personalAgentType ?? initialPersonalAgent ?? "codex";
  const [apiKeys, setApiKeys] = useState<Record<string, string>>({});
  const [showKeys, setShowKeys] = useState<Record<string, boolean>>({});
  const [keySaveStatus, setKeySaveStatus] = useState<Record<string, "idle" | "saving" | "success" | "error">>({});
  const [removingProvider, setRemovingProvider] = useState<string | null>(null);
  const [settingTeamDefaultProvider, setSettingTeamDefaultProvider] = useState<string | null>(null);
  const [personalCodexMethodOverride, setPersonalCodexMethodOverride] = useState<"chatgpt" | "api_key" | null>(null);
  const [showDeviceCodeModal, setShowDeviceCodeModal] = useState(false);

  const upsertMutation = useMutation({
    mutationFn: ({ provider, apiKey }: { provider: string; apiKey: string }) =>
      api.userCredentials.upsertPersonal(provider, { api_key: apiKey }),
    onSuccess: (_data, variables) => {
      queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
      setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "success" }));
      setApiKeys((prev) => ({ ...prev, [variables.provider]: "" }));
      setTimeout(() => setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "idle" })), 2000);
    },
    onError: (err, variables) => {
      captureError(err, { feature: "agent-key-save" });
      setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "error" }));
      setTimeout(() => setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "idle" })), 3000);
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (provider: string) => api.userCredentials.deletePersonal(provider),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
      setRemovingProvider(null);
    },
    onError: (error) => {
      captureError(error, { feature: "agent-key-delete" });
    },
  });

  const setTeamDefaultMutation = useMutation({
    mutationFn: ({ provider, userId }: { provider: string; userId: string }) =>
      api.userCredentials.setTeamDefault(provider, userId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
      setSettingTeamDefaultProvider(null);
    },
    onError: (error) => {
      captureError(error, { feature: "agent-key-set-team-default" });
      setSettingTeamDefaultProvider(null);
    },
  });

  const { data: codexAuthStatusResp } = useQuery({
    queryKey: ["codex-auth-status"],
    queryFn: () => api.codexAuth.status(),
    refetchInterval: false,
  });
  const codexAuthStatus = codexAuthStatusResp?.data;

  const hasCodexChatGPTConnection = codexAuthStatus?.status === "completed";
  const inferredPersonalCodexMethod: "chatgpt" | "api_key" =
    hasCodexChatGPTConnection ? "chatgpt" : "api_key";
  const personalCodexMethod = personalCodexMethodOverride ?? inferredPersonalCodexMethod;

  const disconnectMutation = useMutation({
    mutationFn: () => api.codexAuth.disconnectAll(),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["codex-auth-status"] });
    },
  });

  function handleSavePersonalKey(provider: string) {
    const key = apiKeys[provider]?.trim();
    if (!key) return;
    setKeySaveStatus((prev) => ({ ...prev, [provider]: "saving" }));
    upsertMutation.mutate({ provider, apiKey: key });
  }

  /* ---------- Render helpers ---------- */

  function renderChatGPTAuthStatus(): ReactNode {
    return (
      <div className="flex items-center gap-2">
        {codexAuthStatus?.status === "completed" ? (
          <>
            <Badge variant="outline" className="border-green-600 text-green-600">
              <CheckCircle2 className="mr-1 h-3.5 w-3.5" />
              Connected
            </Badge>
            <Button
              size="sm"
              variant="outline"
              onClick={() => disconnectMutation.mutate()}
              disabled={disconnectMutation.isPending}
            >
              {disconnectMutation.isPending ? "Disconnecting..." : "Disconnect"}
            </Button>
          </>
        ) : (
          <Button size="sm" onClick={() => setShowDeviceCodeModal(true)}>
            Sign in with ChatGPT
          </Button>
        )}
      </div>
    );
  }

  function renderAgentConfigHeader({
    title,
    badges,
    action,
  }: {
    title: string;
    badges: ReactNode;
    action?: ReactNode;
  }): ReactNode {
    return (
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium">{title}</span>
          {badges}
        </div>
        {action}
      </div>
    );
  }

  function renderCodexCredentialToggle({
    method,
    onMethodChange,
  }: {
    method: "chatgpt" | "api_key";
    onMethodChange: (value: "chatgpt" | "api_key") => void;
  }): ReactNode {
    return (
      <div className="space-y-3">
        <Label className="text-xs text-muted-foreground">Credential method</Label>
        <RadioGroup
          value={method}
          onValueChange={(value) => onMethodChange(value as "chatgpt" | "api_key")}
          className="grid gap-3 md:grid-cols-2"
        >
          <RadioCard
            value="chatgpt"
            label="Sign in with ChatGPT"
            description="Best for gpt-5.3-codex model access."
            selected={method === "chatgpt"}
            icon={<Sparkles className="h-4 w-4 text-primary" />}
            ariaLabel="Sign in with ChatGPT"
          />
          <RadioCard
            value="api_key"
            label="Use API key"
            description="Pay-as-you-go credentials with configurable model/base URL."
            selected={method === "api_key"}
            icon={<KeyRound className="h-4 w-4 text-muted-foreground" />}
            ariaLabel="Use API key"
          />
        </RadioGroup>

        {method === "chatgpt" && renderChatGPTAuthStatus()}
      </div>
    );
  }

  function renderInheritedProviderRow(providerKey: string): ReactNode {
    const cred = personalCreds.find((c) => c.provider === providerKey);
    const r = resolved.find((c) => c.provider === providerKey);
    const source = r?.source ?? "none";
    const status = keySaveStatus[providerKey] ?? "idle";
    const label = providerDisplayName(providerKey);
    const placeholder = KEY_PLACEHOLDERS[providerKey] ?? "API key";

    return (
      <div
        key={providerKey}
        data-testid={`inherited-provider-row-${providerKey}`}
        className="rounded-md border bg-muted/20 px-3 py-2.5 space-y-2"
      >
        <div className="flex items-center justify-between gap-2">
          <span className="text-xs font-medium">{label}</span>
          <Badge variant={sourceBadgeVariant(source)} className="text-xs px-1.5 py-0">
            {sourceLabel(source)}
          </Badge>
        </div>

        {cred?.configured ? (
          <div className="flex items-center justify-between gap-2">
            <span className="text-xs font-mono text-muted-foreground truncate">
              {cred.masked_key}
            </span>
            <Button
              variant="ghost"
              size="sm"
              className="text-xs text-muted-foreground shrink-0"
              onClick={() => setRemovingProvider(providerKey)}
              disabled={deleteMutation.isPending}
            >
              Remove
            </Button>
          </div>
        ) : (
          <div className="flex gap-2">
            <div className="relative flex-1">
              <Input
                type={showKeys[providerKey] ? "text" : "password"}
                placeholder={placeholder}
                value={apiKeys[providerKey] ?? ""}
                onChange={(e) =>
                  setApiKeys((prev) => ({ ...prev, [providerKey]: e.target.value }))
                }
                className="pr-9 font-mono text-xs"
                aria-label={`${label} API key`}
              />
              <button
                type="button"
                onClick={() =>
                  setShowKeys((prev) => ({ ...prev, [providerKey]: !prev[providerKey] }))
                }
                className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                aria-label={showKeys[providerKey] ? "Hide key" : "Show key"}
              >
                {showKeys[providerKey] ? (
                  <EyeOff className="h-3.5 w-3.5" />
                ) : (
                  <Eye className="h-3.5 w-3.5" />
                )}
              </button>
            </div>
            <Button
              size="sm"
              onClick={() => handleSavePersonalKey(providerKey)}
              disabled={!apiKeys[providerKey]?.trim() || status === "saving"}
            >
              {status === "saving" ? "Saving..." : "Save"}
            </Button>
          </div>
        )}

        {status === "success" && (
          <p className="text-xs text-emerald-600 dark:text-emerald-400">Key saved.</p>
        )}
        {status === "error" && (
          <p className="text-xs text-destructive">Failed to save key.</p>
        )}
      </div>
    );
  }

  function renderPersonalInheritedCard(agent: (typeof AGENT_TYPES)[number]): ReactNode {
    const configuredCount = countInheritedProvidersConfigured(resolved);
    const total = PI_INHERITED_PROVIDERS.length;
    // Show a count rather than a binary "Ready to run" — a user with only
    // OpenAI configured would see "Ready to run" on this card but still hit
    // the missing-key banner on /sessions/new when they pick an Anthropic
    // model, because the banner runs the strict per-model check. The count
    // keeps this screen honest without asserting model-specific readiness.
    const badgeVariant: "success" | "secondary" | "outline" =
      configuredCount === total ? "success" : configuredCount > 0 ? "secondary" : "outline";
    const badgeBody =
      configuredCount === 0 ? (
        "Add a key to run"
      ) : configuredCount === total ? (
        <>
          <Check className="mr-0.5 h-3 w-3" />
          Ready to run
        </>
      ) : (
        `${configuredCount} of ${total} configured`
      );

    return (
      <div className="space-y-3 border-t pt-3 mt-1">
        {renderAgentConfigHeader({
          title: agent.label,
          badges: (
            <Badge variant={badgeVariant} className="text-xs px-1.5 py-0">
              {badgeBody}
            </Badge>
          ),
        })}

        <p className="text-xs text-muted-foreground">
          {agent.label} routes to Anthropic, OpenAI, or Gemini depending on the model.
          Add a key for each provider whose models you plan to use.
        </p>

        <div className="space-y-2">
          {PI_INHERITED_PROVIDERS.map((provider) => renderInheritedProviderRow(provider))}
        </div>
      </div>
    );
  }

  function renderPersonalCredentialCard(): ReactNode {
    const agent = AGENT_TYPES.find((a) => a.key === effectivePersonalAgentType) ?? AGENT_TYPES[0];
    if (agent.inheritsProviderKeys) {
      return renderPersonalInheritedCard(agent);
    }
    const providerKey = agent.providerKey;
    const cred = personalCreds.find((c) => c.provider === providerKey);
    const status = keySaveStatus[providerKey] ?? "idle";
    const r = resolved.find((c) => c.provider === providerKey);
    const source = r?.source ?? "none";
    const isCodex = agent.key === "codex";
    const hideApiKey = isCodex && personalCodexMethod === "chatgpt";

    return (
      <div className="space-y-3 border-t pt-3 mt-1">
        {renderAgentConfigHeader({
          title: agent.label,
          badges: (
            <>
              {cred?.configured && (
                <Badge variant="success" className="text-xs px-1.5 py-0">
                  <Check className="mr-0.5 h-3 w-3" />
                  Configured
                </Badge>
              )}
              <Badge variant={sourceBadgeVariant(source)} className="text-xs px-1.5 py-0">
                {sourceLabel(source)}
              </Badge>
            </>
          ),
          action: cred?.configured ? (
            <Button
              variant="ghost"
              size="sm"
              className="text-xs text-muted-foreground"
              onClick={() => setRemovingProvider(providerKey)}
              disabled={deleteMutation.isPending}
            >
              Remove
            </Button>
          ) : undefined,
        })}

        {isCodex && renderCodexCredentialToggle({
          method: personalCodexMethod,
          onMethodChange: setPersonalCodexMethodOverride,
        })}

        {cred?.configured && cred.masked_key && !hideApiKey && (
          <p className="text-xs text-muted-foreground font-mono">
            Key: {cred.masked_key}
          </p>
        )}

        {!hideApiKey && (
          <div className="flex gap-2">
            <div className="relative flex-1">
              <Input
                type={showKeys[providerKey] ? "text" : "password"}
                placeholder={cred?.configured ? "Replace existing key..." : KEY_PLACEHOLDERS[providerKey] ?? "API key"}
                value={apiKeys[providerKey] ?? ""}
                onChange={(e) =>
                  setApiKeys((prev) => ({ ...prev, [providerKey]: e.target.value }))
                }
                className="pr-9 font-mono text-xs"
              />
              <button
                type="button"
                onClick={() =>
                  setShowKeys((prev) => ({ ...prev, [providerKey]: !prev[providerKey] }))
                }
                className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
              >
                {showKeys[providerKey] ? (
                  <EyeOff className="h-3.5 w-3.5" />
                ) : (
                  <Eye className="h-3.5 w-3.5" />
                )}
              </button>
            </div>
            <Button
              size="sm"
              onClick={() => handleSavePersonalKey(providerKey)}
              disabled={!apiKeys[providerKey]?.trim() || status === "saving"}
            >
              {status === "saving" ? "Saving..." : "Save key"}
            </Button>
          </div>
        )}

        {hideApiKey && (
          <p className="text-xs text-muted-foreground">
            API key fields are hidden while ChatGPT sign-in is selected.
          </p>
        )}

        {status === "success" && (
          <p className="text-xs text-emerald-600 dark:text-emerald-400">Key saved successfully.</p>
        )}
        {status === "error" && (
          <p className="text-xs text-destructive">Failed to save key.</p>
        )}

        {isAdmin && cred?.configured && !cred.is_team_default && user && (
          <Button
            variant="outline"
            size="sm"
            className="text-xs"
            onClick={() => setSettingTeamDefaultProvider(providerKey)}
            disabled={setTeamDefaultMutation.isPending}
          >
            <Shield className="mr-1 h-3 w-3" />
            Set as team default
          </Button>
        )}
        {cred?.is_team_default && (
          <Badge variant="secondary" className="text-xs px-1.5 py-0">
            <Shield className="mr-0.5 h-3 w-3" />
            Team default
          </Badge>
        )}
      </div>
    );
  }

  /* ---------- Render ---------- */

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="Account"
          description="Your personal preferences and credentials."
        />

        <section className="space-y-3">
          <h2 className="text-xs font-medium text-foreground">Appearance</h2>
          <Card>
            <CardContent>
              <div className="flex items-center justify-between">
                <div className="space-y-0.5">
                  <Label>Theme</Label>
                  <p className="text-xs text-muted-foreground">
                    Select your preferred color scheme
                  </p>
                </div>
                <ThemeSelect />
              </div>
            </CardContent>
          </Card>
        </section>

        <GitHubPRConnection />

        <section className="space-y-3">
          <div>
            <h2 className="text-xs font-medium text-foreground">
              Coding agent credentials
            </h2>
            <p className="text-xs text-muted-foreground mt-1">
              Your personal API keys. Personal keys are used first, falling back to organization defaults.
            </p>
          </div>

          <Card>
            <CardContent>
              <div className="space-y-3">
                <RadioGroup
                  value={effectivePersonalAgentType}
                  onValueChange={setPersonalAgentType}
                  className="grid grid-cols-3 gap-3"
                >
                  {AGENT_TYPES.map((agent) => {
                    // Pi has no personal credential of its own — surface a check
                    // when any inherited provider key is resolved so users can
                    // tell at a glance that a Pi run will work.
                    const configured = agent.inheritsProviderKeys
                      ? hasAnyInheritedProviderConfigured(resolved)
                      : personalCreds.some((c) => c.provider === agent.providerKey && c.configured);
                    return (
                      <RadioCard
                        key={agent.key}
                        value={agent.key}
                        label={agent.label}
                        selected={effectivePersonalAgentType === agent.key}
                        icon={configured ? <Check className="h-3.5 w-3.5 text-emerald-600 dark:text-emerald-400" /> : undefined}
                      />
                    );
                  })}
                </RadioGroup>

                {personalCredsLoaded && renderPersonalCredentialCard()}
              </div>
            </CardContent>
          </Card>
        </section>
      </div>

      {/* Set as Team Default Confirmation Dialog */}
      <AlertDialog
        open={!!settingTeamDefaultProvider}
        onOpenChange={(open) => !open && setSettingTeamDefaultProvider(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Set as team default</AlertDialogTitle>
            {settingTeamDefaultProvider && (
              <AlertDialogDescription>
                Your personal {providerDisplayName(settingTeamDefaultProvider)} key will become the team default. Other members without their own personal key will use your credential for sessions. You can change or remove the team default at any time.
              </AlertDialogDescription>
            )}
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              disabled={!user || setTeamDefaultMutation.isPending}
              onClick={(event) => {
                event.preventDefault();
                if (settingTeamDefaultProvider && user) {
                  setTeamDefaultMutation.mutate({
                    provider: settingTeamDefaultProvider,
                    userId: user.id,
                  });
                }
              }}
            >
              Set as team default
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Remove Personal Key Dialog */}
      <AlertDialog open={!!removingProvider} onOpenChange={(open) => !open && setRemovingProvider(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove API key</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to remove your {providerDisplayName(removingProvider ?? "")} API key?
              Sessions will fall back to the team default or organization key.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (removingProvider) deleteMutation.mutate(removingProvider);
              }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Codex Device Code Modal */}
      {showDeviceCodeModal && (
        <CodexDeviceCodeModal
          onClose={() => setShowDeviceCodeModal(false)}
          onConnected={() => {
            queryClient.invalidateQueries({ queryKey: ["codex-auth-status"] });
            setShowDeviceCodeModal(false);
          }}
        />
      )}
    </PageContainer>
  );
}
