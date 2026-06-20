"use client";

import { MessageSquareText } from "lucide-react";
import { EmptyState } from "@/components/empty-state";
import { ManualSessionCreatePageContent } from "./new/manual-session-create-page-content";
import { SessionDetailPageClient } from "./[id]/session-detail-page-client";
import type { SessionsRouteState } from "./sessions-route-state";

// When nothing is selected we don't show a "Select a session" placeholder; we
// default to the "Let's build" create-session experience so the main panel is
// always actionable.

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

  if (routeState.selectedSessionId) {
    return (
      <SessionDetailPageClient
        key={routeState.selectedSessionId}
        id={routeState.selectedSessionId}
      />
    );
  }

  // Both the explicit create route (/sessions/new) and the bare index
  // (/sessions, nothing selected) fall through to the "Let's build" composer.
  return <ManualSessionCreatePageContent />;
}
