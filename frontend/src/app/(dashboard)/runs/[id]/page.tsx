"use client";

import { use } from "react";
import { RunDetailContent } from "./run-detail-content";

export default function RunDetailPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  return <RunDetailContent id={id} />;
}
