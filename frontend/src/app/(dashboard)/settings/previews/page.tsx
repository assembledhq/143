"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Plus, Trash2 } from "lucide-react";

import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { ListResponse, PreviewAPIToken, Repository } from "@/lib/types";

const SCOPES = ["previews:create", "previews:read", "previews:stop"] as const;

export default function PreviewSettingsPage() {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<string[]>([...SCOPES]);
  const [repositoryIDs, setRepositoryIDs] = useState<string[]>([]);
  const [createdToken, setCreatedToken] = useState("");

  const tokensQuery = useQuery<ListResponse<PreviewAPIToken>>({
    queryKey: ["preview-api-tokens"],
    queryFn: () => api.previews.apiTokens.list(),
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

  const tokens = tokensQuery.data?.data ?? [];
  const repositories = repositoriesQuery.data?.data ?? [];

  function toggleScope(scope: string) {
    setScopes((current) => current.includes(scope) ? current.filter((item) => item !== scope) : [...current, scope]);
  }

  function toggleRepository(id: string) {
    setRepositoryIDs((current) => current.includes(id) ? current.filter((item) => item !== id) : [...current, id]);
  }

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader title="Preview API" description="Manage scoped tokens for branch and pull request previews." />

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
