"use client";

import dynamic from "next/dynamic";
import { SessionDetailLoadingSkeleton } from "./session-detail-loading-skeleton";

const SessionDetailContent = dynamic(
  () => import("./session-detail-content").then((module) => module.SessionDetailContent),
  {
    ssr: false,
    loading: () => <SessionDetailLoadingSkeleton />,
  },
);

export function SessionDetailPageClient({ id }: { id: string }) {
  return <SessionDetailContent id={id} />;
}
