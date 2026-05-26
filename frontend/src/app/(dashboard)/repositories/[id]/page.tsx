"use client";

import { use, useState, type FormEvent } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, MonitorPlay, TestTube2, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { notify as toast } from "@/lib/notify";
import { queryKeys } from "@/lib/query-keys";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { RepoPMSettingsEditor } from "@/components/repo-pm-settings";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { useAuth } from "@/hooks/use-auth";
import { usePageTitle } from "@/hooks/use-page-title";
import type {
  ListResponse,
  PreviewSecretBundleOutput,
  PreviewSecretBundleSummary,
  PreviewSecretBundleUpsertRequest,
  Repository,
  SingleResponse,
} from "@/lib/types";

export default function RepositoryDetailPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <RepositoryDetailContent id={id} />;
}

export function RepositoryDetailContent({ id }: { id: string }) {
  const { data, isLoading } = useQuery<SingleResponse<Repository>>({
    queryKey: ["repository", id],
    queryFn: () => api.repositories.get(id),
  });

  const repo = data?.data;
  usePageTitle(repo?.full_name, "Repository");

  if (isLoading) {
    return (
      <PageContainer size="default">
        <div className="space-y-6">
          <PageHeader title="Repository" description="Loading..." />
        </div>
      </PageContainer>
    );
  }

  if (!repo) {
    return (
      <PageContainer size="default">
        <div className="space-y-6">
          <PageHeader title="Repository" description="Not found." />
        </div>
      </PageContainer>
    );
  }

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title={repo.full_name}
          description="Repository settings and PM agent configuration."
          action={
            <div className="flex items-center gap-2">
              <Badge variant={repo.status === "active" ? "default" : "secondary"}>
                {repo.status}
              </Badge>
              <Button asChild variant="outline">
                <Link href={`/previews/new?repo=${repo.id}`}>
                  <MonitorPlay className="h-4 w-4" />
                  Preview branch
                </Link>
              </Button>
            </div>
          }
        />

        <section className="space-y-3">
          <h2 className="text-xs font-medium text-foreground">PM agent settings</h2>
          <p className="text-xs text-muted-foreground">
            Customize how the PM agent behaves for this repository, or use your organization defaults.
          </p>
          <RepoPMSettingsEditor repository={repo} />
        </section>

        <PreviewSecretBundlesSection repositoryId={repo.id} />
      </div>
    </PageContainer>
  );
}

const defaultValuesJSON = "{\n  \"DATABASE_URL\": \"\"\n}";
const defaultOutputsJSON = "[\n  {\n    \"type\": \"env\",\n    \"values\": {\n      \"DATABASE_URL\": \"secret:DATABASE_URL\"\n    }\n  }\n]";

