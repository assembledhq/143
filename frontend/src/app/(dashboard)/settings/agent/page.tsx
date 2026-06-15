"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Plus, ShieldAlert, Trash2 } from "lucide-react";
import { notify as toast } from "@/lib/notify";
import { api } from "@/lib/api";
import { apiKeyHelp, OPENCODE_BACKING_PROVIDER_OPTIONS, openCodeAgentDefaults, openCodeDefaultModelForBackingProvider, openCodeModelsForBackingProvider, ORG_PROVIDER_OPTIONS, type OpenCodeBackingProvider } from "@/lib/coding-auth-metadata";
import { captureError } from "@/lib/errors";
import { useAuth } from "@/hooks/use-auth";
import {
  AVAILABLE_AMP_MODES,
  AVAILABLE_PI_MODELS,
  PI_MODEL_CLAUDE_OPUS_48,
} from "@/lib/model-constants";
import { queryKeys } from "@/lib/query-keys";
import type { CodingCredentialSummary, ListResponse, Organization, OrgSettings, SingleResponse } from "@/lib/types";
import { CodingAuthStack } from "@/components/coding-auth-stack";
import { EmptyState } from "@/components/empty-state";
import { AGENTS_BY_KEY } from "@/lib/agents";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { APIKeyHelpTooltip } from "@/components/api-key-help-tooltip";
import { CodingAuthDialog } from "@/components/coding-auth-dialog";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { CodexDeviceCodeModal } from "@/components/codex-device-code-modal";
import { ClaudeCodeAuthModal } from "@/components/claude-code-auth-modal";
import { capitalizeWords } from "@/lib/utils";

type ModalProvider = "codex" | "claude_code" | "amp" | "pi" | "opencode";
type AddFlowAuthType = "subscription" | "api_key";
type InsertionMode = "make_default" | "next_fallback";

const PROVIDER_OPTIONS = ORG_PROVIDER_OPTIONS;

function authStatusTone(status: CodingCredentialSummary["status"]) {
  switch (status) {
    case "healthy":
      return "text-success";
    case "rate_limited":
      return "text-warning";
    case "needs_reauth":
    case "invalid":
      return "text-destructive";
    default:
      return "text-slate-700";
  }
}

function moveRows(rows: CodingCredentialSummary[], id: string, direction: "up" | "down") {
  const index = rows.findIndex((row) => row.id === id);
  if (index === -1) return rows;
  const targetIndex = direction === "up" ? Math.max(0, index - 1) : Math.min(rows.length - 1, index + 1);
  if (index === targetIndex) return rows;
  const next = [...rows];
  const [row] = next.splice(index, 1);
  next.splice(targetIndex, 0, row);
  return next;
}

function moveRowToTop(rows: CodingCredentialSummary[], id: string) {
  const index = rows.findIndex((row) => row.id === id);
  if (index <= 0) return rows;
  const next = [...rows];
  const [row] = next.splice(index, 1);
  next.unshift(row);
  return next;
}

export function reorderRows(
  rows: CodingCredentialSummary[],
  sourceId: string,
  targetId: string,
  position: "before" | "after",
) {
  if (sourceId === targetId) return rows;
  const sourceIdx = rows.findIndex((row) => row.id === sourceId);
  const targetIdx = rows.findIndex((row) => row.id === targetId);
  if (sourceIdx === -1 || targetIdx === -1) return rows;
  const next = [...rows];
  const [row] = next.splice(sourceIdx, 1);
  // After removing the source, indices past it shift down by one.
  const targetIdxAfterRemove = sourceIdx < targetIdx ? targetIdx - 1 : targetIdx;
  const insertAt = position === "before" ? targetIdxAfterRemove : targetIdxAfterRemove + 1;
  if (insertAt === sourceIdx) return rows;
  next.splice(insertAt, 0, row);
  return next;
}

