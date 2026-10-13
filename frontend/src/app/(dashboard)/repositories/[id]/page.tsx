"use client";

import { use } from "react";
import { RepositoryDetailContent } from "./repository-detail-content";

export default function RepositoryDetailPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <RepositoryDetailContent id={id} />;
}
