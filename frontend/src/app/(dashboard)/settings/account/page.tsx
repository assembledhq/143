"use client";

import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Plus, ShieldCheck, Terminal, Trash2, type LucideIcon } from "lucide-react";
import { notify as toast } from "@/lib/notify";
import { api } from "@/lib/api";
import { apiKeyHelp, PERSONAL_PROVIDER_OPTIONS, personalProviderToAgent, type PersonalProvider } from "@/lib/coding-auth-metadata";
import { captureError } from "@/lib/errors";
import { APIKeyHelpTooltip } from "@/components/api-key-help-tooltip";
import { ClaudeCodeAuthModal } from "@/components/claude-code-auth-modal";
import { CodexDeviceCodeModal } from "@/components/codex-device-code-modal";
import { CodingAuthDialog } from "@/components/coding-auth-dialog";
import { CopyButton } from "@/components/copy-button";
import { EmptyState } from "@/components/empty-state";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ThemeSelect } from "@/components/theme-select";
import { useAuth } from "@/hooks/use-auth";
import { AGENTS } from "@/lib/agents";
import {
  CODING_AGENT_REASONING_OPTIONS_BY_AGENT,
  getCodingAgentReasoningDefaultsFromSettings,
  toCodingAgentReasoningEffort,
} from "@/lib/coding-agent-reasoning";
import type {
  CodingAuthAgent,
  CodingAuthStatus,
  CodingCredentialSummary,
  CLIToken,
  ListResponse,
  UserSettingsUpdateRequest,
} from "@/lib/types";

// agentLabel renders the human-readable agent name. The unified API exposes
// rows tagged by agent (codex / claude_code / gemini_cli / amp / pi) so the
// translation from provider strings the legacy personal page used is no
// longer needed.
function agentLabel(agent: CodingAuthAgent | string) {
  switch (agent) {
    case "codex":
      return "Codex";
    case "claude_code":
      return "Claude Code";
    case "gemini_cli":
      return "Gemini CLI";
    case "amp":
      return "Amp";
    case "pi":
      return "Pi";
    default:
      return agent;
  }
}

function authTypeLabel(authType: string) {
  return authType === "subscription" ? "Subscription" : "API key";
}

function statusLabel(status: CodingAuthStatus | string | undefined) {
  switch (status) {
    case "healthy":
      return "Healthy";
    case "rate_limited":
      return "Rate limited";
    case "needs_reauth":
      return "Needs reauth";
    case "invalid":
      return "Invalid";
    default:
      return "Unknown";
  }
}

const cliDateFormatter = new Intl.DateTimeFormat("en-US", {
  month: "short",
  day: "numeric",
  year: "numeric",
});

function formatCliDate(value: string | null | undefined): string {
  if (!value) return "Never";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "Never";
  return cliDateFormatter.format(date);
}

function cliDeviceLabel(token: CLIToken): string {
  return token.device_name?.trim() || "Unnamed device";
}

