"use client";

import { useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { notify as toast } from "@/lib/notify";
import { api } from "@/lib/api";
import { apiKeyHelp, PERSONAL_PROVIDER_OPTIONS, personalProviderToAgent, type PersonalProvider } from "@/lib/coding-auth-metadata";
import { captureError } from "@/lib/errors";
import { APIKeyHelpTooltip } from "@/components/api-key-help-tooltip";
import { CodingAuthDialog } from "@/components/coding-auth-dialog";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ThemeSelect } from "@/components/theme-select";
import { useAuth } from "@/hooks/use-auth";
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

// effectiveResolutionLine renders the ordered list as a one-line "Personal #1
// → Personal #2 → Org #1" string. Mirrors the design doc's "Effective
// resolution for you" hint and is the single most-asked support question, so
// surfacing it ambient on the page eliminates the round-trip.
//
// `rows` comes from /api/v1/coding-credentials?scope=resolved, which is
// backed by ListResolvable (status='active' rows only). Counting is
// therefore safe: every row counted is one the resolver would actually walk.
function effectiveResolutionLine(rows: CodingCredentialSummary[]): string {
  const personalCount = rows.filter((r) => r.scope === "personal").length;
  const orgCount = rows.filter((r) => r.scope === "org").length;
  const segments: string[] = [];
  for (let i = 0; i < personalCount; i++) {
    segments.push(`Personal #${i + 1}`);
  }
  for (let i = 0; i < orgCount; i++) {
    segments.push(`Org #${i + 1}`);
  }
  if (segments.length === 0) {
    return "No credentials configured.";
  }
  return segments.join(" → ");
}

export default function AccountPage() {
  const { user } = useAuth();
  const queryClient = useQueryClient();
  const [addOpen, setAddOpen] = useState(false);
  const [provider, setProvider] = useState<PersonalProvider>("openai");
  const [apiKey, setApiKey] = useState("");
  const [authLabel, setAuthLabel] = useState("");
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
  // Resolved — the merged ordered list (personal-then-org) the runtime uses.
  // Drives the "Effective resolution" line at the bottom of the page.
  const { data: resolvedResp } = useQuery<ListResponse<CodingCredentialSummary>>({
    queryKey: ["coding-credentials", "resolved"],
    queryFn: () => api.codingCredentials.list("resolved"),
  });

  const personalRows = personalResp?.data ?? [];
  const orgRows = orgResp?.data ?? [];

  const resolutionLine = useMemo(
    () => effectiveResolutionLine(resolvedResp?.data ?? []),
    [resolvedResp?.data],
  );

  const storedReasoningDefaults = getCodingAgentReasoningDefaultsFromSettings(user?.settings);
  const effectiveReasoningDefaults = pendingReasoningDefaults ?? storedReasoningDefaults;

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

  const updateReasoningDefaultsMutation = useMutation({
    mutationFn: (defaults: UserSettingsUpdateRequest["coding_agent_reasoning_defaults"]) =>
      api.auth.updateSettings({ coding_agent_reasoning_defaults: defaults }),
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
          <CardContent className="pb-6">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-12">#</TableHead>
                  <TableHead>Agent</TableHead>
                  <TableHead>Auth type</TableHead>
                  <TableHead>Notes</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="w-24 text-right">Action</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {personalRows.length > 0 ? personalRows.map((row, idx) => (
                  <TableRow key={row.id}>
                    <TableCell className="text-muted-foreground">{idx + 1}</TableCell>
                    <TableCell>
                      {agentLabel(row.agent)}
                      {row.is_default ? (
                        <Badge variant="secondary" className="ml-2">Default</Badge>
                      ) : null}
                    </TableCell>
                    <TableCell>{authTypeLabel(row.auth_type)}</TableCell>
                    <TableCell>{row.usage_note ?? row.label}</TableCell>
                    <TableCell>
                      <Badge variant="outline">{statusLabel(row.status)}</Badge>
                    </TableCell>
                    <TableCell className="text-right">
                      <Button variant="ghost" size="sm" onClick={() => deleteMutation.mutate(row.id)}>
                        <Trash2 className="mr-2 h-4 w-4" />
                        Disable
                      </Button>
                    </TableCell>
                  </TableRow>
                )) : (
                  <TableRow>
                    <TableCell colSpan={6} className="text-muted-foreground">
                      No personal auth configured. Click &ldquo;Add auth&rdquo; above to enable sessions to use your own subscription. Org-wide credentials are used as a fallback.
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Org fallback</CardTitle>
            <CardDescription>
              Read-only. Contact an admin to change org auths.
            </CardDescription>
          </CardHeader>
          <CardContent className="pb-6">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-12">#</TableHead>
                  <TableHead>Agent</TableHead>
                  <TableHead>Auth type</TableHead>
                  <TableHead>Notes</TableHead>
                  <TableHead>Status</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {orgRows.length > 0 ? orgRows.map((row, idx) => (
                  <TableRow key={row.id}>
                    <TableCell className="text-muted-foreground">{idx + 1}</TableCell>
                    <TableCell>{agentLabel(row.agent)}</TableCell>
                    <TableCell>{authTypeLabel(row.auth_type)}</TableCell>
                    <TableCell>{row.usage_note ?? row.label}</TableCell>
                    <TableCell>
                      <Badge variant="outline">{statusLabel(row.status)}</Badge>
                    </TableCell>
                  </TableRow>
                )) : (
                  <TableRow>
                    <TableCell colSpan={5} className="text-muted-foreground">
                      No org-level fallback configured.
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
            <div className="mt-4 rounded-md border border-dashed border-muted bg-muted/30 px-4 py-3 text-sm text-muted-foreground">
              <span className="font-medium text-foreground">Effective resolution for you:</span>{" "}
              {resolutionLine}
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Coding agent defaults</CardTitle>
          </CardHeader>
          <CardContent className="pb-6">
            <div className="space-y-4">
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
                      <SelectTrigger id={`default-coding-agent-reasoning-${agentType}`} aria-label={`${config.label} default coding-agent reasoning`} className="w-[220px]">
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
          setAddOpen(open);
          if (!open) {
            setApiKey("");
            setAuthLabel("");
          }
        }}
        title="Add auth"
        description="Add a personal API key that will be tried before the organization fallback stack."
        providerOptions={PERSONAL_PROVIDER_OPTIONS}
        provider={provider}
        onProviderChange={setProvider}
        primaryLabel="Save auth"
        onPrimary={() => createMutation.mutate()}
        primaryDisabled={!apiKey.trim()}
        onCancel={() => {
          setApiKey("");
          setAuthLabel("");
          setAddOpen(false);
        }}
      >
        <div className="space-y-2">
          <Label htmlFor="personal-auth-label">Label</Label>
          <Input
            id="personal-auth-label"
            value={authLabel}
            onChange={(event) => setAuthLabel(event.target.value)}
            placeholder={`${agentLabel(personalProviderToAgent(provider))} backup`}
          />
        </div>
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
      </CodingAuthDialog>
    </PageContainer>
  );
}
