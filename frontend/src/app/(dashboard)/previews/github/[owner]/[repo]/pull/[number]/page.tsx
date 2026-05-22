"use client";

import { use } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ExternalLink, GitBranch, GitPullRequest, RotateCw } from "lucide-react";

import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { BranchPreviewResponse, SingleResponse } from "@/lib/types";

export default function PullRequestPreviewPage({
  params,
}: {
  params: Promise<{ owner: string; repo: string; number: string }>;
}) {
  const { owner, repo, number } = use(params);
  const queryClient = useQueryClient();
  const queryKey = ["branch-preview-pr", owner, repo, number];
  const previewQuery = useQuery<SingleResponse<BranchPreviewResponse>>({
    queryKey,
    queryFn: () => api.previews.getPullRequest(owner, repo, number),
    refetchInterval: (query) => {
      const status = query.state.data?.data.status;
      return status === "starting" ? 3000 : false;
    },
  });
  const startLatest = useMutation({
    mutationFn: (id: string) => api.previews.startLatest(id),
    onSuccess: (response) => {
      queryClient.setQueryData(queryKey, response);
    },
  });
  const restart = useMutation({
    mutationFn: (id: string) => api.previews.restart(id),
    onSuccess: (response) => {
      queryClient.setQueryData(queryKey, response);
    },
  });

  const preview = previewQuery.data?.data;
  const title = `${owner}/${repo}#${number}`;
  const status = preview?.status.replaceAll("_", " ") ?? "Loading";
  const canStartLatest = preview?.target_id || preview?.preview_id;

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title={title}
          description="Pull request preview"
          action={<Badge variant={preview?.status === "ready" ? "default" : "secondary"}>{status}</Badge>}
        />

        <Card>
          <CardContent className="space-y-5 pt-6">
            {previewQuery.isLoading ? (
              <p className="text-sm text-muted-foreground">Loading PR preview...</p>
            ) : previewQuery.isError ? (
              <p className="text-sm text-destructive">
                {previewQuery.error instanceof Error ? previewQuery.error.message : "Preview could not be loaded."}
              </p>
            ) : preview ? (
              <>
                {preview.new_commits_available ? (
                  <div className="flex items-start gap-3 rounded-md border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-900/50 dark:bg-amber-950/30 dark:text-amber-200">
                    <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
                    <div className="min-w-0">
                      <p className="font-medium">New commits available</p>
                      <p className="break-all text-amber-800 dark:text-amber-300">
                        Latest head: {preview.latest_commit_sha?.slice(0, 12) ?? "unknown"}
                      </p>
                    </div>
                  </div>
                ) : null}

                <div className="grid gap-3 text-sm md:grid-cols-2">
                  <div>
                    <p className="text-muted-foreground">Repository</p>
                    <p className="font-medium text-foreground">{preview.repository_full_name ?? `${owner}/${repo}`}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">Branch</p>
                    <p className="break-all font-medium text-foreground">{preview.branch ?? "Unknown"}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">Commit</p>
                    <p className="break-all font-medium text-foreground">{preview.commit_sha?.slice(0, 12) ?? "Unknown"}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">Phase</p>
                    <p className="font-medium text-foreground">{preview.current_phase?.replaceAll("_", " ") ?? status}</p>
                  </div>
                </div>

                {preview.error ? (
                  <p className="rounded-md border border-destructive/20 bg-destructive/5 p-3 text-sm text-destructive">{preview.error}</p>
                ) : null}

                <div className="flex flex-col gap-2 sm:flex-row">
                  {preview.preview_url ? (
                    <Button asChild>
                      <a href={preview.preview_url}>
                        <ExternalLink className="h-4 w-4" />
                        Open preview
                      </a>
                    </Button>
                  ) : null}
                  {preview.pull_request_url ? (
                    <Button asChild variant="outline">
                      <a href={preview.pull_request_url}>
                        <GitPullRequest className="h-4 w-4" />
                        Open PR
                      </a>
                    </Button>
                  ) : null}
                  {preview.github_branch_url ? (
                    <Button asChild variant="outline">
                      <a href={preview.github_branch_url}>
                        <GitBranch className="h-4 w-4" />
                        Branch
                      </a>
                    </Button>
                  ) : null}
                  {canStartLatest ? (
                    <Button
                      type="button"
                      variant="outline"
                      onClick={() => startLatest.mutate(preview.target_id)}
                      disabled={startLatest.isPending}
                    >
                      <GitBranch className="h-4 w-4" />
                      Start latest
                    </Button>
                  ) : null}
                  {preview.preview_id ? (
                    <Button
                      type="button"
                      variant="outline"
                      onClick={() => restart.mutate(preview.preview_id!)}
                      disabled={restart.isPending}
                    >
                      <RotateCw className="h-4 w-4" />
                      Retry
                    </Button>
                  ) : null}
                </div>
              </>
            ) : null}
          </CardContent>
        </Card>
      </div>
    </PageContainer>
  );
}