function CredentialList({
  rows,
  emptyState,
  readOnly = false,
  onDelete,
}: {
  rows: CodingCredentialSummary[];
  emptyState: {
    icon: LucideIcon;
    title: string;
    description: string;
    action?: {
      label: string;
      onClick: () => void;
    };
  };
  readOnly?: boolean;
  onDelete?: (id: string) => void;
}) {
  if (rows.length === 0) {
    return (
      <EmptyState
        variant="inline"
        icon={emptyState.icon}
        title={emptyState.title}
        description={emptyState.description}
        action={emptyState.action}
      />
    );
  }

  return (
    <div className="divide-y divide-border/60">
      {rows.map((row, idx) => (
        <div key={row.id} className="space-y-3 px-4 py-4 md:hidden">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0 space-y-1">
              <div className="text-xs font-medium text-foreground">
                {agentLabel(row.agent)}
                {row.is_default ? (
                  <Badge variant="secondary" className="ml-2">Default</Badge>
                ) : null}
              </div>
              <div className="text-xs text-muted-foreground">{row.label}</div>
            </div>
            <Badge variant="outline">{statusLabel(row.status)}</Badge>
          </div>
          <dl className="grid grid-cols-2 gap-3 text-xs">
            <div className="space-y-1">
              <dt className="font-medium text-muted-foreground">Priority</dt>
              <dd>{idx + 1}</dd>
            </div>
            <div className="space-y-1">
              <dt className="font-medium text-muted-foreground">Auth type</dt>
              <dd>{authTypeLabel(row.auth_type)}</dd>
            </div>
          </dl>
          {row.usage_note && row.usage_note !== row.label ? (
            <div className="space-y-1">
              <div className="text-xs font-medium text-muted-foreground">Notes</div>
              <div className="text-xs text-muted-foreground">{row.usage_note}</div>
            </div>
          ) : null}
          {!readOnly && onDelete ? (
            <Button variant="ghost" size="sm" onClick={() => onDelete(row.id)}>
              <Trash2 className="mr-2 h-4 w-4" />
              Disable
            </Button>
          ) : null}
        </div>
      ))}

      <div className="hidden md:block">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-12">#</TableHead>
              <TableHead>Agent</TableHead>
              <TableHead>Auth type</TableHead>
              <TableHead>Notes</TableHead>
              <TableHead>Status</TableHead>
              {!readOnly ? <TableHead className="w-24 text-right">Action</TableHead> : null}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row, idx) => (
              <TableRow key={row.id}>
                <TableCell className="text-muted-foreground">{idx + 1}</TableCell>
                <TableCell>
                  {agentLabel(row.agent)}
                  {row.is_default ? (
                    <Badge variant="secondary" className="ml-2">Default</Badge>
                  ) : null}
                </TableCell>
                <TableCell>{authTypeLabel(row.auth_type)}</TableCell>
                <TableCell>
                  <div>{row.label}</div>
                  {row.usage_note && row.usage_note !== row.label ? (
                    <div className="text-xs text-muted-foreground">{row.usage_note}</div>
                  ) : null}
                </TableCell>
                <TableCell>
                  <Badge variant="outline">{statusLabel(row.status)}</Badge>
                </TableCell>
                {!readOnly ? (
                  <TableCell className="text-right">
                    <Button variant="ghost" size="sm" onClick={() => onDelete?.(row.id)}>
                      <Trash2 className="mr-2 h-4 w-4" />
                      Disable
                    </Button>
                  </TableCell>
                ) : null}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}

// Auth type for the personal Add-auth modal. Mirrors the org-side flow in
// /settings/agent: subscription auth runs the OAuth handshake against the
// upstream provider; api_key takes a static key. The modal exposes the
// selector only when the chosen provider's supportsSubscription is true
// (Codex + Claude Code today).
type PersonalAuthType = "subscription" | "api_key";

export default function AccountPage() {
  const { user } = useAuth();
  const queryClient = useQueryClient();
  const [addOpen, setAddOpen] = useState(false);
  const [provider, setProvider] = useState<PersonalProvider>("openai");
  const [authType, setAuthType] = useState<PersonalAuthType>("subscription");
  const [apiKey, setApiKey] = useState("");
  const [authLabel, setAuthLabel] = useState("");
  // Subscription OAuth modal dispatch — only one is open at a time.
  // The dialog itself closes when these open so the OAuth modal owns the
  // user's attention during the device-code or paste-back flow.
  const [showCodexModal, setShowCodexModal] = useState(false);
  const [showClaudeModal, setShowClaudeModal] = useState(false);
  const [pendingDefaultModel, setPendingDefaultModel] = useState<string | null>(null);
  const [pendingReasoningDefaults, setPendingReasoningDefaults] = useState<UserSettingsUpdateRequest["coding_agent_reasoning_defaults"] | null>(null);
  const reasoningSaveInFlightRef = useRef(false);
  const queuedReasoningDefaultsRef = useRef<UserSettingsUpdateRequest["coding_agent_reasoning_defaults"] | null>(null);

  // Personal stack — the user's own credentials, ordered by priority.
  const { data: personalResp } = useQuery<ListResponse<CodingCredentialSummary>>({
    queryKey: ["coding-credentials", "personal"],
    queryFn: () => api.codingCredentials.list("personal"),
  });
  // Org fallback — read-only here; the admin manages it on /settings/agent.
  const { data: orgResp } = useQuery<ListResponse<CodingCredentialSummary>>({
    queryKey: ["coding-credentials", "org"],
    queryFn: () => api.codingCredentials.list("org"),
  });
  const {
    data: cliTokensResp,
    isLoading: cliTokensLoading,
    isError: cliTokensError,
  } = useQuery<ListResponse<CLIToken>>({
    queryKey: ["cli-tokens"],
    queryFn: () => api.cliTokens.list(),
  });

  const personalRows = personalResp?.data ?? [];
  const orgRows = orgResp?.data ?? [];
  const cliTokens = cliTokensResp?.data ?? [];
  const showCLICard = Boolean(cliTokensResp) && !cliTokensError;
  const cliInstallCommand = `curl -fsSL ${
    typeof window === "undefined" ? "http://localhost:3000" : window.location.origin
  }/install.sh | sh`;

  const storedReasoningDefaults = getCodingAgentReasoningDefaultsFromSettings(user?.settings);
  const effectiveReasoningDefaults = pendingReasoningDefaults ?? storedReasoningDefaults;
  const effectiveDefaultModel = pendingDefaultModel ?? user?.settings?.coding_agent_model_default ?? "";
  const hasEffectiveReasoningDefaults = Object.keys(effectiveReasoningDefaults).length > 0;
  // Not editable on this page (toggled from the diff viewer toolbar), but it
  // must ride along with every settings PATCH or it would be wiped.
  const storedDiffFullScreen = user?.settings?.diff_viewer_full_screen ?? false;

  const createMutation = useMutation({
    mutationFn: () =>
      api.codingCredentials.create({
        scope: "personal",
        agent: personalProviderToAgent(provider),
        auth_type: "api_key",
        label: authLabel.trim() || undefined,
        api_key: apiKey,
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["coding-credentials"] });
      // TODO(unified-credentials cleanup PR): drop the legacy invalidations
      // once /settings/agent and other surfaces stop reading user-credentials
      // / credentials.resolved.
      void queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
      void queryClient.invalidateQueries({ queryKey: ["credentials", "resolved"] });
      setApiKey("");
      setAuthLabel("");
      setAddOpen(false);
      toast.success("Personal auth saved");
    },
    onError: (error) => {
      captureError(error, { feature: "personal-coding-auth-save" });
      toast.error("Could not save personal auth");
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.codingCredentials.delete(id, "personal"),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["coding-credentials"] });
      // TODO(unified-credentials cleanup PR): drop the legacy invalidations.
      void queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
      void queryClient.invalidateQueries({ queryKey: ["credentials", "resolved"] });
      toast.success("Personal auth removed");
    },
    onError: (error) => {
      captureError(error, { feature: "personal-coding-auth-delete" });
      // Force a refetch so any divergence between the cached list and the
      // server's actual state (e.g. a concurrent disable from another tab)
      // is reconciled instead of silently persisting until the next nav.
      void queryClient.invalidateQueries({ queryKey: ["coding-credentials"] });
      toast.error("Could not remove personal auth");
    },
  });

  const revokeCliTokenMutation = useMutation({
    mutationFn: (id: string) => api.cliTokens.revoke(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["cli-tokens"] });
      toast.success("CLI session revoked");
    },
    onError: (error) => {
      captureError(error, { feature: "cli-token-revoke" });
      toast.error("Could not revoke CLI session");
    },
  });

  const updateReasoningDefaultsMutation = useMutation({
    // PATCH replaces the whole settings document, so every settings mutation
    // on this page must carry the fields it isn't editing.
    mutationFn: (defaults: UserSettingsUpdateRequest["coding_agent_reasoning_defaults"]) =>
      api.auth.updateSettings({
        ...(effectiveDefaultModel ? { coding_agent_model_default: effectiveDefaultModel } : {}),
        ...(defaults && Object.keys(defaults).length > 0 ? { coding_agent_reasoning_defaults: defaults } : {}),
        ...(storedDiffFullScreen ? { diff_viewer_full_screen: true } : {}),
      }),
    onMutate: (defaults) => {
      reasoningSaveInFlightRef.current = true;
      setPendingReasoningDefaults(defaults);
    },
    onSuccess: (response) => {
      queryClient.setQueryData(["auth", "me"], { data: response.data });
      const queuedDefaults = queuedReasoningDefaultsRef.current;
      if (queuedDefaults) {
        queuedReasoningDefaultsRef.current = null;
        updateReasoningDefaultsMutation.mutate(queuedDefaults);
        return;
      }
      reasoningSaveInFlightRef.current = false;
      setPendingReasoningDefaults(null);
      toast.success("Coding agent defaults saved");
    },
    onError: (error) => {
      const queuedDefaults = queuedReasoningDefaultsRef.current;
      if (queuedDefaults) {
        queuedReasoningDefaultsRef.current = null;
        updateReasoningDefaultsMutation.mutate(queuedDefaults);
        return;
      }
      reasoningSaveInFlightRef.current = false;
      captureError(error, { feature: "personal-coding-agent-defaults-save" });
      setPendingReasoningDefaults(null);
      toast.error("Could not save coding agent defaults");
    },
  });

  function saveReasoningDefaults(defaults: UserSettingsUpdateRequest["coding_agent_reasoning_defaults"]) {
    setPendingReasoningDefaults(defaults);
    if (reasoningSaveInFlightRef.current) {
      queuedReasoningDefaultsRef.current = defaults;
      return;
    }
    updateReasoningDefaultsMutation.mutate(defaults);
  }

  const updateDefaultModelMutation = useMutation({
    mutationFn: (model: string) =>
      api.auth.updateSettings({
        ...(model ? { coding_agent_model_default: model } : {}),
        ...(hasEffectiveReasoningDefaults ? { coding_agent_reasoning_defaults: effectiveReasoningDefaults } : {}),
        ...(storedDiffFullScreen ? { diff_viewer_full_screen: true } : {}),
      }),
    onMutate: (model) => {
      setPendingDefaultModel(model);
    },
    onSuccess: (response) => {
      queryClient.setQueryData(["auth", "me"], { data: response.data });
      setPendingDefaultModel(null);
      toast.success("Coding agent defaults saved");
    },
    onError: (error) => {
      captureError(error, { feature: "personal-coding-agent-default-model-save" });
      setPendingDefaultModel(null);
      toast.error("Could not save coding agent defaults");
    },
  });

  // The selected provider's metadata drives whether the auth-type selector
  // is visible. For providers that don't ship a subscription OAuth flow
  // (Gemini CLI / Amp / Pi), we coerce auth_type to "api_key" so the modal
  // doesn't render a dead radio group.
  const selectedProviderOption = PERSONAL_PROVIDER_OPTIONS.find((o) => o.key === provider) ?? PERSONAL_PROVIDER_OPTIONS[0];
  const showAuthTypeSelector = selectedProviderOption.supportsSubscription;
  const effectiveAuthType: PersonalAuthType = showAuthTypeSelector ? authType : "api_key";

  // Default label shown as the modal placeholder. Mirrors the admin
  // /settings/agent generated-label format ("Codex subscription" /
  // "Claude Code API key" / etc) so the two flows feel consistent.
  function defaultLabelFor(p: PersonalProvider, type: PersonalAuthType): string {
    const agent = personalProviderToAgent(p);
    const base = agent === "codex" ? "Codex"
      : agent === "claude_code" ? "Claude Code"
      : agent === "gemini_cli" ? "Gemini CLI"
      : agent === "amp" ? "Amp"
      : agent === "pi" ? "Pi"
      : agent;
    return type === "subscription" ? `${base} subscription` : `${base} API key`;
  }
  const generatedLabel = authLabel.trim() || defaultLabelFor(provider, effectiveAuthType);

  function resetModalState() {
    setApiKey("");
    setAuthLabel("");
    setProvider("openai");
    setAuthType("subscription");
  }

  function closeAddModal() {
    setAddOpen(false);
    resetModalState();
  }

  // handlePrimary routes the modal's primary action to either the API-key
  // POST or the subscription OAuth modal, depending on the selected
  // auth_type. The OAuth modals invalidate query caches on success so the
  // personal stack table refreshes once the subscription is active.
  function handlePrimary() {
    if (effectiveAuthType === "subscription") {
      const agent = personalProviderToAgent(provider);
      if (agent === "codex") {
        setShowCodexModal(true);
        setAddOpen(false);
        return;
      }
      if (agent === "claude_code") {
        setShowClaudeModal(true);
        setAddOpen(false);
        return;
      }
    }
    createMutation.mutate();
  }

  return (
    <PageContainer>
      <div className="space-y-8 pt-2">
        <PageHeader
          title="My settings"
          description="Manage your personal coding agent auths and appearance. Personal auths run before any organization fallback."
          action={(
            <Button onClick={() => setAddOpen(true)}>
              <Plus className="mr-2 h-4 w-4" />
              Add auth
            </Button>
          )}
        />

        {showCLICard && (
          <Card>
            <CardHeader>
              <CardTitle role="heading" aria-level={2} className="flex items-center gap-2">
                <Terminal className="h-4 w-4 text-muted-foreground" />
                143-tools CLI
              </CardTitle>
              <CardDescription>
                Install the local CLI on this machine, then sign in with GitHub to use this org from local coding agents.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4 pb-6">
              <div className="space-y-2 rounded-md border border-border bg-muted/30 p-3">
                <div className="flex items-center justify-between gap-3">
                  <p className="text-xs font-medium text-foreground">Install command</p>
                  <CopyButton value={cliInstallCommand} label="Copy CLI install command" />
                </div>
                <pre className="overflow-x-auto rounded-md bg-background px-3 py-2 text-xs text-foreground">
                  <code>{cliInstallCommand}</code>
                </pre>
              </div>

              <div className="space-y-2">
                <h3 className="text-xs font-medium text-foreground">CLI sessions</h3>
                {cliTokensLoading ? (
                  <div className="rounded-md border border-border px-3 py-3 text-xs text-muted-foreground">
                    Loading CLI sessions...
                  </div>
                ) : cliTokens.length === 0 ? (
                  <div className="rounded-md border border-border px-3 py-3 text-xs text-muted-foreground">
                    No CLI sessions yet.
                  </div>
                ) : (
                  <div className="divide-y divide-border rounded-md border border-border">
                    {cliTokens.map((token) => {
                      const device = cliDeviceLabel(token);
                      return (
                        <div key={token.id} className="flex flex-col gap-3 px-3 py-3 sm:flex-row sm:items-center sm:justify-between">
                          <div className="min-w-0 space-y-1">
                            <div className="flex flex-wrap items-center gap-2">
                              <span className="text-xs font-medium text-foreground">{device}</span>
                              <Badge variant="outline" className="font-mono">{token.token_prefix}</Badge>
                            </div>
                            <div className="text-xs text-muted-foreground">
                              Last used {formatCliDate(token.last_used_at)} · Expires {formatCliDate(token.expires_at)}
                            </div>
                          </div>
                          <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            className="w-full justify-start text-destructive hover:text-destructive sm:w-auto"
                            disabled={revokeCliTokenMutation.isPending}
                            aria-label={`Revoke CLI session ${device}`}
                            onClick={() => revokeCliTokenMutation.mutate(token.id)}
                          >
                            <Trash2 className="mr-2 h-4 w-4" />
                            Revoke
                          </Button>
                        </div>
                      );
                    })}
                  </div>
                )}
              </div>
            </CardContent>
          </Card>
        )}

        <Card>
          <CardHeader>
            <CardTitle>My coding agents</CardTitle>
            <CardDescription>
              Your personal stack runs ahead of any organization fallback. Add as many as you need — the resolver picks the highest-priority active row.
            </CardDescription>
          </CardHeader>
          <CardContent className="px-0 pb-6">
            <CredentialList
              rows={personalRows}
              emptyState={{
                icon: KeyRound,
                title: "No personal auths yet",
                description: "Add a personal auth to use your own subscription before org fallback.",
                action: {
                  label: "Add auth",
                  onClick: () => setAddOpen(true),
                },
              }}
              onDelete={(id) => deleteMutation.mutate(id)}
            />
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Org fallback</CardTitle>
            <CardDescription>
              Read-only. Contact an admin to change org auths.
            </CardDescription>
          </CardHeader>
          <CardContent className="px-0 pb-6">
            <CredentialList
              rows={orgRows}
              emptyState={{
                icon: ShieldCheck,
                title: "No org fallback yet",
                description: "Ask an admin to add an org-level fallback so sessions have shared credentials when personal auths are unavailable.",
              }}
              readOnly
            />
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Coding agent defaults</CardTitle>
          </CardHeader>
          <CardContent className="pb-6">
            <div className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="default-coding-agent-model">Default model</Label>
                <Select
                  value={effectiveDefaultModel || "__default__"}
                  onValueChange={(value) => updateDefaultModelMutation.mutate(value === "__default__" ? "" : value)}
                >
                  <SelectTrigger
                    id="default-coding-agent-model"
                    aria-label="Default coding-agent model"
                    className="w-full sm:w-[260px]"
                  >
                    <SelectValue placeholder="Default" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="__default__">Default</SelectItem>
                    {AGENTS.map((agent) => (
                      <SelectGroup key={agent.key}>
                        <SelectLabel>{agent.label}</SelectLabel>
                        {agent.models.map((model) => (
                          <SelectItem key={model} value={model}>
                            {model}
                          </SelectItem>
                        ))}
                      </SelectGroup>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              {Object.entries(CODING_AGENT_REASONING_OPTIONS_BY_AGENT).map(([agentType, config]) => {
                const defaultReasoning = effectiveReasoningDefaults[agentType as "codex" | "claude_code"] ?? "";

                return (
                  <div key={agentType} className="space-y-2">
                    <Label htmlFor={`default-coding-agent-reasoning-${agentType}`}>{config.label} default reasoning level</Label>
                    <Select
                      value={defaultReasoning || "__default__"}
                      onValueChange={(value) => {
                        const nextValue = value === "__default__" ? "" : toCodingAgentReasoningEffort(value);
                        const nextDefaults = { ...effectiveReasoningDefaults };
                        if (nextValue) {
                          nextDefaults[agentType as "codex" | "claude_code"] = nextValue;
                        } else {
                          delete nextDefaults[agentType as "codex" | "claude_code"];
                        }
                        saveReasoningDefaults(nextDefaults);
                      }}
                    >
                      <SelectTrigger
                        id={`default-coding-agent-reasoning-${agentType}`}
                        aria-label={`${config.label} default coding-agent reasoning`}
                        className="w-full sm:w-[220px]"
                      >
                        <SelectValue placeholder="Default" />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="__default__">Default</SelectItem>
                        {config.options.map((option) => (
                          <SelectItem key={option.value} value={option.value}>
                            {option.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                );
              })}
              <p className="text-xs text-muted-foreground">
                Used as the default for supported coding agents in the session composer. You can still override it per prompt.
              </p>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Appearance</CardTitle>
          </CardHeader>
          <CardContent className="pb-6">
            <ThemeSelect />
          </CardContent>
        </Card>
      </div>

      <CodingAuthDialog
        open={addOpen}
        onOpenChange={(open) => {
          if (!open) closeAddModal();
          else setAddOpen(true);
        }}
        title="Add auth"
        description="Add a personal subscription or API key — your personal stack runs ahead of the organization fallback."
        providerOptions={PERSONAL_PROVIDER_OPTIONS}
        provider={provider}
        onProviderChange={(value) => {
          const next = value as PersonalProvider;
          setProvider(next);
          // When switching to a provider that doesn't support subscription
          // auth, reset the radio so the modal doesn't carry a dead value.
          const opt = PERSONAL_PROVIDER_OPTIONS.find((o) => o.key === next);
          if (!opt?.supportsSubscription) {
            setAuthType("api_key");
          } else {
            setAuthType("subscription");
          }
        }}
        primaryLabel={effectiveAuthType === "subscription" ? "Continue" : "Save auth"}
        onPrimary={handlePrimary}
        primaryDisabled={effectiveAuthType === "api_key" && !apiKey.trim()}
        onCancel={closeAddModal}
      >
        {showAuthTypeSelector ? (
          <div className="space-y-2">
            <Label>Auth type</Label>
            <RadioGroup
              value={effectiveAuthType}
              onValueChange={(value) => setAuthType(value as PersonalAuthType)}
              className="grid gap-3 md:grid-cols-2"
            >
              <Label htmlFor="personal-auth-subscription" className="flex cursor-pointer items-start gap-3 rounded-xl border border-border p-4">
                <RadioGroupItem value="subscription" id="personal-auth-subscription" />
                <div className="space-y-1">
                  <div className="font-medium text-sm">Subscription</div>
                  <p className="text-xs text-muted-foreground">
                    Sign in to your existing Codex or Claude subscription.
                  </p>
                </div>
              </Label>
              <Label htmlFor="personal-auth-api-key" className="flex cursor-pointer items-start gap-3 rounded-xl border border-border p-4">
                <RadioGroupItem value="api_key" id="personal-auth-api-key" />
                <div className="space-y-1">
                  <div className="font-medium text-sm">API key</div>
                  <p className="text-xs text-muted-foreground">
                    Paste a key for pay-as-you-go billing or service accounts.
                  </p>
                </div>
              </Label>
            </RadioGroup>
          </div>
        ) : null}

        <div className="space-y-2">
          <Label htmlFor="personal-auth-label">Label</Label>
          <Input
            id="personal-auth-label"
            value={authLabel}
            onChange={(event) => setAuthLabel(event.target.value)}
            placeholder={`Optional — defaults to "${defaultLabelFor(provider, effectiveAuthType)}"`}
          />
        </div>

        {effectiveAuthType === "api_key" ? (
          <div className="space-y-2">
            <Label htmlFor="personal-api-key" className="flex items-center gap-2">
              API key
              <APIKeyHelpTooltip
                ariaLabel={`Where to get a ${apiKeyHelp(provider).label} API key`}
                description={apiKeyHelp(provider).description}
                href={apiKeyHelp(provider).href}
                linkLabel={apiKeyHelp(provider).linkLabel}
              />
            </Label>
            <Input
              id="personal-api-key"
              type="password"
              value={apiKey}
              onChange={(event) => setApiKey(event.target.value)}
              placeholder={
                provider === "anthropic"
                  ? "sk-ant-..."
                  : provider === "gemini"
                    ? "AIza..."
                    : provider === "amp"
                      ? "amp_..."
                      : provider === "pi"
                        ? "pi_..."
                        : "sk-..."
              }
            />
          </div>
        ) : null}
      </CodingAuthDialog>

      {showCodexModal ? (
        <CodexDeviceCodeModal
          label={generatedLabel}
          scope="personal"
          onClose={() => {
            setShowCodexModal(false);
            resetModalState();
          }}
          onConnected={() => {
            setShowCodexModal(false);
            resetModalState();
            // Invalidate the personal stack so the new subscription appears.
            void queryClient.invalidateQueries({ queryKey: ["coding-credentials"] });
            // Legacy keys that still feed parts of the UI during the
            // unified-credentials migration window.
            void queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
            void queryClient.invalidateQueries({ queryKey: ["credentials", "resolved"] });
            toast.success("Personal subscription connected");
          }}
        />
      ) : null}

      {showClaudeModal ? (
        <ClaudeCodeAuthModal
          label={generatedLabel}
          scope="personal"
          onClose={() => {
            setShowClaudeModal(false);
            resetModalState();
          }}
          onConnected={() => {
            setShowClaudeModal(false);
            resetModalState();
            void queryClient.invalidateQueries({ queryKey: ["coding-credentials"] });
            void queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
            void queryClient.invalidateQueries({ queryKey: ["credentials", "resolved"] });
            toast.success("Personal subscription connected");
          }}
        />
      ) : null}
    </PageContainer>
  );
}
