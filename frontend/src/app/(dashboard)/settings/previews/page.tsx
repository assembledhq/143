"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Plus, Save, Trash2 } from "lucide-react";

import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { ListResponse, PreviewAPIToken, PreviewSecretBundleSummary, Repository } from "@/lib/types";

const SCOPES = ["previews:create", "previews:read", "previews:stop"] as const;

type SecretEnvRow = {
  key: string;
  value: string;
};

export default function PreviewSettingsPage() {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<string[]>([...SCOPES]);
  const [repositoryIDs, setRepositoryIDs] = useState<string[]>([]);
  const [createdToken, setCreatedToken] = useState("");
  const [bundleName, setBundleName] = useState("");
  const [envRows, setEnvRows] = useState<SecretEnvRow[]>([{ key: "", value: "" }]);

  const tokensQuery = useQuery<ListResponse<PreviewAPIToken>>({
    queryKey: ["preview-api-tokens"],
    queryFn: () => api.previews.apiTokens.list(),
  });
  const secretBundlesQuery = useQuery<ListResponse<PreviewSecretBundleSummary>>({
    queryKey: ["preview-secret-bundles"],
    queryFn: () => api.previews.secretBundles.list(),
  });
  const repositoriesQuery = useQuery<ListResponse<Repository>>({
    queryKey: ["repositories"],
    queryFn: () => api.repositories.list(),
  });

  const createToken = useMutation({
    mutationFn: () => api.previews.apiTokens.create({ name: name.trim(), scopes, repository_ids: repositoryIDs }),
    onSuccess: (response) => {
      setCreatedToken(response.data.token);
      setName("");
      setScopes([...SCOPES]);
      setRepositoryIDs([]);
      void queryClient.invalidateQueries({ queryKey: ["preview-api-tokens"] });
    },
  });
  const revokeToken = useMutation({
    mutationFn: (id: string) => api.previews.apiTokens.revoke(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["preview-api-tokens"] });
    },
  });
  const upsertSecretBundle = useMutation({
    mutationFn: () => api.previews.secretBundles.upsert(bundleName.trim(), collectEnvRows(envRows)),
    onSuccess: () => {
      setBundleName("");
      setEnvRows([{ key: "", value: "" }]);
      void queryClient.invalidateQueries({ queryKey: ["preview-secret-bundles"] });
    },
  });
  const deleteSecretBundle = useMutation({
    mutationFn: (name: string) => api.previews.secretBundles.delete(name),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["preview-secret-bundles"] });
    },
  });

  const tokens = tokensQuery.data?.data ?? [];
  const secretBundles = secretBundlesQuery.data?.data ?? [];
  const repositories = repositoriesQuery.data?.data ?? [];
  const bundleEnv = collectEnvRows(envRows);

  function toggleScope(scope: string) {
    setScopes((current) => current.includes(scope) ? current.filter((item) => item !== scope) : [...current, scope]);
  }

  function toggleRepository(id: string) {
    setRepositoryIDs((current) => current.includes(id) ? current.filter((item) => item !== id) : [...current, id]);
  }

  function updateEnvRow(index: number, patch: Partial<SecretEnvRow>) {
    setEnvRows((current) => current.map((row, i) => i === index ? { ...row, ...patch } : row));
  }

  function removeEnvRow(index: number) {
    setEnvRows((current) => current.length === 1 ? [{ key: "", value: "" }] : current.filter((_, i) => i !== index));
  }

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader title="Preview API" description="Manage scoped tokens for branch and pull request previews." />

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <KeyRound className="h-4 w-4" />
              Secret bundles
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-5">
            <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(280px,420px)]">
              <div className="space-y-3">
                {secretBundles.map((bundle) => (
                  <div key={bundle.id || bundle.name} className="flex flex-col gap-3 rounded-md border border-border p-3 sm:flex-row sm:items-center sm:justify-between">
                    <div className="min-w-0 space-y-1">
                      <p className="font-medium text-foreground">{bundle.name}</p>
                      <div className="flex flex-wrap gap-1">
                        {bundle.env_names.map((envName) => <Badge key={envName} variant="secondary">{envName}</Badge>)}
                      </div>
                      <p className="text-xs text-muted-foreground">Update this bundle by re-entering every value for the env vars it should contain.</p>
                    </div>
                    <Button type="button" variant="outline" onClick={() => deleteSecretBundle.mutate(bundle.name)} disabled={deleteSecretBundle.isPending}>
                      <Trash2 className="h-4 w-4" />
                      Delete
                    </Button>
                  </div>
                ))}
                {!secretBundles.length && !secretBundlesQuery.isLoading ? (
                  <p className="text-sm text-muted-foreground">No preview secret bundles.</p>
                ) : null}
              </div>

              <div className="space-y-4 rounded-md border border-border p-3">
                <div className="space-y-2">
                  <Label htmlFor="preview-secret-bundle-name">Bundle name</Label>
                  <Input id="preview-secret-bundle-name" value={bundleName} onChange={(event) => setBundleName(event.target.value)} placeholder="staging" />
                </div>
                <div className="space-y-2">
                  <Label>Environment variables</Label>
                  <div className="space-y-2">
                    {envRows.map((row, index) => (
                      <div key={index} className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]">
                        <Input value={row.key} onChange={(event) => updateEnvRow(index, { key: event.target.value.toUpperCase() })} placeholder="API_TOKEN" aria-label="Environment variable name" />
                        <Input value={row.value} onChange={(event) => updateEnvRow(index, { value: event.target.value })} placeholder="Secret value" type="password" aria-label="Environment variable value" />
                        <Button type="button" variant="outline" size="icon" onClick={() => removeEnvRow(index)} aria-label="Remove environment variable">
                          <Trash2 className="h-4 w-4" />
                        </Button>
                      </div>
                    ))}
                  </div>
                  <Button type="button" variant="outline" onClick={() => setEnvRows((current) => [...current, { key: "", value: "" }])}>
                    <Plus className="h-4 w-4" />
                    Add variable
                  </Button>
                </div>
                {upsertSecretBundle.isError ? (
                  <p className="text-sm text-destructive">{upsertSecretBundle.error instanceof Error ? upsertSecretBundle.error.message : "Secret bundle could not be saved."}</p>
                ) : null}
                <Button type="button" onClick={() => upsertSecretBundle.mutate()} disabled={!bundleName.trim() || Object.keys(bundleEnv).length === 0 || upsertSecretBundle.isPending}>
                  <Save className="h-4 w-4" />
                  Save bundle
                </Button>
              </div>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <Plus className="h-4 w-4" />
              Create token
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-5">
            <div className="space-y-2">
              <Label htmlFor="preview-token-name">Name</Label>
              <Input id="preview-token-name" value={name} onChange={(event) => setName(event.target.value)} placeholder="CI previews" />
            </div>

            <div className="space-y-2">
              <Label>Scopes</Label>
              <div className="grid gap-2 md:grid-cols-3">
                {SCOPES.map((scope) => (
                  <Label key={scope} className="flex items-center gap-2 rounded-md border border-border px-3 py-2 text-sm">
                    <Checkbox checked={scopes.includes(scope)} onCheckedChange={() => toggleScope(scope)} />
                    {scope}
                  </Label>
                ))}
              </div>
            </div>

            <div className="space-y-2">
              <Label>Repository access</Label>
              <div className="grid max-h-56 gap-2 overflow-auto rounded-md border border-border p-2 md:grid-cols-2">
                {repositories.map((repo) => (
                  <Label key={repo.id} className="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm">
                    <Checkbox checked={repositoryIDs.includes(repo.id)} onCheckedChange={() => toggleRepository(repo.id)} />
                    <span className="truncate">{repo.full_name}</span>
                  </Label>
                ))}
              </div>
              <p className="text-xs text-muted-foreground">Leave every repository unchecked to allow all repositories.</p>
            </div>

            {createdToken ? (
              <p className="break-all rounded-md border border-border bg-muted/40 p-3 text-sm text-foreground">{createdToken}</p>
            ) : null}
            {createToken.isError ? (
              <p className="text-sm text-destructive">{createToken.error instanceof Error ? createToken.error.message : "Token could not be created."}</p>
            ) : null}
            <Button type="button" onClick={() => createToken.mutate()} disabled={!name.trim() || scopes.length === 0 || createToken.isPending}>
              <KeyRound className="h-4 w-4" />
              Create token
            </Button>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base">Active tokens</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            {tokens.map((token) => (
              <div key={token.id} className="flex flex-col gap-3 rounded-md border border-border p-3 sm:flex-row sm:items-center sm:justify-between">
                <div className="min-w-0 space-y-1">
                  <p className="font-medium text-foreground">{token.name}</p>
                  <div className="flex flex-wrap gap-1">
                    {token.scopes.map((scope) => <Badge key={scope} variant="secondary">{scope}</Badge>)}
                  </div>
                  <p className="text-xs text-muted-foreground">
                    {token.repository_ids.length ? `${token.repository_ids.length} repositories` : "All repositories"}
                    {token.last_used_at ? ` · Last used ${new Date(token.last_used_at).toLocaleString()}` : ""}
                  </p>
                </div>
                <Button type="button" variant="outline" onClick={() => revokeToken.mutate(token.id)} disabled={revokeToken.isPending}>
                  <Trash2 className="h-4 w-4" />
                  Revoke
                </Button>
              </div>
            ))}
            {!tokens.length && !tokensQuery.isLoading ? (
              <p className="text-sm text-muted-foreground">No preview API tokens.</p>
            ) : null}
          </CardContent>
        </Card>
      </div>
    </PageContainer>
  );
}

function collectEnvRows(rows: SecretEnvRow[]): Record<string, string> {
  return rows.reduce<Record<string, string>>((acc, row) => {
    const key = row.key.trim();
    if (key && row.value) {
      acc[key] = row.value;
    }
    return acc;
  }, {});
}