function PreviewSecretBundlesSection({ repositoryId }: { repositoryId: string }) {
  const { user } = useAuth();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [valuesText, setValuesText] = useState(defaultValuesJSON);
  const [outputsText, setOutputsText] = useState(defaultOutputsJSON);
  const [formError, setFormError] = useState<string | null>(null);

  const bundlesQuery = useQuery<ListResponse<PreviewSecretBundleSummary>>({
    queryKey: queryKeys.repositories.previewSecretBundles(repositoryId),
    queryFn: () => api.repositories.previewSecretBundles.list(repositoryId),
  });
  const bundles = bundlesQuery.data?.data ?? [];
  const canManageBundles = user?.role === "admin";

  const saveMutation = useMutation({
    mutationFn: (body: PreviewSecretBundleUpsertRequest) =>
      api.repositories.previewSecretBundles.upsert(repositoryId, body),
    onSuccess: () => {
      setFormError(null);
      toast.success("Preview secret bundle saved");
      void queryClient.invalidateQueries({ queryKey: queryKeys.repositories.previewSecretBundles(repositoryId) });
    },
    onError: (error) => {
      toast.error(error instanceof ApiError ? error.message : "Could not save preview secret bundle");
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (bundleName: string) => api.repositories.previewSecretBundles.delete(repositoryId, bundleName),
    onSuccess: () => {
      toast.success("Preview secret bundle removed");
      void queryClient.invalidateQueries({ queryKey: queryKeys.repositories.previewSecretBundles(repositoryId) });
    },
    onError: (error) => {
      toast.error(error instanceof ApiError ? error.message : "Could not remove preview secret bundle");
    },
  });

  const testMutation = useMutation({
    mutationFn: (bundleId: string) => api.repositories.previewSecretBundles.test(bundleId),
    onSuccess: (response) => {
      if (response.data.status === "ready") {
        toast.success("Preview secret bundle is ready");
      } else {
        toast.error(response.data.error || "Preview secret bundle test failed");
      }
    },
    onError: (error) => {
      toast.error(error instanceof ApiError ? error.message : "Could not test preview secret bundle");
    },
  });

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trimmedName = name.trim();
    if (!trimmedName) {
      setFormError("Bundle name is required.");
      return;
    }
    let values: Record<string, string>;
    let outputs: PreviewSecretBundleOutput[];
    try {
      values = parseSecretValues(valuesText);
      outputs = parseSecretOutputs(outputsText);
    } catch (err) {
      setFormError(err instanceof Error ? err.message : "Bundle JSON is invalid.");
      return;
    }
    setFormError(null);
    saveMutation.mutate({
      name: trimmedName,
      source: {
        type: "managed",
        values,
      },
      outputs,
      exposure_policy: "preview_runtime",
    });
  }

  return (
    <section className="space-y-3">
      <div className="flex flex-col gap-1">
        <h2 className="text-xs font-medium text-foreground">Preview secrets</h2>
        <p className="text-xs text-muted-foreground">
          Manage repo-scoped bundles used by preview services.
        </p>
      </div>

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(280px,360px)]">
        <Card>
          <CardHeader>
            <CardTitle>Bundles</CardTitle>
            <CardDescription>{bundles.length} configured</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            {bundlesQuery.isLoading ? (
              <p className="text-xs text-muted-foreground">Loading bundles...</p>
            ) : bundles.length === 0 ? (
              <div className="flex min-h-24 items-center justify-center rounded-md border border-dashed border-border px-4 text-center">
                <p className="text-xs text-muted-foreground">No preview secret bundles configured.</p>
              </div>
            ) : (
              bundles.map((bundle) => (
                <div key={bundle.id} className="rounded-md border border-border p-3">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0 space-y-1">
                      <div className="flex items-center gap-2">
                        <KeyRound className="h-4 w-4 text-muted-foreground" />
                        <p className="truncate text-xs font-medium text-foreground">{bundle.name}</p>
                      </div>
                      <p className="text-xs text-muted-foreground">
                        {bundle.source_type} - {bundle.exposure_policy}
                      </p>
                    </div>
                    <div className="flex shrink-0 items-center gap-1">
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        aria-label={`Test ${bundle.name}`}
                        onClick={() => testMutation.mutate(bundle.id)}
                        disabled={testMutation.isPending}
                      >
                        <TestTube2 className="h-4 w-4" />
                      </Button>
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        aria-label={`Remove ${bundle.name}`}
                        onClick={() => deleteMutation.mutate(bundle.name)}
                        disabled={deleteMutation.isPending}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
                  </div>
                  {bundle.outputs.length > 0 ? (
                    <div className="mt-3 flex flex-wrap gap-1.5">
                      {bundle.outputs.map((output, index) => (
                        <Badge key={`${bundle.id}-${index}`} variant="secondary">
                          {formatOutputSummary(output)}
                        </Badge>
                      ))}
                    </div>
                  ) : null}
                </div>
              ))
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Create or update</CardTitle>
            <CardDescription>
              {canManageBundles ? "Managed values only" : "Admin access required"}
            </CardDescription>
          </CardHeader>
          <CardContent>
            {canManageBundles ? (
              <form className="space-y-3" onSubmit={handleSubmit}>
                <div className="space-y-1.5">
                  <Label htmlFor="preview-secret-bundle-name">Bundle name</Label>
                  <Input
                    id="preview-secret-bundle-name"
                    value={name}
                    onChange={(event) => setName(event.target.value)}
                    placeholder="assembled-dev"
                    autoComplete="off"
                  />
                </div>

                <div className="space-y-1.5">
                  <Label htmlFor="preview-secret-values">Secret values</Label>
                  <Textarea
                    id="preview-secret-values"
                    value={valuesText}
                    onChange={(event) => setValuesText(event.target.value)}
                    spellCheck={false}
                    className="min-h-28 font-mono text-xs"
                  />
                </div>

                <div className="space-y-1.5">
                  <Label htmlFor="preview-secret-outputs">Outputs</Label>
                  <Textarea
                    id="preview-secret-outputs"
                    value={outputsText}
                    onChange={(event) => setOutputsText(event.target.value)}
                    spellCheck={false}
                    className="min-h-36 font-mono text-xs"
                  />
                </div>

                {formError ? (
                  <p className="text-xs text-destructive">{formError}</p>
                ) : null}

                <Button type="submit" disabled={saveMutation.isPending}>
                  <KeyRound className="h-4 w-4" />
                  Save bundle
                </Button>
              </form>
            ) : (
              <p className="text-xs text-muted-foreground">
                Ask an org admin to create or update preview secret bundles for this repository.
              </p>
            )}
          </CardContent>
        </Card>
      </div>
    </section>
  );
}

function parseSecretValues(raw: string): Record<string, string> {
  const parsed = JSON.parse(raw) as unknown;
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("Secret values must be a JSON object.");
  }
  const values: Record<string, string> = {};
  for (const [key, value] of Object.entries(parsed)) {
    if (typeof value !== "string") {
      throw new Error(`Secret value ${key} must be a string.`);
    }
    values[key] = value;
  }
  return values;
}

function parseSecretOutputs(raw: string): PreviewSecretBundleOutput[] {
  const parsed = JSON.parse(raw) as unknown;
  if (!Array.isArray(parsed)) {
    throw new Error("Outputs must be a JSON array.");
  }
  return parsed as PreviewSecretBundleOutput[];
}

function formatOutputSummary(output: PreviewSecretBundleSummary["outputs"][number]): string {
  if (output.type === "env") {
    return `env ${output.env?.join(", ") || "values"}`;
  }
  if (output.type === "file") {
    return `${output.format || "raw"} ${output.path || "file"}`;
  }
  return output.type;
}
