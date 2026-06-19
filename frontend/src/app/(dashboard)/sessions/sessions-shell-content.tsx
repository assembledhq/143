"use client";

import { MessageSquareText } from "lucide-react";
import { EmptyState } from "@/components/empty-state";
import { ManualSessionCreatePageContent } from "./new/manual-session-create-page-content";
import { SessionDetailPageClient } from "./[id]/session-detail-page-client";
import type { SessionsRouteState } from "./sessions-route-state";

export function SessionsShellContent({ routeState }: { routeState: SessionsRouteState }) {
  if (routeState.isUnsupportedRoute) {
    return (
      <div className="flex min-h-[calc(100vh-8rem)] items-center justify-center px-4 py-10">
        <EmptyState
          variant="inline"
          icon={MessageSquareText}
          title="Unsupported sessions route"
          description="Return to sessions and choose a supported session view."
          action={{ label: "Sessions", href: "/sessions" }}
        />
      </div>
    );
  }

  if (routeState.isCreatingSession) {
    return <ManualSessionCreatePageContent />;
  }

  if (routeState.selectedSessionId) {
    return (
      <SessionDetailPageClient
        key={routeState.selectedSessionId}
        id={routeState.selectedSessionId}
      />
    );
  }

  return (
    <div className="flex min-h-[calc(100vh-8rem)] items-center justify-center px-4 py-10">
      <EmptyState
        variant="inline"
        icon={MessageSquareText}
        title="Select a session"
        description="Choose a session from the sidebar to review its transcript, changes, preview, and publish state."
        action={{ label: "New session", href: "/sessions/new" }}
      />
    </div>
  );
}
