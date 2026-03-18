"use client";

import { use } from "react";
import { ProjectDetailContent } from "./project-detail-content";

export default function ProjectDetailPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  return <ProjectDetailContent id={id} />;
}
