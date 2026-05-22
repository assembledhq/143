"use client";

import { useMemo, useState } from "react";
import { useSearchParams } from "next/navigation";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ChevronDown, ExternalLink, MonitorPlay, Play } from "lucide-react";

import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
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
  const [ttlSeconds, setTTLSeconds] = useState("");
  const [repoSearch, setRepoSearch] = useState("");
  const [branchSearch, setBranchSearch] = useState("");
  const [optionsOpen, setOptionsOpen] = useState(false);

  const repositoriesQuery = useQuery<ListResponse<Repository>>({
    queryKey: ["repositories"],
    queryFn: () => api.repositories.list(),
  });

  const repositories = useMemo(() => repositoriesQuery.data?.data ?? [], [repositoriesQuery.data?.data]);
  const filteredRepositories = useMemo(
    () => repositories.filter((repo) => repo.full_name.toLowerCase().includes(repoSearch.trim().toLowerCase())),
    [repositories, repoSearch],
  );
  const selectedRepo = useMemo(
    () => repositories.find((repo) => repo.id === repositoryId),
    [repositories, repositoryId],
  );

  const branchesQuery = useQuery({
    queryKey: ["repository-branches", repositoryId],
    queryFn: () => api.repositories.branches(repositoryId),
    enabled: repositoryId.length > 0,
  });

  const recentPreviewsQuery = useQuery<ListResponse<BranchPreviewResponse>>({
    queryKey: ["recent-previews", repositoryId],
    queryFn: () => api.previews.list({ repository_id: repositoryId }),
    enabled: repositoryId.length > 0,
  });

  const createPreview = useMutation({
    mutationFn: () =>
      api.previews.create({
        repository_id: repositoryId,
        branch,
        commit_sha: commitSha.trim() || undefined,
        preview_config_name: previewConfigName.trim() || configOptions?.selected_name || null,
        ttl_seconds: ttlSeconds.trim() ? Number(ttlSeconds) : undefined,
        source: { type: "manual" },
      }),
  });

  const configOptionsQuery = useQuery({
    queryKey: ["preview-config-options", repositoryId, branch, commitSha],
    queryFn: () =>
      api.previews.configOptions({
        repository_id: repositoryId,
        branch,
        commit_sha: commitSha.trim() || undefined,
      }),
    enabled: repositoryId.length > 0 && branch.trim().length > 0,
  });

  const configOptions = configOptionsQuery.data?.data;
  const branches = useMemo(() => branchesQuery.data?.data ?? [], [branchesQuery.data?.data]);
  const recentBranchTimes = useMemo(() => {
    const byBranch = new Map<string, number>();
    for (const preview of recentPreviewsQuery.data?.data ?? []) {
      if (!preview.branch || !preview.created_at) {
        continue;
      }
      const createdAt = Date.parse(preview.created_at);
      if (Number.isNaN(createdAt)) {
        continue;
      }
      byBranch.set(preview.branch, Math.max(byBranch.get(preview.branch) ?? 0, createdAt));
    }
    return byBranch;
  }, [recentPreviewsQuery.data?.data]);
  const filteredBranches = useMemo(() => {
    const query = branchSearch.trim().toLowerCase();
    return [...branches]
      .filter((item) => item.name.toLowerCase().includes(query))
      .sort((a, b) => {
        const recency = (recentBranchTimes.get(b.name) ?? 0) - (recentBranchTimes.get(a.name) ?? 0);
        if (recency !== 0) {
          return recency;
        }
        return a.name.localeCompare(b.name);
      });
  }, [branches, branchSearch, recentBranchTimes]);

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
                    <div className="p-2">
                      <Input
                        value={repoSearch}
                        onChange={(event) => setRepoSearch(event.target.value)}
                        placeholder="Search repositories"
                      />
                    </div>
                    {filteredRepositories.length ? (
                      filteredRepositories.map((repo) => (
                        <SelectItem key={repo.id} value={repo.id}>
                          {repo.full_name}
                        </SelectItem>
                      ))
                    ) : (
                      <div className="px-2 py-1.5 text-sm text-muted-foreground">No repositories match</div>
                    )}
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
                    <div className="p-2">
                      <Input
                        value={branchSearch}
                        onChange={(event) => setBranchSearch(event.target.value)}
                        placeholder="Search branches"
                      />
                    </div>
                    {filteredBranches.length ? (
                      filteredBranches.map((item) => (
                        <SelectItem key={item.name} value={item.name}>
                          <span className="flex min-w-0 items-center gap-2">
                            <span className="truncate">{item.name}</span>
                            {recentBranchTimes.has(item.name) ? (
                              <Badge variant="secondary" className="shrink-0 text-xs">
                                Recent
                              </Badge>
                            ) : null}
                          </span>
                        </SelectItem>
                      ))
                    ) : (
                      <div className="px-2 py-1.5 text-sm text-muted-foreground">No branches match</div>
                    )}
                  </SelectContent>
                </Select>
              </div>
            </div>

            <div className="space-y-2">
              <div className="space-y-2">
                <Label htmlFor="preview-config-name">Preview config</Label>
                {configOptions?.names.length ? (
                  <Select value={previewConfigName || configOptions.selected_name || ""} onValueChange={setPreviewConfigName}>
                    <SelectTrigger id="preview-config-name">
                      <SelectValue placeholder={configOptions.requires_selection ? "Choose config" : "Auto-select default config"} />
                    </SelectTrigger>
                    <SelectContent>
                      {configOptions.names.map((name) => (
                        <SelectItem key={name} value={name}>
                          {name}
                          {name === configOptions.default_name ? " (default)" : ""}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                ) : (
                  <Input
                    id="preview-config-name"
                    value={previewConfigName}
                    onChange={(event) => setPreviewConfigName(event.target.value)}
                    placeholder={configOptionsQuery.isLoading ? "Loading configs..." : "Auto-select default config"}
                  />
                )}
              </div>
            </div>

            <Collapsible open={optionsOpen} onOpenChange={setOptionsOpen}>
              <CollapsibleTrigger asChild>
                <Button type="button" variant="outline" size="sm">
                  <ChevronDown className="h-4 w-4" />
                  Options
                </Button>
              </CollapsibleTrigger>
              <CollapsibleContent className="mt-4 grid gap-4 md:grid-cols-2">
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
                  <Label htmlFor="preview-ttl">Lifetime seconds</Label>
                  <Input
                    id="preview-ttl"
                    inputMode="numeric"
                    value={ttlSeconds}
                    onChange={(event) => setTTLSeconds(event.target.value.replace(/\D/g, ""))}
                    placeholder="Use org default"
                  />
                </div>
              </CollapsibleContent>
            </Collapsible>

            {configOptions?.validation_errors?.length ? (
              <p className="text-sm text-destructive">{configOptions.validation_errors[0]}</p>
            ) : null}

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
