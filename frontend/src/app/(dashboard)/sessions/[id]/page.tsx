"use client";

import { use } from "react";
import { PageContainer } from "@/components/page-container";
import { SessionDetailContent } from "./session-detail-content";

export default function SessionDetailPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  return (
    <PageContainer size="wide">
      <SessionDetailContent id={id} />
    </PageContainer>
  );
}
