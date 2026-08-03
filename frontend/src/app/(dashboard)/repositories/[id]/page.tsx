"use client";

import { use } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { MonitorPlay } from "lucide-react";
import { api } from "@/lib/api";
import { notify as toast } from "@/lib/notify";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { RadioCard } from "@/components/radio-card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { RadioGroup } from "@/components/ui/radio-group";
import { useAuth } from "@/hooks/use-auth";
import { usePageTitle } from "@/hooks/use-page-title";
import type { PRHandoffMode, Repository, SingleResponse } from "@/lib/types";

export default function RepositoryDetailPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <RepositoryDetailContent id={id} />;
}

export function RepositoryDetailContent({ id }: { id: string }) {
  const { user } = useAuth();
  const queryClient = useQueryClient();
  const { data, isLoading } = useQuery<SingleResponse<Repository>>({
    queryKey: ["repository", id],
    queryFn: () => api.repositories.get(id),
  });

  const repo = data?.data;
  usePageTitle(repo?.full_name, "Repository");
  const handoffMode = repo?.settings?.pr_handoff_mode === "draft_first" ? "draft_first" : "pre_publish";
  const canEdit = user?.role === "admin" || user?.role === "member";
  const updateHandoffMode = useMutation({
    mutationFn: (mode: PRHandoffMode) => api.repositories.update(id, { settings: { pr_handoff_mode: mode } }),
    onSuccess: (response) => {
      queryClient.setQueryData(["repository", id], response);
      toast.success("PR handoff mode saved");
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : "Could not save PR handoff mode"),
  });

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
          description="Repository details, PR handoff, and preview configuration."
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
        <Card>
          <CardHeader>
            <CardTitle>PR handoff</CardTitle>
            <CardDescription>
              Choose when 143 opens the pull request for this repository. Repository policy is authoritative because some checks only run after a PR exists.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <RadioGroup
              value={handoffMode}
              onValueChange={(value) => updateHandoffMode.mutate(value as PRHandoffMode)}
              className="grid gap-3"
              disabled={!canEdit || updateHandoffMode.isPending}
            >
              <RadioCard
                value="pre_publish"
                label="Review before opening the PR"
                description="Run review and fixes against the unpublished changes, then create the PR only after the gate passes."
                selected={handoffMode === "pre_publish"}
                disabled={!canEdit || updateHandoffMode.isPending}
              />
              <RadioCard
                value="draft_first"
                label="Create a draft first"
                description="Open one draft so PR-only CI, previews, scans, and policy bots can run. 143 marks it ready only after review passes and the reviewed head is published."
                selected={handoffMode === "draft_first"}
                disabled={!canEdit || updateHandoffMode.isPending}
              />
            </RadioGroup>
            {!canEdit ? <p className="mt-3 text-xs text-muted-foreground">An organization admin or member can change this setting.</p> : null}
          </CardContent>
        </Card>
      </div>
    </PageContainer>
  );
}