function defaultLabel(provider: ModalProvider, authType: AddFlowAuthType) {
  switch (provider) {
    case "codex":
      return authType === "subscription" ? "Codex subscription" : "Codex API key";
    case "claude_code":
      return authType === "subscription" ? "Claude Code subscription" : "Claude Code API key";
    case "amp":
      return "Amp API key";
    case "pi":
      return "Pi API key";
    case "opencode":
      return "OpenCode API key";
    default:
      return "Coding auth";
  }
}

export default function AgentPage() {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";

  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [addOpen, setAddOpen] = useState(false);
  const [showCodexModal, setShowCodexModal] = useState(false);
  const [showClaudeModal, setShowClaudeModal] = useState(false);
  const [provider, setProvider] = useState<ModalProvider>("codex");
  const [authType, setAuthType] = useState<AddFlowAuthType>("subscription");
  const [label, setLabel] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [insertionMode, setInsertionMode] = useState<InsertionMode>("next_fallback");
  const [renameValue, setRenameValue] = useState("");
  const [ampMode, setAmpMode] = useState<string>(AVAILABLE_AMP_MODES[0] ?? "smart");
  const [piModel, setPiModel] = useState<string>(PI_MODEL_CLAUDE_OPUS_48);
  const [openCodeBackingProvider, setOpenCodeBackingProvider] = useState<OpenCodeBackingProvider>("opencode");
  const [openCodeModel, setOpenCodeModel] = useState<string>(openCodeDefaultModelForBackingProvider("opencode"));
  const [openCodeCustomModel, setOpenCodeCustomModel] = useState("");
  const openCodeModelOptions = useMemo(() => openCodeModelsForBackingProvider(openCodeBackingProvider), [openCodeBackingProvider]);

  const { data: codingCredentialsResponse } = useQuery<ListResponse<CodingCredentialSummary>>({
    queryKey: queryKeys.codingCredentials.list("org"),
    queryFn: () => api.codingCredentials.list("org"),
  });
  const rows = useMemo(() => codingCredentialsResponse?.data ?? [], [codingCredentialsResponse?.data]);
  const selected = rows.find((row) => row.id === selectedId) ?? null;

  const { data: settingsResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });

  const settings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;

  const reorderMutation = useMutation({
    mutationFn: async (nextRows: CodingCredentialSummary[]) => {
      await api.codingCredentials.reorder("org", nextRows.map((row) => row.id));
      const nextDefault = nextRows.find((row) => row.status === "healthy") ?? nextRows[0];
      if (nextDefault && settings.default_agent_type !== nextDefault.agent) {
        await api.settings.update({ settings: { default_agent_type: nextDefault.agent } });
      }
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.codingCredentials.all });
      void queryClient.invalidateQueries({ queryKey: queryKeys.settings.all });
    },
    onError: (error) => {
      captureError(error, { feature: "coding-auth-reorder" });
      toast.error("Could not update fallback order");
    },
  });

  const stackCreateMutation = useMutation({
    mutationFn: async () => {
      const nextLabel = label.trim() || defaultLabel(provider, authType);
      // The unified create endpoint returns the new row unwrapped (no
      // SingleResponse envelope).
      return api.codingCredentials.create({
        scope: "org",
        agent: provider,
        auth_type: "api_key",
        label: nextLabel,
        api_key: apiKey,
        ...(provider === "opencode" ? { api_type: openCodeBackingProvider } : {}),
        ...(provider === "amp"
          ? { agent_defaults: { AMP_MODE: ampMode } }
          : provider === "pi"
            ? { agent_defaults: { PI_MODEL: piModel } }
            : provider === "opencode"
              ? { agent_defaults: openCodeAgentDefaults(openCodeModel, openCodeCustomModel) }
            : {}),
      });
    },
    onSuccess: async (created) => {
      await queryClient.invalidateQueries({ queryKey: queryKeys.codingCredentials.all });
      await queryClient.invalidateQueries({ queryKey: queryKeys.settings.all });
      closeAddModal();
      if (insertionMode === "make_default") {
        const nextRows = moveRowToTop([created, ...rows], created.id);
        await reorderMutation.mutateAsync(nextRows);
      }
      toast.success("Auth added");
    },
    onError: (error) => {
      captureError(error, { feature: "coding-auth-create" });
      toast.error("Could not create auth");
    },
  });

  const renameMutation = useMutation({
    mutationFn: (nextLabel: string) => api.codingCredentials.update(selectedId ?? "", { scope: "org", label: nextLabel }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.codingCredentials.all });
      toast.success("Auth renamed");
    },
    onError: (error) => {
      captureError(error, { feature: "coding-auth-rename" });
      toast.error("Could not rename auth");
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.codingCredentials.delete(id, "org"),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.codingCredentials.all });
      setSelectedId(null);
      setRenameValue("");
      toast.success("Auth removed");
    },
    onError: (error) => {
      captureError(error, { feature: "coding-auth-delete" });
      toast.error("Could not remove auth");
    },
  });

  const selectedProvider = PROVIDER_OPTIONS.find((option) => option.key === provider) ?? PROVIDER_OPTIONS[0];
  const effectiveAuthType = selectedProvider.supportsSubscription ? authType : "api_key";
  const generatedLabel = label.trim() || defaultLabel(provider, effectiveAuthType);
  const showInsertionSelect = selectedProvider.supportsStackOrder;
  const showAuthTypeSelector = selectedProvider.supportsSubscription;
  const addBusy = stackCreateMutation.isPending;

  function closeAddModal() {
    setAddOpen(false);
    setLabel("");
    setApiKey("");
    setProvider("codex");
    setAuthType("subscription");
    setInsertionMode("next_fallback");
    setAmpMode(AVAILABLE_AMP_MODES[0] ?? "smart");
    setPiModel(PI_MODEL_CLAUDE_OPUS_48);
    setOpenCodeBackingProvider("opencode");
    setOpenCodeModel(openCodeDefaultModelForBackingProvider("opencode"));
    setOpenCodeCustomModel("");
  }

  function updateOpenCodeBackingProvider(value: OpenCodeBackingProvider) {
    setOpenCodeBackingProvider(value);
    setOpenCodeModel(openCodeDefaultModelForBackingProvider(value));
    setOpenCodeCustomModel("");
  }

  function openAddModal(nextProvider: ModalProvider) {
    setProvider(nextProvider);
    setAuthType(PROVIDER_OPTIONS.find((option) => option.key === nextProvider)?.supportsSubscription ? "subscription" : "api_key");
    setAddOpen(true);
  }

  function handleContinueAdd() {
    if (effectiveAuthType === "subscription") {
      if (provider === "codex") {
        setShowCodexModal(true);
        setAddOpen(false);
        return;
      }
      if (provider === "claude_code") {
        setShowClaudeModal(true);
        setAddOpen(false);
        return;
      }
    }
    stackCreateMutation.mutate();
  }

  if (!isAdmin) {
    return (
      <PageContainer>
        <div className="space-y-6 pt-2">
          <PageHeader
            title="Coding agents"
            description="View the org's coding-agent auth stack. Only admins can change shared auths."
          />
          <div className="rounded-md bg-muted px-3 py-2 text-xs text-muted-foreground">
            <ShieldAlert className="mr-1.5 inline h-3.5 w-3.5 align-text-bottom" />
            Read-only view. Only admins can add, edit, or reorder coding auths.
          </div>
          <div className="flex items-center justify-between rounded-md border border-border bg-muted/30 px-3 py-3">
            <p className="text-xs text-muted-foreground">
              Shared sandbox networking, lifecycle, and capacity controls live in Runtime.
            </p>
            <Button asChild variant="outline" size="sm">
              <Link href="/settings/runtime">Runtime settings</Link>
            </Button>
          </div>

          <section className="space-y-3">
            <h2 className="text-xs font-medium text-foreground">Fallback stack</h2>
            {rows.length === 0 ? (
              <Card>
                <EmptyState
                  variant="inline"
                  icon={KeyRound}
                  title="No org coding auths yet"
                  description="Ask an admin to add org-level auths so coding-agent sessions have shared fallback credentials."
                />
              </Card>
            ) : (
              <Card>
                <CardContent className="p-0">
                  <div className="divide-y divide-border/50">
                    {rows.map((row, index) => (
                      <div
                        key={row.id}
                        className="grid gap-2 px-4 py-3 md:grid-cols-[60px_minmax(0,1fr)_minmax(0,1fr)_140px] md:items-center"
                      >
                        <div className="text-xs font-semibold text-muted-foreground">
                          #{index + 1}
                        </div>
                        <div className="text-xs font-medium">
                          {AGENTS_BY_KEY[row.agent]?.label ?? row.agent}
                        </div>
                        <div className="text-xs text-muted-foreground truncate">
                          {row.label}
                        </div>
                        <div>
                          <Badge variant="outline" className="text-xs">
                            {capitalizeWords(row.status)}
                          </Badge>
                        </div>
                      </div>
                    ))}
                  </div>
                </CardContent>
              </Card>
            )}
          </section>

        </div>
      </PageContainer>
    );
  }

  return (
    <PageContainer>
      <div className="space-y-8 pt-2">
        <PageHeader
          title="Coding agents"
          description="Control which auths the org can use and how fallback works."
          action={(
            <Button onClick={() => openAddModal("codex")}>
              <Plus className="mr-2 h-4 w-4" />
              Add auth
            </Button>
          )}
        />
        <div className="flex items-center justify-between rounded-md border border-border bg-muted/30 px-3 py-3">
          <p className="text-xs text-muted-foreground">
            Shared sandbox networking, lifecycle, and capacity controls live in Runtime.
          </p>
          <Button asChild variant="outline" size="sm">
            <Link href="/settings/runtime">Runtime settings</Link>
          </Button>
        </div>

        <section className="space-y-4">
          <div className="space-y-1.5">
            <h2 className="text-xs font-medium text-foreground">Fallback stack</h2>
            <p className="text-xs text-muted-foreground">
              The stack runs from top to bottom. Move the auth you want to prefer higher in the list.
            </p>
          </div>
          <CodingAuthStack
            rows={rows}
            selectedId={selectedId}
            onSelect={(id) => {
              setSelectedId(id);
              setRenameValue(rows.find((row) => row.id === id)?.label ?? "");
            }}
            onMove={(id, direction) => {
              const nextRows = moveRows(rows, id, direction);
              void reorderMutation.mutateAsync(nextRows);
            }}
            onReorder={(sourceId, targetId, position) => {
              const nextRows = reorderRows(rows, sourceId, targetId, position);
              if (nextRows === rows) return;
              void reorderMutation.mutateAsync(nextRows);
            }}
          />
        </section>

        <Sheet open={Boolean(selected)} onOpenChange={(open) => { if (!open) setSelectedId(null); }}>
          <SheetContent className="w-full sm:max-w-lg">
            {selected ? (
              <>
                <SheetHeader>
                  <div className="flex items-center gap-2">
                    <SheetTitle>{selected.label}</SheetTitle>
                    {selected.is_default ? <Badge>Default</Badge> : null}
                  </div>
                  <SheetDescription>
                    {selected.agent === "codex"
                      ? "Codex"
                      : selected.agent === "claude_code"
                        ? "Claude Code"
                        : selected.agent === "amp"
                          ? "Amp"
                          : selected.agent === "opencode"
                            ? "OpenCode"
                            : "Pi"} {selected.auth_type === "subscription" ? "subscription" : "API key"} auth
                  </SheetDescription>
                </SheetHeader>

                <div className="mt-6 space-y-6">
                  <div className="grid gap-3 text-sm">
                    <div className="flex items-center justify-between">
                      <span className="text-muted-foreground">Status</span>
                      <span className={authStatusTone(selected.status)}>{capitalizeWords(selected.status)}</span>
                    </div>
                    <div className="flex items-center justify-between">
                      <span className="text-muted-foreground">Priority</span>
                      <span>{selected.priority}</span>
                    </div>
                    <div className="flex items-center justify-between">
                      <span className="text-muted-foreground">Scope</span>
                      <span className="capitalize">{selected.scope}</span>
                    </div>
                    <div className="flex items-center justify-between gap-4">
                      <span className="text-muted-foreground">Usage note</span>
                      <span className="text-right">{selected.usage_note ? capitalizeWords(selected.usage_note) : "Unavailable"}</span>
                    </div>
                  </div>

                  <div className="space-y-2">
                    <Label htmlFor="rename-auth">Name</Label>
                    <Input id="rename-auth" value={renameValue} onChange={(event) => setRenameValue(event.target.value)} />
                  </div>

                  <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-between">
                    <div className="flex flex-wrap gap-2">
                      <Button
                        variant="outline"
                        onClick={() => void reorderMutation.mutateAsync(moveRowToTop(rows, selected.id))}
                        disabled={selected.priority === 1}
                      >
                        Set as default
                      </Button>
                      <Button
                        variant="ghost"
                        className="text-destructive hover:text-destructive"
                        onClick={() => deleteMutation.mutate(selected.id)}
                      >
                        <Trash2 className="mr-2 h-4 w-4" />
                        Remove
                      </Button>
                    </div>
                    <Button
                      onClick={() => renameMutation.mutate(renameValue.trim() || selected.label)}
                      disabled={
                        !renameValue.trim()
                        || renameValue.trim() === selected.label
                        || renameMutation.isPending
                      }
                    >
                      Save
                    </Button>
                  </div>
                </div>
              </>
            ) : null}
          </SheetContent>
        </Sheet>

        <CodingAuthDialog
          open={addOpen}
          onOpenChange={(open) => {
            if (!open) closeAddModal();
            else setAddOpen(true);
          }}
          title="Add auth"
          description="Add access for a coding agent."
          providerOptions={PROVIDER_OPTIONS}
          provider={provider}
          onProviderChange={(value) => {
            const nextProvider = value as ModalProvider;
            setProvider(nextProvider);
            if (!PROVIDER_OPTIONS.find((option) => option.key === nextProvider)?.supportsSubscription) {
              setAuthType("api_key");
            }
          }}
          primaryLabel={effectiveAuthType === "subscription" ? "Continue" : "Save auth"}
          onPrimary={handleContinueAdd}
          primaryDisabled={addBusy || (effectiveAuthType === "api_key" && !apiKey.trim())}
          onCancel={closeAddModal}
        >
          <>
            {showAuthTypeSelector ? (
              <div className="space-y-2">
                <Label>Auth type</Label>
                <RadioGroup
                  value={effectiveAuthType}
                  onValueChange={(value) => setAuthType(value as AddFlowAuthType)}
                  className="grid gap-3 md:grid-cols-2"
                >
                  <Label htmlFor="auth-subscription" className="flex cursor-pointer items-start gap-3 rounded-xl border border-border p-4">
                    <RadioGroupItem value="subscription" id="auth-subscription" />
                    <div className="space-y-1">
                      <div className="font-medium text-sm">Subscription</div>
                      <p className="text-xs text-muted-foreground">
                        Use this when a seat is already provisioned and you want managed sign-in.
                      </p>
                    </div>
                  </Label>
                  <Label htmlFor="auth-api-key" className="flex cursor-pointer items-start gap-3 rounded-xl border border-border p-4">
                    <RadioGroupItem value="api_key" id="auth-api-key" />
                    <div className="space-y-1">
                      <div className="font-medium text-sm">API key</div>
                      <p className="text-xs text-muted-foreground">
                        Use this for service accounts, rotation, and pay-as-you-go billing.
                      </p>
                    </div>
                  </Label>
                </RadioGroup>
              </div>
            ) : null}

            <div className="space-y-2">
              <Label htmlFor="auth-label">Name</Label>
              <Input
                id="auth-label"
                value={label}
                onChange={(event) => setLabel(event.target.value)}
                placeholder={`Optional — defaults to “${generatedLabel}”`}
              />
            </div>

            {effectiveAuthType === "api_key" ? (
              <>
                {provider === "amp" ? (
                  <div className="space-y-2">
                    <Label htmlFor="amp-mode">Default mode</Label>
                    <Select value={ampMode} onValueChange={setAmpMode}>
                      <SelectTrigger id="amp-mode">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {AVAILABLE_AMP_MODES.map((mode) => (
                          <SelectItem key={mode} value={mode}>{capitalizeWords(mode)}</SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                ) : null}
                {provider === "pi" ? (
                  <div className="space-y-2">
                    <Label htmlFor="pi-model">Default model</Label>
                    <Select value={piModel} onValueChange={setPiModel}>
                      <SelectTrigger id="pi-model">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {AVAILABLE_PI_MODELS.map((model) => (
                          <SelectItem key={model} value={model}>{model}</SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                ) : null}
                {provider === "opencode" ? (
                  <>
                    <div className="space-y-2">
                      <Label htmlFor="opencode-backing-provider">OpenCode provider</Label>
                      <Select value={openCodeBackingProvider} onValueChange={(value) => updateOpenCodeBackingProvider(value as OpenCodeBackingProvider)}>
                        <SelectTrigger id="opencode-backing-provider">
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
                      <Label htmlFor="opencode-model">Default model</Label>
                      <Select value={openCodeModel} onValueChange={setOpenCodeModel}>
                        <SelectTrigger id="opencode-model">
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
                      <Label htmlFor="opencode-model-custom">Custom model override</Label>
                      <Input
                        id="opencode-model-custom"
                        value={openCodeCustomModel}
                        onChange={(event) => setOpenCodeCustomModel(event.target.value)}
                        placeholder="provider/model (e.g. xai/grok-code-fast)"
                      />
                    </div>
                  </>
                ) : null}
                <div className="space-y-2">
                  <Label htmlFor="auth-api-key-input" className="flex items-center gap-2">
                    API key
                    <APIKeyHelpTooltip
                      ariaLabel={`Where to get a ${apiKeyHelp(provider).label} API key`}
                      description={apiKeyHelp(provider).description}
                      href={apiKeyHelp(provider).href}
                      linkLabel={apiKeyHelp(provider).linkLabel}
                    />
                  </Label>
                  <Input
                    id="auth-api-key-input"
                    type="password"
                    value={apiKey}
                    onChange={(event) => setApiKey(event.target.value)}
                    placeholder={provider === "amp" ? "amp_..." : provider === "pi" ? "pi_..." : provider === "opencode" ? "OpenCode or provider key" : provider === "claude_code" ? "sk-ant-..." : "sk-..."}
                  />
                </div>
              </>
            ) : null}

            {showInsertionSelect ? (
              <div className="space-y-2">
                <Label htmlFor="insertion-mode">Insertion point</Label>
                <Select value={insertionMode} onValueChange={(value) => setInsertionMode(value as InsertionMode)}>
                  <SelectTrigger id="insertion-mode">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="next_fallback">Add as next fallback</SelectItem>
                    <SelectItem value="make_default">Make default</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            ) : null}
          </>
        </CodingAuthDialog>

        {showCodexModal ? (
          <CodexDeviceCodeModal
            label={generatedLabel}
            scope="org"
            onClose={() => {
              setShowCodexModal(false);
              closeAddModal();
            }}
            onConnected={() => {
              setShowCodexModal(false);
              closeAddModal();
              void queryClient.invalidateQueries({ queryKey: queryKeys.codingCredentials.all });
            }}
          />
        ) : null}

        {showClaudeModal ? (
          <ClaudeCodeAuthModal
            label={generatedLabel}
            scope="org"
            onClose={() => {
              setShowClaudeModal(false);
              closeAddModal();
            }}
            onConnected={() => {
              setShowClaudeModal(false);
              closeAddModal();
              void queryClient.invalidateQueries({ queryKey: queryKeys.codingCredentials.all });
            }}
          />
        ) : null}
      </div>
    </PageContainer>
  );
}
