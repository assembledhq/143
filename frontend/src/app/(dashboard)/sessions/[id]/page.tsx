import { SessionDetailPageClient } from "./session-detail-page-client";

export default async function SessionDetailPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  return <SessionDetailPageClient id={id} />;
}
