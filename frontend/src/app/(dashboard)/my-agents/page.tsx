"use client";

import { useMemo, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { useAuth } from "@/hooks/use-auth";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
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
import { Check, Eye, EyeOff, Shield } from "lucide-react";
import type {
  UserCredentialSummary,
  ResolvedCredential,
  ListResponse,
} from "@/lib/types";

const CODING_AGENT_PROVIDERS: {
  key: string;
  name: string;
  description: string;
  keyPlaceholder: string;
}[] = [
  { key: "anthropic", name: "Anthropic", description: "Claude Code (Opus, Sonnet, Haiku)", keyPlaceholder: "sk-ant-..." },
  { key: "openai", name: "OpenAI", description: "Codex (GPT-5 models)", keyPlaceholder: "sk-..." },
  { key: "gemini", name: "Google Gemini", description: "Gemini CLI (Pro, Flash)", keyPlaceholder: "AIza..." },
  { key: "openrouter", name: "OpenRouter", description: "Access all coding agents with a single key", keyPlaceholder: "sk-or-..." },
];

export default function MyAgentsPage() {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";

  // Fetch personal credentials
  const { data: personalResp } = useQuery<ListResponse<UserCredentialSummary>>({
    queryKey: ["user-credentials", "personal"],
    queryFn: () => api.userCredentials.listPersonal(),
  });
  const personalCreds = useMemo(() => personalResp?.data ?? [], [personalResp?.data]);

  // Fetch team defaults (admin only — skipped for non-admins)
  const { data: teamResp } = useQuery<ListResponse<UserCredentialSummary>>({
    queryKey: ["user-credentials", "team"],
    queryFn: () => api.userCredentials.listTeamDefaults(),
    enabled: isAdmin,
  });
  const teamDefaults = useMemo(() => teamResp?.data ?? [], [teamResp?.data]);

  // Fetch resolved view
  const { data: resolvedResp } = useQuery<ListResponse<ResolvedCredential>>({
    queryKey: ["user-credentials", "resolved"],
    queryFn: () => api.userCredentials.listResolved(),
  });
  const resolved = useMemo(() => resolvedResp?.data ?? [], [resolvedResp?.data]);

  // Form state
  const [apiKeys, setApiKeys] = useState<Record<string, string>>({});
  const [showKeys, setShowKeys] = useState<Record<string, boolean>>({});
  const [keySaveStatus, setKeySaveStatus] = useState<Record<string, "idle" | "saving" | "success" | "error">>({});
  const [removingProvider, setRemovingProvider] = useState<string | null>(null);
  const [removingTeamProvider, setRemovingTeamProvider] = useState<string | null>(null);

  // Save personal key
  const upsertMutation = useMutation({
    mutationFn: ({ provider, apiKey }: { provider: string; apiKey: string }) =>
      api.userCredentials.upsertPersonal(provider, { api_key: apiKey }),
    onSuccess: (_data, variables) => {
      queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
      setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "success" }));
      setApiKeys((prev) => ({ ...prev, [variables.provider]: "" }));
      setTimeout(() => {
        setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "idle" }));
      }, 2000);
    },
    onError: (_err, variables) => {
      setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "error" }));
      setTimeout(() => {
        setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "idle" }));
      }, 3000);
    },
  });

  // Delete personal key
  const deleteMutation = useMutation({
    mutationFn: (provider: string) => api.userCredentials.deletePersonal(provider),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
      setRemovingProvider(null);
    },
  });

  // Remove team default (admin only)
  const removeTeamMutation = useMutation({
    mutationFn: (provider: string) => api.userCredentials.removeTeamDefault(provider),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
      setRemovingTeamProvider(null);
    },
  });

  // Set as team default (admin only)
  const setTeamDefaultMutation = useMutation({
    mutationFn: ({ provider, userId }: { provider: string; userId: string }) =>
      api.userCredentials.setTeamDefault(provider, userId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
    },
  });

  function handleSaveKey(provider: string) {
    const key = apiKeys[provider]?.trim();
    if (!key) return;
    setKeySaveStatus((prev) => ({ ...prev, [provider]: "saving" }));
    upsertMutation.mutate({ provider, apiKey: key });
  }

  function sourceLabel(source: string): string {
    switch (source) {
      case "personal": return "Your key";
      case "team_default": return "Team default";
      case "org": return "Organization";
      default: return "Not configured";
    }
  }

  function sourceBadgeVariant(source: string): "success" | "secondary" | "outline" | "destructive" {
    switch (source) {
      case "personal": return "success";
      case "team_default": return "secondary";
      case "org": return "secondary";
      default: return "outline";
    }
  }

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="My Agents"
          description="Manage your personal coding agent API keys. Your keys are used first, falling back to team defaults, then organization keys."
        />

        <Tabs defaultValue="personal" className="w-full">
          <TabsList>
            <TabsTrigger value="personal">My Keys</TabsTrigger>
            {isAdmin && <TabsTrigger value="team">Team Defaults</TabsTrigger>}
            <TabsTrigger value="resolved">Active Config</TabsTrigger>
          </TabsList>

          {/* Personal Keys Tab */}
          <TabsContent value="personal" className="space-y-3 mt-4">
            <p className="text-xs text-muted-foreground">
              Add your own API keys for coding agents. These will be used for your sessions, helping distribute rate limits across your team.
            </p>
            <div className="space-y-3">
              {CODING_AGENT_PROVIDERS.map((provider) => {
                const cred = personalCreds.find((c) => c.provider === provider.key);
                const status = keySaveStatus[provider.key] ?? "idle";

                return (
                  <Card key={provider.key}>
                    <CardContent>
                      <div className="space-y-3">
                        <div className="flex items-center justify-between">
                          <div>
                            <div className="flex items-center gap-2">
                              <span className="text-sm font-medium">{provider.name}</span>
                              {cred?.configured && (
                                <Badge variant="success" className="text-[10px] px-1.5 py-0">
                                  <Check className="mr-0.5 h-3 w-3" />
                                  Configured
                                </Badge>
                              )}
                              {cred?.is_team_default && (
                                <Badge variant="secondary" className="text-[10px] px-1.5 py-0">
                                  <Shield className="mr-0.5 h-3 w-3" />
                                  Team default
                                </Badge>
                              )}
                            </div>
                            <p className="text-xs text-muted-foreground mt-0.5">{provider.description}</p>
                          </div>
                          {cred?.configured && (
                            <Button
                              variant="ghost"
                              size="sm"
                              className="text-xs text-muted-foreground"
                              onClick={() => setRemovingProvider(provider.key)}
                              disabled={deleteMutation.isPending}
                            >
                              Remove
                            </Button>
                          )}
                        </div>

                        {cred?.configured && cred.masked_key && (
                          <p className="text-xs text-muted-foreground font-mono">
                            Key: {cred.masked_key}
                          </p>
                        )}

                        <div className="flex gap-2">
                          <div className="relative flex-1">
                            <Input
                              type={showKeys[provider.key] ? "text" : "password"}
                              placeholder={cred?.configured ? "Replace existing key..." : provider.keyPlaceholder}
                              value={apiKeys[provider.key] ?? ""}
                              onChange={(e) =>
                                setApiKeys((prev) => ({ ...prev, [provider.key]: e.target.value }))
                              }
                              className="pr-9 font-mono text-xs"
                            />
                            <button
                              type="button"
                              onClick={() =>
                                setShowKeys((prev) => ({ ...prev, [provider.key]: !prev[provider.key] }))
                              }
                              className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                            >
                              {showKeys[provider.key] ? (
                                <EyeOff className="h-3.5 w-3.5" />
                              ) : (
                                <Eye className="h-3.5 w-3.5" />
                              )}
                            </button>
                          </div>
                          <Button
                            size="sm"
                            onClick={() => handleSaveKey(provider.key)}
                            disabled={!apiKeys[provider.key]?.trim() || status === "saving"}
                          >
                            {status === "saving" ? "Saving..." : "Save key"}
                          </Button>
                        </div>
                        {status === "success" && (
                          <p className="text-xs text-emerald-600 dark:text-emerald-400">Key saved successfully.</p>
                        )}
                        {status === "error" && (
                          <p className="text-xs text-destructive">Failed to save key.</p>
                        )}

                        {/* Admin: promote to team default */}
                        {isAdmin && cred?.configured && !cred.is_team_default && user && (
                          <Button
                            variant="outline"
                            size="sm"
                            className="text-xs"
                            onClick={() => setTeamDefaultMutation.mutate({ provider: provider.key, userId: user.id })}
                            disabled={setTeamDefaultMutation.isPending}
                          >
                            <Shield className="mr-1 h-3 w-3" />
                            Set as team default
                          </Button>
                        )}
                      </div>
                    </CardContent>
                  </Card>
                );
              })}
            </div>
          </TabsContent>

          {/* Team Defaults Tab (admin only) */}
          {isAdmin && (
            <TabsContent value="team" className="space-y-3 mt-4">
              <p className="text-xs text-muted-foreground">
                Team defaults are used when a team member doesn&apos;t have their own key configured.
                Only admins can manage team defaults.
              </p>
              <div className="space-y-3">
                {CODING_AGENT_PROVIDERS.map((provider) => {
                  const cred = teamDefaults.find((c) => c.provider === provider.key);
                  return (
                    <Card key={provider.key}>
                      <CardContent>
                        <div className="flex items-center justify-between">
                          <div>
                            <div className="flex items-center gap-2">
                              <span className="text-sm font-medium">{provider.name}</span>
                              {cred ? (
                                <Badge variant="success" className="text-[10px] px-1.5 py-0">
                                  <Check className="mr-0.5 h-3 w-3" />
                                  Active
                                </Badge>
                              ) : (
                                <Badge variant="outline" className="text-[10px] px-1.5 py-0">
                                  Not set
                                </Badge>
                              )}
                            </div>
                            <p className="text-xs text-muted-foreground mt-0.5">{provider.description}</p>
                            {cred?.masked_key && (
                              <p className="text-xs text-muted-foreground font-mono mt-1">
                                Key: {cred.masked_key}
                              </p>
                            )}
                            {cred?.set_by_user_name && (
                              <p className="text-xs text-muted-foreground mt-0.5">
                                Set by {cred.set_by_user_name}
                              </p>
                            )}
                          </div>
                          {cred && (
                            <Button
                              variant="ghost"
                              size="sm"
                              className="text-xs text-muted-foreground"
                              onClick={() => setRemovingTeamProvider(provider.key)}
                              disabled={removeTeamMutation.isPending}
                            >
                              Remove
                            </Button>
                          )}
                        </div>
                      </CardContent>
                    </Card>
                  );
                })}
              </div>
            </TabsContent>
          )}

          {/* Resolved Config Tab */}
          <TabsContent value="resolved" className="space-y-3 mt-4">
            <p className="text-xs text-muted-foreground">
              Shows which API key source will be used for each provider when you run a session.
              Resolution order: your personal key, then team default, then organization key.
            </p>
            <div className="space-y-3">
              {CODING_AGENT_PROVIDERS.map((provider) => {
                const r = resolved.find((c) => c.provider === provider.key);
                const source = r?.source ?? "none";
                return (
                  <Card key={provider.key}>
                    <CardContent>
                      <div className="flex items-center justify-between">
                        <div>
                          <div className="flex items-center gap-2">
                            <span className="text-sm font-medium">{provider.name}</span>
                            <Badge variant={sourceBadgeVariant(source)} className="text-[10px] px-1.5 py-0">
                              {sourceLabel(source)}
                            </Badge>
                          </div>
                          {r?.masked_key && (
                            <p className="text-xs text-muted-foreground font-mono mt-1">
                              Key: {r.masked_key}
                            </p>
                          )}
                        </div>
                      </div>
                    </CardContent>
                  </Card>
                );
              })}
            </div>
          </TabsContent>
        </Tabs>
      </div>

      {/* Remove Personal Key Dialog */}
      <AlertDialog open={!!removingProvider} onOpenChange={(open) => !open && setRemovingProvider(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove API key</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to remove your {removingProvider ? CODING_AGENT_PROVIDERS.find((p) => p.key === removingProvider)?.name : ""} API key?
              Sessions will fall back to the team default or organization key.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (removingProvider) {
                  deleteMutation.mutate(removingProvider);
                }
              }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Remove Team Default Dialog */}
      <AlertDialog open={!!removingTeamProvider} onOpenChange={(open) => !open && setRemovingTeamProvider(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove team default</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to remove the team default for {removingTeamProvider ? CODING_AGENT_PROVIDERS.find((p) => p.key === removingTeamProvider)?.name : ""}?
              Team members without personal keys will fall back to the organization credential.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (removingTeamProvider) {
                  removeTeamMutation.mutate(removingTeamProvider);
                }
              }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </PageContainer>
  );
}
