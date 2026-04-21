"use client";

import { useMemo, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import { Card, CardContent } from "@/components/ui/card";
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
import { AlertTriangle } from "lucide-react";
import Link from "next/link";
import {
  LLM_MODELS_BY_PROVIDER,
  LLM_PROVIDER_INFO,
  DEFAULT_LLM_MODEL,
  OPENAI_API_TYPE_CHAT,
  ownerProviderForModel,
} from "@/lib/model-constants";
import type {
  Organization,
  OrgSettings,
  SingleResponse,
  CredentialSummary,
  ListResponse,
} from "@/lib/types";
import { DefaultModelCard } from "./_components/DefaultModelCard";
import { ProviderKeyDialog, type SaveStatus } from "./_components/ProviderKeyDialog";
import { ProviderKeyRow } from "./_components/ProviderKeyRow";

const LLM_PROVIDERS = Object.keys(LLM_PROVIDER_INFO) as (keyof typeof LLM_PROVIDER_INFO)[];

export default function LLMPage() {
  const queryClient = useQueryClient();

  const { data: settings } = useQuery<SingleResponse<Organization>>({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });
  const orgSettings = (settings?.data?.settings ?? {}) as OrgSettings;

  const { data: credentialsResp } = useQuery<ListResponse<CredentialSummary>>({
    queryKey: ["credentials"],
    queryFn: () => api.credentials.list(),
  });
  const credentials = useMemo(() => credentialsResp?.data ?? [], [credentialsResp?.data]);

  const { data: llmDefaultsResp } = useQuery<{ data: Record<string, string> }>({
    queryKey: ["llm-defaults"],
    queryFn: () => api.settings.getLLMDefaults(),
  });
  const platformProviders = useMemo(() => llmDefaultsResp?.data ?? {}, [llmDefaultsResp?.data]);
  const hasPlatformLLM = Object.keys(platformProviders).length > 0;

  const { data: llmModelsResp } = useQuery<{ data: Record<string, string[]> }>({
    queryKey: ["llm-models"],
    queryFn: () => api.settings.getLLMModels(),
  });

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

  const [llmModel, setLlmModel] = useState(DEFAULT_LLM_MODEL);
  const [reasoningEffort, setReasoningEffort] = useState<string>("");
  const [saveStatus, setSaveStatus] = useState<"idle" | "success" | "error">("idle");
  const [keySaveStatus, setKeySaveStatus] = useState<Record<string, SaveStatus>>({});
  const [removingProvider, setRemovingProvider] = useState<string | null>(null);
  const [editingProvider, setEditingProvider] = useState<string | null>(null);

  const [prevSettingsRef, setPrevSettingsRef] = useState<unknown>(undefined);
  const settingsData = settings?.data?.settings;
  if (settingsData && settingsData !== prevSettingsRef) {
    setPrevSettingsRef(settingsData);
    setLlmModel(orgSettings.llm_model || DEFAULT_LLM_MODEL);
    setReasoningEffort(orgSettings.llm_reasoning_effort || "");
  }

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

  const enabledModelGroups = useMemo(() => {
    return Object.entries(modelsByProvider)
      .filter(([provider]) => {
        const ps = providerStatus[provider];
        return ps?.orgConfigured || ps?.platformAvailable;
      })
      .map(([, group]) => group);
  }, [modelsByProvider, providerStatus]);

  const ownerProvider = useMemo(
    () => ownerProviderForModel(llmModel, modelsByProvider, providerStatus),
    [llmModel, modelsByProvider, providerStatus],
  );
  const ownerProviderInfo = ownerProvider ? LLM_PROVIDER_INFO[ownerProvider] : null;
  const ownerConfigured = Boolean(
    ownerProvider &&
      (providerStatus[ownerProvider]?.orgConfigured || providerStatus[ownerProvider]?.platformAvailable),
  );

  const modelMutation = useMutation({
    mutationFn: (data: Record<string, unknown>) => api.settings.update(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["settings"] });
      setSaveStatus("success");
      setTimeout(() => setSaveStatus("idle"), 2000);
    },
    onError: (error) => {
      captureError(error, { feature: "llm-model-save" });
      setSaveStatus("error");
      setTimeout(() => setSaveStatus("idle"), 3000);
    },
  });

  const keyMutation = useMutation({
    mutationFn: ({ provider, config }: { provider: string; config: Record<string, unknown> }) =>
      api.credentials.update(provider, config),
    onSuccess: (_data, variables) => {
      queryClient.invalidateQueries({ queryKey: ["credentials"] });
      setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "success" }));
      setTimeout(() => {
        setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "idle" }));
      }, 2000);
    },
    onError: (err, variables) => {
      captureError(err, { feature: "llm-key-save" });
      setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "error" }));
      setTimeout(() => {
        setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "idle" }));
      }, 3000);
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (provider: string) => api.credentials.delete(provider),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["credentials"] });
      setRemovingProvider(null);
      setEditingProvider(null);
    },
    onError: (error) => {
      captureError(error, { feature: "llm-key-delete" });
    },
  });

  function handleSaveModel() {
    modelMutation.mutate({
      settings: {
        llm_model: llmModel,
        llm_reasoning_effort: reasoningEffort || "",
      },
    });
  }

  function handleSaveKey(provider: string, key: string) {
    if (!key) return;
    const config: Record<string, unknown> = { api_key: key };
    if (provider === "openai") {
      config.api_type = OPENAI_API_TYPE_CHAT;
    }
    setKeySaveStatus((prev) => ({ ...prev, [provider]: "saving" }));
    keyMutation.mutate({ provider, config });
  }

  function openEditor(provider: string) {
    // Clear any lingering save status from a previous save so a freshly opened
    // dialog doesn't immediately auto-close on a stale "success".
    setKeySaveStatus((prev) => ({ ...prev, [provider]: "idle" }));
    setEditingProvider(provider);
  }

  const editingInfo = editingProvider ? LLM_PROVIDER_INFO[editingProvider] : null;
  const editingStatus = editingProvider ? providerStatus[editingProvider] : undefined;
  const editingSaveStatus = editingProvider ? keySaveStatus[editingProvider] ?? "idle" : "idle";

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="LLM"
          description="Configure agent credentials and the AI model for your organization."
        />

        {!hasPlatformLLM && (
          <Card className="border-amber-300 dark:border-amber-700/60 bg-amber-50 dark:bg-amber-950/20">
            <CardContent>
              <div className="flex items-start gap-2">
                <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400" />
                <p className="text-xs text-amber-700 dark:text-amber-300">
                  Platform LLM not configured. Background features (session titles, PR descriptions,
                  project generation, validation, prioritization) will be unavailable. See the{" "}
                  <Link
                    href="https://github.com/assembledhq/143/blob/main/docs/self-hosting/platform-llm.md"
                    target="_blank"
                    rel="noopener noreferrer"
                    className="underline underline-offset-2 hover:text-amber-900 dark:hover:text-amber-200"
                  >
                    self-hosting guide
                  </Link>{" "}
                  to enable them.
                </p>
              </div>
            </CardContent>
          </Card>
        )}

        <section className="space-y-3">
          <h2 className="text-xs font-medium text-foreground">Default model</h2>
          <DefaultModelCard
            value={llmModel}
            reasoningEffort={reasoningEffort}
            modelGroups={enabledModelGroups}
            ownerProvider={ownerProvider}
            ownerProviderInfo={ownerProviderInfo}
            ownerConfigured={ownerConfigured}
            saving={modelMutation.isPending}
            saveStatus={saveStatus}
            onChange={setLlmModel}
            onReasoningChange={setReasoningEffort}
            onSave={handleSaveModel}
          />
        </section>

        <section className="space-y-3">
          <h2 className="text-xs font-medium text-foreground">Provider keys</h2>
          <p className="text-xs text-muted-foreground">
            Add API keys for this org. Keys flow to coding agent sessions and can power the default
            model above when the matching provider is selected.
          </p>
          <div className="space-y-2">
            {LLM_PROVIDERS.map((provider) => (
              <ProviderKeyRow
                key={provider}
                provider={provider}
                info={LLM_PROVIDER_INFO[provider]}
                status={providerStatus[provider] ?? { orgConfigured: false, platformAvailable: false }}
                isDefaultOwner={provider === ownerProvider}
                onEdit={() => openEditor(provider)}
              />
            ))}
          </div>
        </section>
      </div>

      {editingProvider && editingInfo && (
        <ProviderKeyDialog
          open={!!editingProvider}
          onOpenChange={(open) => {
            if (!open) setEditingProvider(null);
          }}
          provider={editingProvider}
          info={editingInfo}
          existingMaskedKey={editingStatus?.maskedKey}
          saveStatus={editingSaveStatus}
          onSave={(key) => handleSaveKey(editingProvider, key)}
          onRemove={
            editingStatus?.orgConfigured ? () => setRemovingProvider(editingProvider) : undefined
          }
        />
      )}

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
