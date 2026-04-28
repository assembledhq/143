"use client";

import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { notify as toast } from "@/lib/notify";
import { api } from "@/lib/api";
import { apiKeyHelp, PERSONAL_PROVIDER_OPTIONS, type PersonalProvider } from "@/lib/coding-auth-metadata";
import { captureError } from "@/lib/errors";
import { APIKeyHelpTooltip } from "@/components/api-key-help-tooltip";
import { CodingAuthDialog } from "@/components/coding-auth-dialog";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
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
import type { ListResponse, UserCredentialSummary, UserSettingsUpdateRequest } from "@/lib/types";

function providerLabel(provider: string) {
  switch (provider) {
    case "openai":
      return "Codex";
    case "anthropic":
      return "Claude Code";
    case "gemini":
      return "Gemini CLI";
    case "amp":
      return "Amp";
    case "pi":
      return "Pi";
    case "openrouter":
      return "OpenRouter";
    default:
      return provider;
  }
}

function statusLabel(status?: string) {
  switch (status) {
    case "active":
      return "Healthy";
    case "invalid":
      return "Invalid";
    case "pending_auth":
      return "Needs reauth";
    default:
      return "Never verified";
  }
}

export default function AccountPage() {
  const { user } = useAuth();
  const queryClient = useQueryClient();
  const [addOpen, setAddOpen] = useState(false);
  const [provider, setProvider] = useState<PersonalProvider>("openai");
  const [apiKey, setApiKey] = useState("");
  const [pendingReasoningDefaults, setPendingReasoningDefaults] = useState<UserSettingsUpdateRequest["coding_agent_reasoning_defaults"] | null>(null);
  const reasoningSaveInFlightRef = useRef(false);
  const queuedReasoningDefaultsRef = useRef<UserSettingsUpdateRequest["coding_agent_reasoning_defaults"] | null>(null);

  const { data: personalResp } = useQuery<ListResponse<UserCredentialSummary>>({
    queryKey: ["user-credentials", "personal"],
    queryFn: () => api.userCredentials.listPersonal(),
  });
  const personalCreds = personalResp?.data ?? [];
  const personalRows = personalCreds.filter((row) => row.configured);
  const storedReasoningDefaults = getCodingAgentReasoningDefaultsFromSettings(user?.settings);
  const effectiveReasoningDefaults = pendingReasoningDefaults ?? storedReasoningDefaults;

  const createMutation = useMutation({
    mutationFn: () => api.userCredentials.upsertPersonal(provider, { api_key: apiKey }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
      void queryClient.invalidateQueries({ queryKey: ["credentials", "resolved"] });
      setApiKey("");
      setAddOpen(false);
      toast.success("Personal auth saved");
    },
    onError: (error) => {
      captureError(error, { feature: "personal-coding-auth-save" });
      toast.error("Could not save personal auth");
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (targetProvider: string) => api.userCredentials.deletePersonal(targetProvider),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
      void queryClient.invalidateQueries({ queryKey: ["credentials", "resolved"] });
      toast.success("Personal auth removed");
    },
    onError: (error) => {
      captureError(error, { feature: "personal-coding-auth-delete" });
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
          description="Manage your personal coding agent auths and appearance."
          action={(
            <Button onClick={() => setAddOpen(true)}>
              <Plus className="mr-2 h-4 w-4" />
              Add auth
            </Button>
          )}
        />

        <Card>
          <CardHeader>
            <CardTitle>Configured personal auths</CardTitle>
          </CardHeader>
          <CardContent className="pb-6">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Agent</TableHead>
                  <TableHead>Auth type</TableHead>
                  <TableHead>Notes</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="w-24 text-right">Action</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {personalRows.length > 0 ? personalRows.map((row) => (
                  <TableRow key={row.provider}>
                    <TableCell>{providerLabel(row.provider)}</TableCell>
                    <TableCell>API key</TableCell>
                    <TableCell>{row.masked_key ?? "Masked key unavailable"}</TableCell>
                    <TableCell>
                      <Badge variant="outline">{statusLabel(row.status)}</Badge>
                    </TableCell>
                    <TableCell className="text-right">
                      <Button variant="ghost" size="sm" onClick={() => deleteMutation.mutate(row.provider)}>
                        <Trash2 className="mr-2 h-4 w-4" />
                        Disable
                      </Button>
                    </TableCell>
                  </TableRow>
                )) : (
                  <TableRow>
                    <TableCell colSpan={5} className="text-muted-foreground">
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
        onOpenChange={setAddOpen}
        title="Add auth"
        description="Add a personal API key that will be tried before the organization fallback stack."
        providerOptions={PERSONAL_PROVIDER_OPTIONS}
        provider={provider}
        onProviderChange={setProvider}
        primaryLabel="Save auth"
        onPrimary={() => createMutation.mutate()}
        primaryDisabled={!apiKey.trim()}
        onCancel={() => setAddOpen(false)}
      >
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
