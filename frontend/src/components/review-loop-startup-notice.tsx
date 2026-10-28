"use client";

import { StatusLabel } from "@/components/status-label";
import { Card, CardContent } from "@/components/ui/card";
import type { ReviewLoopAdmissionState } from "@/lib/types";

export function ReviewLoopStartupNotice({ state }: { state?: ReviewLoopAdmissionState | null }) {
  const title = state === "accepted"
    ? "Review startup accepted"
    : state === "attention"
    ? "Review startup needs attention"
    : state === "refused"
      ? "Review startup was refused"
      : state === "cancelled"
        ? "Review startup cancelled"
        : state === "unknown" || state === "attempting"
          ? "Review created; confirming startup"
          : state === "pending"
            ? "Review created; waiting to start"
            : "Review creation is awaiting confirmation";
  const detail = state === "accepted"
    ? "The review has been created. Waiting for confirmed review activity."
    : state === "attention"
    ? "Startup remains unconfirmed. Periodic checks continue for the original review."
    : state === "refused" || state === "cancelled"
      ? "The original review command will not be retried."
      : "Check the original review's status here. Starting another review could duplicate the request.";
  return (
    <Card>
      <CardContent className="space-y-2 py-3">
        <StatusLabel label={title} tone={state === "attention" || state === "refused" ? "attention" : "info"} announcement="polite" />
        <p className="text-xs text-muted-foreground">{detail}</p>
      </CardContent>
    </Card>
  );
}
