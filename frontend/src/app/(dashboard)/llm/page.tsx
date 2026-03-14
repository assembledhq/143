"use client";

import { useMemo, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
import { Check, Eye, EyeOff } from "lucide-react";
import {
  LLM_MODELS_BY_PROVIDER,
  LLM_PROVIDER_INFO,
  DEFAULT_LLM_MODEL,
  OPENAI_API_TYPE_CHAT,
} from "@/lib/model-constants";
import type {
  Organization,
  OrgSettings,
  SingleResponse,
  CredentialSummary,
  ListResponse,
} from "@/lib/types";

const LLM_PROVIDERS = Object.keys(LLM_PROVIDER_INFO) as (keyof typeof LLM_PROVIDER_INFO)[];

export default function LLMPage() {
  const queryClient = useQueryClient();

  // Fetch org settings (for llm_model)
  const { data: settings } = useQuery<SingleResponse<Organization>>({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });
  const orgSettings = (settings?.data?.settings ?? {}) as OrgSettings;

  // Fetch credential summaries (to show what the org has configured)
  const { data: credentialsResp } = useQuery<ListResponse<CredentialSummary>>({
    queryKey: ["credentials"],
    queryFn: () => api.credentials.list(),
  });
  const credentials = useMemo(() => credentialsResp?.data ?? [], [credentialsResp?.data]);

  // Fetch platform-level LLM defaults (to show fallback availability)
  const { data: llmDefaultsResp } = useQuery<{ data: Record<string, string> }>({
    queryKey: ["llm-defaults"],
    queryFn: () => api.settings.getLLMDefaults(),
  });
  const platformProviders = useMemo(() => llmDefaultsResp?.data ?? {}, [llmDefaultsResp?.data]);

  // Fetch available models from backend (source of truth)
  const { data: llmModelsResp } = useQuery<{ data: Record<string, string[]> }>({
    queryKey: ["llm-models"],
    queryFn: () => api.settings.getLLMModels(),
  });

  // Use backend models if available, fall back to static constants
  const modelsByProvider = useMemo(() => {
    const backendModels = llmModelsResp?.data;
    if (backendModels && Object.keys(backendModels).length > 0) {
      const result: Record<string, { label: string; models: readonly string[] }> = {};
      for (const [provider, models] of Object.entries(backendModels)) {
        const info = LLM_PROVIDER_INFO[provider];
        result[provider] = {
          label: info?.name ?? provider,
          models,
        };
      }
      return result;
    }
    return LLM_MODELS_BY_PROVIDER;
  }, [llmModelsResp?.data]);

  // Form state
  const [llmModel, setLlmModel] = useState(DEFAULT_LLM_MODEL);
  const [apiKeys, setApiKeys] = useState<Record<string, string>>({});
  const [showKeys, setShowKeys] = useState<Record<string, boolean>>({});
  const [saveStatus, setSaveStatus] = useState<"idle" | "success" | "error">("idle");
  const [keySaveStatus, setKeySaveStatus] = useState<Record<string, "idle" | "saving" | "success" | "error">>({});
  const [removingProvider, setRemovingProvider] = useState<string | null>(null);

  // Sync server data into form state.
  const [prevSettingsRef, setPrevSettingsRef] = useState<unknown>(undefined);
  const settingsData = settings?.data?.settings;
  if (settingsData && settingsData !== prevSettingsRef) {
    setPrevSettingsRef(settingsData);
    setLlmModel(orgSettings.llm_model || DEFAULT_LLM_MODEL);
  }

  // Determine which providers are configured (org-level or platform-level)
  const providerStatus = useMemo(() => {
    const status: Record<string, { orgConfigured: boolean; platformAvailable: boolean; maskedKey?: string }> = {};
    for (const provider of LLM_PROVIDERS) {
      const cred = credentials.find((c) => c.provider === provider);
      status[provider] = {
        orgConfigured: cred?.configured ?? false,
        platformAvailable: Boolean(platformProviders[provider]),
        maskedKey: cred?.configured ? cred.masked_key : undefined,
      };
    }
    return status;
  }, [credentials, platformProviders]);

  // Filter model groups to providers that are configured (org or platform)
  const enabledModelGroups = useMemo(() => {
    return Object.entries(modelsByProvider)
      .filter(([provider]) => {
        const ps = providerStatus[provider];
        return ps?.orgConfigured || ps?.platformAvailable;
      })
      .map(([, group]) => group);
  }, [modelsByProvider, providerStatus]);

  // Save model selection mutation
  const modelMutation = useMutation({
    mutationFn: (data: Record<string, unknown>) => api.settings.update(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["settings"] });
      setSaveStatus("success");
      setTimeout(() => setSaveStatus("idle"), 2000);
    },
    onError: () => {
      setSaveStatus("error");
      setTimeout(() => setSaveStatus("idle"), 3000);
    },
  });

  // Save API key mutation
  const keyMutation = useMutation({
    mutationFn: ({ provider, config }: { provider: string; config: Record<string, unknown> }) =>
      api.credentials.update(provider, config),
    onSuccess: (_data, variables) => {
      queryClient.invalidateQueries({ queryKey: ["credentials"] });
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

  // Delete credential mutation
  const deleteMutation = useMutation({
    mutationFn: (provider: string) => api.credentials.delete(provider),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["credentials"] });
      setRemovingProvider(null);
    },
  });

  function handleSaveModel() {
    modelMutation.mutate({
      settings: { llm_model: llmModel },
    });
  }

  function handleSaveKey(provider: string) {
    const key = apiKeys[provider]?.trim();
    if (!key) return;

    const config: Record<string, unknown> = { api_key: key };
    if (provider === "openai") {
      config.api_type = OPENAI_API_TYPE_CHAT;
    }
    setKeySaveStatus((prev) => ({ ...prev, [provider]: "saving" }));
    keyMutation.mutate({ provider, config });
  }

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="LLM"
          description="Configure the AI model used for validation, prioritization, and general intelligence."
        />

        {/* Provider API Keys */}
        <section className="space-y-3">
          <h2 className="text-[13px] font-medium text-foreground">Provider keys</h2>
          <p className="text-xs text-muted-foreground">
            Add your own API key for any provider below. If you don&apos;t configure a key, the platform
            default will be used when available.
          </p>
          <div className="space-y-3">
            {LLM_PROVIDERS.map((provider) => {
              const info = LLM_PROVIDER_INFO[provider];
              const ps = providerStatus[provider];
              const status = keySaveStatus[provider] ?? "idle";

              return (
                <Card key={provider}>
                  <CardContent>
                    <div className="space-y-3">
                      <div className="flex items-center justify-between">
                        <div>
                          <div className="flex items-center gap-2">
                            <span className="text-sm font-medium">{info.name}</span>
                            {ps?.orgConfigured && (
                              <Badge variant="success" className="text-[10px] px-1.5 py-0">
                                <Check className="mr-0.5 h-3 w-3" />
                                Configured
                              </Badge>
                            )}
                            {!ps?.orgConfigured && ps?.platformAvailable && (
                              <Badge variant="secondary" className="text-[10px] px-1.5 py-0">
                                Platform default
                              </Badge>
                            )}
                          </div>
                          <p className="text-xs text-muted-foreground mt-0.5">{info.description}</p>
                        </div>
                        {ps?.orgConfigured && (
                          <Button
                            variant="ghost"
                            size="sm"
                            className="text-xs text-muted-foreground"
                            onClick={() => setRemovingProvider(provider)}
                            disabled={deleteMutation.isPending}
                          >
                            Remove
                          </Button>
                        )}
                      </div>

                      {ps?.orgConfigured && ps.maskedKey && (
                        <p className="text-xs text-muted-foreground font-mono">
                          Key: {ps.maskedKey}
                        </p>
                      )}

                      <div className="flex gap-2">
                        <div className="relative flex-1">
                          <Input
                            type={showKeys[provider] ? "text" : "password"}
                            placeholder={ps?.orgConfigured ? "Replace existing key..." : info.keyPlaceholder}
                            value={apiKeys[provider] ?? ""}
                            onChange={(e) =>
                              setApiKeys((prev) => ({ ...prev, [provider]: e.target.value }))
                            }
                            className="pr-9 font-mono text-xs"
                          />
                          <button
                            type="button"
                            onClick={() =>
                              setShowKeys((prev) => ({ ...prev, [provider]: !prev[provider] }))
                            }
                            className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                          >
                            {showKeys[provider] ? (
                              <EyeOff className="h-3.5 w-3.5" />
                            ) : (
                              <Eye className="h-3.5 w-3.5" />
                            )}
                          </button>
                        </div>
                        <Button
                          size="sm"
                          onClick={() => handleSaveKey(provider)}
                          disabled={!apiKeys[provider]?.trim() || status === "saving"}
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
                    </div>
                  </CardContent>
                </Card>
              );
            })}
          </div>
        </section>

        {/* Model Selection */}
        <section className="space-y-3">
          <h2 className="text-[13px] font-medium text-foreground">Model</h2>
          <Card>
            <CardContent>
              <div className="space-y-3">
                <div className="space-y-2">
                  <Label htmlFor="llm-model">Default LLM model</Label>
                  <Select value={llmModel} onValueChange={setLlmModel}>
                    <SelectTrigger id="llm-model" aria-label="LLM Model">
                      <SelectValue placeholder="Select a model" />
                    </SelectTrigger>
                    <SelectContent>
                      {enabledModelGroups.length === 0 ? (
                        <SelectItem value={DEFAULT_LLM_MODEL} disabled>
                          No providers configured
                        </SelectItem>
                      ) : (
                        enabledModelGroups.map((group) => (
                          <SelectGroup key={group.label}>
                            <SelectLabel>{group.label}</SelectLabel>
                            {group.models.map((model) => (
                              <SelectItem key={model} value={model}>
                                {model}
                              </SelectItem>
                            ))}
                          </SelectGroup>
                        ))
                      )}
                    </SelectContent>
                  </Select>
                  <p className="text-xs text-muted-foreground">
                    The model used for validation, prioritization, and other general LLM tasks.
                    Only models from configured providers are shown.
                  </p>
                </div>
              </div>
            </CardContent>
          </Card>
        </section>

        <div className="flex items-center justify-end gap-3">
          {saveStatus === "success" && (
            <span className="text-sm text-emerald-600 dark:text-emerald-400">Model saved.</span>
          )}
          {saveStatus === "error" && (
            <span className="text-sm text-destructive">Failed to save model.</span>
          )}
          <Button onClick={handleSaveModel} disabled={modelMutation.isPending}>
            {modelMutation.isPending ? "Saving..." : "Save model"}
          </Button>
        </div>
      </div>

      {/* Remove Credential Confirmation Dialog */}
      <AlertDialog open={!!removingProvider} onOpenChange={(open) => !open && setRemovingProvider(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove API key</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to remove the {removingProvider ? LLM_PROVIDER_INFO[removingProvider]?.name : ""} API key?
              LLM features will fall back to the platform default if available, or stop working for this provider.
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
    </PageContainer>
  );
}
