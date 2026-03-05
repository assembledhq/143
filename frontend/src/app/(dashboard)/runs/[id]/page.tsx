"use client";

import { use } from "react";
import { PageContainer } from "@/components/page-container";
import { RunDetailContent } from "./run-detail-content";

export default function RunDetailPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  return (
    <PageContainer size="wide">
      <RunDetailContent id={id} />
    </PageContainer>
  );
}
