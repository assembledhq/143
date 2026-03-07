"use client";

import { use } from "react";
import { PageContainer } from "@/components/page-container";
import { ProjectDetailContent } from "./project-detail-content";

export default function ProjectDetailPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  return (
    <PageContainer size="wide">
      <ProjectDetailContent id={id} />
    </PageContainer>
  );
}
