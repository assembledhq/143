"use client";

import { use } from "react";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { MonitorPlay } from "lucide-react";
import { api } from "@/lib/api";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { RepoPMSettingsEditor } from "@/components/repo-pm-settings";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { usePageTitle } from "@/hooks/use-page-title";
import type { Repository, SingleResponse } from "@/lib/types";

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
      </div>
    </PageContainer>
  );
}
