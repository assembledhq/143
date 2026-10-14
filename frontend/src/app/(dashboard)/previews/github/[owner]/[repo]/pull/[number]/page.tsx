"use client";

import { use } from "react";
import { PullRequestPreviewContent } from "./pull-request-preview-content";

export default function PullRequestPreviewPage({
  params,
}: {
  params: Promise<{ owner: string; repo: string; number: string }>;
}) {
  const { owner, repo, number } = use(params);
  return <PullRequestPreviewContent owner={owner} repo={repo} number={number} />;
}
