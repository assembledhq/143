"use client";

import { useMemo, useState } from "react";
import { useSearchParams } from "next/navigation";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ExternalLink, MonitorPlay, Play } from "lucide-react";

import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { api } from "@/lib/api";
import type { BranchPreviewResponse, ListResponse, Repository } from "@/lib/types";

export default function NewPreviewPage() {
  const searchParams = useSearchParams();
  const [repositoryId, setRepositoryId] = useState(searchParams.get("repo") ?? "");
  const [branch, setBranch] = useState("");
  const [commitSha, setCommitSha] = useState("");
  const [previewConfigName, setPreviewConfigName] = useState("");

  const repositoriesQuery = useQuery<ListResponse<Repository>>({
    queryKey: ["repositories"],
    queryFn: () => api.repositories.list(),
  });

  const repositories = useMemo(() => repositoriesQuery.data?.data ?? [], [repositoriesQuery.data?.data]);
  const selectedRepo = useMemo(
    () => repositories.find((repo) => repo.id === repositoryId),
    [repositories, repositoryId],
  );

  const branchesQuery = useQuery({
    queryKey: ["repository-branches", repositoryId],
    queryFn: () => api.repositories.branches(repositoryId),
    enabled: repositoryId.length > 0,
  });

  const createPreview = useMutation({
    mutationFn: () =>
      api.previews.create({
        repository_id: repositoryId,
        branch,
        commit_sha: commitSha.trim() || undefined,
        preview_config_name: previewConfigName.trim() || null,
        source: { type: "manual" },
      }),
  });

  const result: BranchPreviewResponse | undefined = createPreview.data?.data;
  const canStart = repositoryId.length > 0 && branch.trim().length > 0 && !createPreview.isPending;

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="Create preview"
          description="Start from a repository branch and pin the commit that should run."
        />

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <MonitorPlay className="h-4 w-4" />
              Branch target
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-5">
            <div className="grid gap-4 md:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="preview-repository">Repository</Label>
                <Select
                  value={repositoryId}
                  onValueChange={(value) => {
                    setRepositoryId(value);
                    setBranch("");
                  }}
                >
                  <SelectTrigger id="preview-repository">
                    <SelectValue placeholder={repositoriesQuery.isLoading ? "Loading repositories..." : "Choose repository"} />
                  </SelectTrigger>
                  <SelectContent>
                    {repositories.map((repo) => (
                      <SelectItem key={repo.id} value={repo.id}>
                        {repo.full_name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>

              <div className="space-y-2">
                <Label htmlFor="preview-branch">Branch</Label>
                <Select value={branch} onValueChange={setBranch} disabled={!repositoryId}>
                  <SelectTrigger id="preview-branch">
                    <SelectValue placeholder={branchesQuery.isLoading ? "Loading branches..." : "Choose branch"} />
                  </SelectTrigger>
                  <SelectContent>
                    {(branchesQuery.data?.data ?? []).map((item) => (
                      <SelectItem key={item.name} value={item.name}>
                        {item.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>

            <div className="space-y-2">
              <Label htmlFor="preview-commit">Commit SHA</Label>
              <Input
                id="preview-commit"
                value={commitSha}
                onChange={(event) => setCommitSha(event.target.value)}
                placeholder="Resolve branch head automatically"
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="preview-config-name">Preview config</Label>
              <Input
                id="preview-config-name"
                value={previewConfigName}
                onChange={(event) => setPreviewConfigName(event.target.value)}
                placeholder="Auto-select default config"
              />
            </div>

            {createPreview.isError ? (
              <p className="text-sm text-destructive">
                {createPreview.error instanceof Error ? createPreview.error.message : "Preview could not be created."}
              </p>
            ) : null}

            <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
              <div className="min-h-5 text-sm text-muted-foreground">
                {selectedRepo ? (
                  <span>
                    {selectedRepo.full_name}
                    {branch ? ` · ${branch}` : ""}
                  </span>
                ) : null}
              </div>
              <Button type="button" onClick={() => createPreview.mutate()} disabled={!canStart}>
                <Play className="h-4 w-4" />
                Start preview
              </Button>
            </div>
          </CardContent>
        </Card>

        {result ? (
          <Card>
            <CardContent className="flex flex-col gap-3 pt-6 sm:flex-row sm:items-center sm:justify-between">
              <div className="space-y-1">
                <div className="flex items-center gap-2">
                  <p className="text-sm font-medium text-foreground">Stable preview link</p>
                  <Badge variant="secondary">{result.status.replaceAll("_", " ")}</Badge>
                </div>
                <p className="break-all text-sm text-muted-foreground">{result.stable_url}</p>
              </div>
              <Button asChild variant="outline">
                <a href={result.stable_url}>
                  <ExternalLink className="h-4 w-4" />
                  Open
                </a>
              </Button>
            </CardContent>
          </Card>
        ) : null}
      </div>
    </PageContainer>
  );
}
