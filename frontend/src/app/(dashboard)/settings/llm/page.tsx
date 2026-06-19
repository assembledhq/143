"use client";

import { useMemo, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { useAutosave } from "@/hooks/useAutosave";
import { queryKeys } from "@/lib/query-keys";
import {
  applyOrgSettingsPatch,
  coalesceSettingsPatch,
  type SettingsPatch,
} from "@/lib/settings-autosave";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
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
  PLATFORM_DEFAULT_ALLOWED_MODELS,
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

const EMPTY_PROVIDER_STATUS = {
  orgConfigured: false,
  platformAvailable: false,
} as const;

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

  const [keySaveStatus, setKeySaveStatus] = useState<Record<string, SaveStatus>>({});
  const [keySaveError, setKeySaveError] = useState<Record<string, string>>({});
  const [removingProvider, setRemovingProvider] = useState<string | null>(null);
  const [removeError, setRemoveError] = useState<string | null>(null);
  const [editingProvider, setEditingProvider] = useState<string | null>(null);

  const llmModel = orgSettings.llm_model || DEFAULT_LLM_MODEL;
  const reasoningEffort = orgSettings.llm_reasoning_effort || "";

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

  // Filter each provider's model list down to a cost-safe subset when the org
  // is leaning on the platform default (143's key). App-level LLM runtime is
  // currently backed by platform credentials, so org keys do not unlock the
  // stronger default models here.
  const enabledModelGroups = useMemo(() => {
    return Object.entries(modelsByProvider)
      .filter(([provider]) => {
        const ps = providerStatus[provider];
        return ps?.platformAvailable;
      })
      .map(([provider, group]) => {
        const ps = providerStatus[provider];
        const restriction = PLATFORM_DEFAULT_ALLOWED_MODELS[provider];
        if (restriction && ps?.platformAvailable) {
          const allowed = new Set(restriction);
          return { ...group, models: group.models.filter((m) => allowed.has(m)) };
        }
        return group;
      })
      .filter((group) => group.models.length > 0);
  }, [modelsByProvider, providerStatus]);

  const platformProviderStatus = useMemo(() => {
    const status: Record<string, { orgConfigured: boolean; platformAvailable: boolean }> = {};
    for (const provider of LLM_PROVIDERS) {
      status[provider] = {
        orgConfigured: false,
        platformAvailable: Boolean(platformProviders[provider]),
      };
    }
    return status;
  }, [platformProviders]);

  const ownerProvider = useMemo(
    () => ownerProviderForModel(llmModel, modelsByProvider, platformProviderStatus),
    [llmModel, modelsByProvider, platformProviderStatus],
  );
  const ownerProviderInfo = ownerProvider ? LLM_PROVIDER_INFO[ownerProvider] : null;
  const ownerConfigured = Boolean(
    ownerProvider && platformProviderStatus[ownerProvider]?.platformAvailable,
  );
  const ownerUsesPlatformDefault = Boolean(
    ownerProvider && platformProviderStatus[ownerProvider]?.platformAvailable,
  );
  const ownerHasRestriction = Boolean(
    ownerProvider && PLATFORM_DEFAULT_ALLOWED_MODELS[ownerProvider],
  );

  const autosave = useAutosave<SettingsPatch>({
    queryKey: queryKeys.settings.all,
    mutationFn: (payload) => api.settings.update(payload),
    applyOptimistic: applyOrgSettingsPatch,
    coalesce: coalesceSettingsPatch,
  });

  const keyMutation = useMutation({
    mutationFn: ({ provider, config }: { provider: string; config: Record<string, unknown> }) =>
      api.credentials.update(provider, config),
    onSuccess: (_data, variables) => {
      queryClient.invalidateQueries({ queryKey: ["credentials"] });
      setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "success" }));
      setKeySaveError((prev) => {
        const next = { ...prev };
        delete next[variables.provider];
        return next;
      });
      setTimeout(() => {
        setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "idle" }));
      }, 2000);
    },
    onError: (err, variables) => {
      captureError(err, { feature: "llm-key-save" });
      setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "error" }));
      setKeySaveError((prev) => ({
        ...prev,
        [variables.provider]: err instanceof Error ? err.message : String(err),
      }));
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
      setRemoveError(null);
      setEditingProvider(null);
    },
    onError: (error) => {
      captureError(error, { feature: "llm-key-delete" });
      setRemoveError(error instanceof Error ? error.message : String(error));
      setRemovingProvider(null);
    },
  });

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
  const editingSaveError = editingProvider ? keySaveError[editingProvider] : undefined;

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="App LLM"
          description="Configure models for app-generated titles, PR descriptions, validation, prioritization, and project generation. Coding-agent credentials are managed separately on Coding agents."
          action={(
            <Button asChild variant="outline" size="sm">
              <Link href="/settings/agent">Coding agents</Link>
            </Button>
          )}
        />

        {!hasPlatformLLM && (
          <Card className="border-warning/30 bg-warning/10">
            <CardContent>
              <div className="flex items-start gap-2">
                <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warning" />
                <p className="text-xs text-warning">
                  Platform LLM not configured. Background features (session titles, PR descriptions,
                  project generation, validation, prioritization) will be unavailable. See the{" "}
                  <Link
                    href="https://github.com/assembledhq/143/blob/main/docs/self-hosting/platform-llm.md"
                    target="_blank"
                    rel="noopener noreferrer"
                    className="underline underline-offset-2 hover:text-warning/80"
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
          <div className="flex items-center justify-between">
            <h2 className="text-xs font-medium text-foreground">Default model</h2>
            <AutosaveIndicator status={autosave.status} />
          </div>
          <p className="text-xs text-muted-foreground">
            Choose the default model for app features like PR descriptions, titles, validation, and
            project generation.
          </p>
          <DefaultModelCard
            value={llmModel}
            reasoningEffort={reasoningEffort}
            modelGroups={enabledModelGroups}
            ownerProvider={ownerProvider}
            ownerProviderInfo={ownerProviderInfo}
            ownerConfigured={ownerConfigured}
            ownerUsesPlatformDefault={ownerUsesPlatformDefault}
            ownerHasModelRestriction={ownerHasRestriction}
            onChange={(model) => autosave.save({ settings: { llm_model: model } })}
            onReasoningChange={(effort) =>
              autosave.save({
                settings: {
                  llm_reasoning_effort: effort as "" | "low" | "medium" | "high" | "xhigh" | "max",
                },
              })
            }
          />
        </section>

        <section className="space-y-3">
          <h2 className="text-xs font-medium text-foreground">Provider keys</h2>
          <p className="text-xs text-muted-foreground">
            Add API keys for these app-level LLM features. These keys power the default model above
            and are separate from the coding agent credentials on the Agent page.
          </p>
          <div className="space-y-2">
            {LLM_PROVIDERS.map((provider) => (
              <ProviderKeyRow
                key={provider}
                provider={provider}
                info={LLM_PROVIDER_INFO[provider]}
                status={providerStatus[provider] ?? EMPTY_PROVIDER_STATUS}
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
          info={editingInfo}
          existingMaskedKey={editingStatus?.maskedKey}
          saveStatus={editingSaveStatus}
          errorMessage={editingSaveError}
          onSave={(key) => handleSaveKey(editingProvider, key)}
          onRemove={
            editingStatus?.orgConfigured ? () => setRemovingProvider(editingProvider) : undefined
          }
        />
      )}

      {removeError && (
        <AlertDialog open onOpenChange={(open) => !open && setRemoveError(null)}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Couldn&apos;t remove API key</AlertDialogTitle>
              <AlertDialogDescription>{removeError}</AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogAction onClick={() => setRemoveError(null)}>Dismiss</AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
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
