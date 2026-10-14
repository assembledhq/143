"use client";

import { use } from "react";
import { PreviewLandingContent } from "./preview-landing-content";

export default function PreviewLandingPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <PreviewLandingContent id={id} />;
}
