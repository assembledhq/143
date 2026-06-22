"use client";

import { useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Plus, ShieldCheck, Trash2, type LucideIcon } from "lucide-react";
import { notify as toast } from "@/lib/notify";
import { api } from "@/lib/api";
import { apiKeyHelp, OPENCODE_BACKING_PROVIDER_OPTIONS, openCodeAgentDefaults, openCodeDefaultModelForBackingProvider, openCodeModelsForBackingProvider, PERSONAL_PROVIDER_OPTIONS, personalProviderToAgent, type OpenCodeBackingProvider, type PersonalProvider } from "@/lib/coding-auth-metadata";
import { captureError } from "@/lib/errors";
import { APIKeyHelpTooltip } from "@/components/api-key-help-tooltip";
import { ClaudeCodeAuthModal } from "@/components/claude-code-auth-modal";
import { CLISessionsCard } from "@/components/cli-sessions-card";
import { CodexDeviceCodeModal } from "@/components/codex-device-code-modal";
import { CodingAuthDialog } from "@/components/coding-auth-dialog";
import { EmptyState } from "@/components/empty-state";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { SettingsLastActivity } from "@/components/settings/settings-last-activity";
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
  ListResponse,
  UserSettingsUpdateRequest,
} from "@/lib/types";

// agentLabel renders the human-readable agent name. The unified API exposes
// rows tagged by agent (codex / claude_code / amp / pi / opencode) so the
// translation from provider strings the legacy personal page used is no
// longer needed.
function agentLabel(agent: CodingAuthAgent | string) {
  switch (agent) {
    case "codex":
      return "Codex";
    case "claude_code":
      return "Claude Code";
    case "amp":
      return "Amp";
    case "pi":
      return "Pi";
    case "opencode":
      return "OpenCode";
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

// Local display map of reasoning defaults (no nulls) vs the wire patch sent
// to the merge-patch settings endpoint (null clears an agent's entry).
type ReasoningDefaults = ReturnType<typeof getCodingAgentReasoningDefaultsFromSettings>;
type ReasoningDefaultsPatch = NonNullable<UserSettingsUpdateRequest["coding_agent_reasoning_defaults"]>;

export default function AccountPage() {
  const { user } = useAuth();
  const queryClient = useQueryClient();
  const [addOpen, setAddOpen] = useState(false);
  const [provider, setProvider] = useState<PersonalProvider>("openai");
  const [authType, setAuthType] = useState<PersonalAuthType>("subscription");
  const [apiKey, setApiKey] = useState("");
  const [authLabel, setAuthLabel] = useState("");
  const [openCodeBackingProvider, setOpenCodeBackingProvider] = useState<OpenCodeBackingProvider>("opencode");
  const [openCodeModel, setOpenCodeModel] = useState<string>(openCodeDefaultModelForBackingProvider("opencode"));
  const [openCodeCustomModel, setOpenCodeCustomModel] = useState("");
  const openCodeModelOptions = useMemo(() => openCodeModelsForBackingProvider(openCodeBackingProvider), [openCodeBackingProvider]);
  // Subscription OAuth modal dispatch — only one is open at a time.
  // The dialog itself closes when these open so the OAuth modal owns the
  // user's attention during the device-code or paste-back flow.
  const [showCodexModal, setShowCodexModal] = useState(false);
  const [showClaudeModal, setShowClaudeModal] = useState(false);
  const [pendingDefaultModel, setPendingDefaultModel] = useState<string | null>(null);
  const [pendingReasoningDefaults, setPendingReasoningDefaults] = useState<ReasoningDefaults | null>(null);
  const reasoningSaveInFlightRef = useRef(false);
  const queuedReasoningDefaultsRef = useRef<ReasoningDefaultsPatch | null>(null);

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

  const personalRows = personalResp?.data ?? [];
  const orgRows = orgResp?.data ?? [];

  const storedReasoningDefaults = getCodingAgentReasoningDefaultsFromSettings(user?.settings);
  const effectiveReasoningDefaults = pendingReasoningDefaults ?? storedReasoningDefaults;
  const effectiveDefaultModel = pendingDefaultModel ?? user?.settings?.coding_agent_model_default ?? "";

  const createMutation = useMutation({
    mutationFn: () =>
      api.codingCredentials.create({
        scope: "personal",
        agent: personalProviderToAgent(provider),
        auth_type: "api_key",
        label: authLabel.trim() || undefined,
        api_key: apiKey,
        ...(provider === "opencode" ? { api_type: openCodeBackingProvider } : {}),
        ...(provider === "opencode" ? { agent_defaults: openCodeAgentDefaults(openCodeModel, openCodeCustomModel) } : {}),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["coding-credentials"] });
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

  const updateReasoningDefaultsMutation = useMutation({
    // The settings endpoint is a JSON merge patch, so each save carries only
    // the per-agent entries that changed (null clears an entry back to the
    // built-in default).
    mutationFn: (patch: ReasoningDefaultsPatch) =>
      api.auth.updateSettings({ coding_agent_reasoning_defaults: patch }),
    onMutate: () => {
      reasoningSaveInFlightRef.current = true;
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

  function saveReasoningDefault(agentType: keyof ReasoningDefaultsPatch, effort: ReasoningDefaults[keyof ReasoningDefaults] | null) {
    const nextDefaults = { ...effectiveReasoningDefaults };
    if (effort) {
      nextDefaults[agentType] = effort;
    } else {
      delete nextDefaults[agentType];
    }
    setPendingReasoningDefaults(nextDefaults);
    if (reasoningSaveInFlightRef.current) {
      // Merge queued entries per agent so rapid edits to different agents
      // all land instead of the last patch replacing the queue.
      queuedReasoningDefaultsRef.current = { ...queuedReasoningDefaultsRef.current, [agentType]: effort };
      return;
    }
    updateReasoningDefaultsMutation.mutate({ [agentType]: effort });
  }

  const updateDefaultModelMutation = useMutation({
    // Merge-patch endpoint: send just the model, with null clearing the
    // stored default when the user picks "Default".
    mutationFn: (model: string) =>
      api.auth.updateSettings({ coding_agent_model_default: model || null }),
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
  // (Amp / Pi / OpenCode), we coerce auth_type to "api_key" so the modal
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
      : agent === "amp" ? "Amp"
      : agent === "pi" ? "Pi"
      : agent === "opencode" ? "OpenCode"
      : agent;
    return type === "subscription" ? `${base} subscription` : `${base} API key`;
  }
  const generatedLabel = authLabel.trim() || defaultLabelFor(provider, effectiveAuthType);

  function resetModalState() {
    setApiKey("");
    setAuthLabel("");
    setProvider("openai");
    setAuthType("subscription");
    setOpenCodeBackingProvider("opencode");
    setOpenCodeModel(openCodeDefaultModelForBackingProvider("opencode"));
    setOpenCodeCustomModel("");
  }

  function updateOpenCodeBackingProvider(value: OpenCodeBackingProvider) {
    setOpenCodeBackingProvider(value);
    setOpenCodeModel(openCodeDefaultModelForBackingProvider(value));
    setOpenCodeCustomModel("");
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

        <CLISessionsCard />

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
                        saveReasoningDefault(agentType as "codex" | "claude_code", nextValue || null);
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

        <SettingsLastActivity
          scopes={[{ resource_type: "credential" }, { resource_type: "user" }]}
          title="Account settings activity"
        />
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
          <>
            {provider === "opencode" ? (
              <>
                <div className="space-y-2">
                  <Label htmlFor="personal-opencode-backing-provider">OpenCode provider</Label>
                  <Select value={openCodeBackingProvider} onValueChange={(value) => updateOpenCodeBackingProvider(value as OpenCodeBackingProvider)}>
                    <SelectTrigger id="personal-opencode-backing-provider">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {OPENCODE_BACKING_PROVIDER_OPTIONS.map((option) => (
                        <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="personal-opencode-model">Default model</Label>
                  <Select value={openCodeModel} onValueChange={setOpenCodeModel}>
                    <SelectTrigger id="personal-opencode-model">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {openCodeModelOptions.map((model) => (
                        <SelectItem key={model} value={model}>{model}</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="personal-opencode-model-custom">Custom model override</Label>
                  <Input
                    id="personal-opencode-model-custom"
                    value={openCodeCustomModel}
                    onChange={(event) => setOpenCodeCustomModel(event.target.value)}
                    placeholder="provider/model (e.g. xai/grok-code-fast)"
                  />
                </div>
              </>
            ) : null}
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
	                    : provider === "amp"
	                      ? "amp_..."
	                      : provider === "pi"
	                        ? "pi_..."
	                        : provider === "opencode"
	                          ? "OpenCode or provider key"
	                          : "sk-..."
	                }
              />
            </div>
          </>
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
            toast.success("Personal subscription connected");
          }}
        />
      ) : null}
    </PageContainer>
  );
}
