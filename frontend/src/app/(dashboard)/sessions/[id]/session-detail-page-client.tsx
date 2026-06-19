"use client";

import dynamic from "next/dynamic";
import { SessionDetailLoadingSkeleton } from "./session-detail-loading-skeleton";

const loadSessionDetailContent = () => import("./session-detail-content");

// router.prefetch() only fetches the route's RSC payload and the small page
// wrapper chunk — the ssr:false chunk below starts downloading (prod) or
// compiling (dev) only when the page renders, so the first session open
// stalls on it. List views call this from hover/idle handlers to warm the
// chunk before the click; the bundler caches the promise, so repeat calls
// and the dynamic() loader all share one fetch.
export function preloadSessionDetailContent() {
  void loadSessionDetailContent();
}

const SessionDetailContent = dynamic(
  () => loadSessionDetailContent().then((module) => module.SessionDetailContent),
  {
    ssr: false,
    loading: () => <SessionDetailLoadingSkeleton />,
  },
);

export function SessionDetailPageClient({ id }: { id: string }) {
  return <SessionDetailContent id={id} />;
}
