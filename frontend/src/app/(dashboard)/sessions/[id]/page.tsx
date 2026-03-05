"use client";

import { use } from "react";
import { SessionDetailContent } from "./session-detail-content";

export default function SessionDetailPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  return <SessionDetailContent id={id} />;
}
