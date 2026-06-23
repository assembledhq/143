"use client";

import { use } from "react";
import { useQuery } from "@tanstack/react-query";
import { GitBranch } from "lucide-react";

import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { OpenPreviewButton } from "@/components/preview/open-preview-button";
import { PreviewStatusBadge } from "@/components/preview/preview-status-badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { ErrorText } from "@/components/ui/error-notice";
import { api } from "@/lib/api";
import type { BranchPreviewResponse, SingleResponse } from "@/lib/types";
import { safeExternalUrl } from "@/lib/utils";

export default function PreviewStableLinkPage({
  params,
}: {
  params: Promise<{ link_type: "target" | "pull_request"; slug: string[] }>;
}) {
  const { link_type: linkType, slug } = use(params);
  const stableSlug = slug.join("/");
  const previewQuery = useQuery<SingleResponse<BranchPreviewResponse>>({
    queryKey: ["branch-preview-link", linkType, stableSlug],
    queryFn: () => api.previews.resolveLink(linkType, stableSlug),
  });
  const preview = previewQuery.data?.data;

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title={preview?.repository_full_name ?? "Preview link"}
          description={preview?.branch ?? stableSlug}
          action={preview ? <PreviewStatusBadge status={preview.status} variant={preview.status === "ready" ? "default" : "secondary"} /> : undefined}
        />
        <Card>
          <CardContent className="space-y-4 pt-6">
            {previewQuery.isLoading ? (
              <p className="text-sm text-muted-foreground">Loading preview link...</p>
            ) : previewQuery.isError ? (
              <ErrorText className="text-sm">
                {previewQuery.error instanceof Error ? previewQuery.error.message : "Preview link could not be loaded."}
              </ErrorText>
            ) : preview ? (
              <>
                <div className="grid gap-3 text-sm md:grid-cols-2">
                  <div>
                    <p className="text-muted-foreground">Commit</p>
                    <p className="break-all font-medium text-foreground">{preview.commit_sha?.slice(0, 12) ?? "Unknown"}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">Phase</p>
                    <p className="font-medium capitalize text-foreground">{preview.current_phase?.replaceAll("_", " ") ?? preview.status}</p>
                  </div>
                </div>
                <div className="flex flex-col gap-2 sm:flex-row">
                  {safeExternalUrl(preview.preview_url) && preview.preview_id ? (
                    <OpenPreviewButton previewId={preview.preview_id} previewUrl={preview.preview_url} />
                  ) : null}
                  <Button asChild variant="outline">
                    <a href={`/previews/${preview.preview_id ?? preview.target_id}`}>
                      <GitBranch className="h-4 w-4" />
                      Details
                    </a>
                  </Button>
                </div>
              </>
            ) : null}
          </CardContent>
        </Card>
      </div>
    </PageContainer>
  );
}
