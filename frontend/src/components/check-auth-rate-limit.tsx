"use client";

import { useIsMutating, useMutation, useQueryClient } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger } from "@/components/ui/alert-dialog";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import { notify } from "@/lib/notify";
import { queryKeys } from "@/lib/query-keys";
import type { CodingCredentialSummary } from "@/lib/types";

export function CheckAuthRateLimit({ row }: { row: CodingCredentialSummary }) {
  const queryClient = useQueryClient();
  const retry = row.can_retry_rate_limit === true;
  const pending = useIsMutating({ mutationKey: ["check-auth-rate-limit", row.id] }) > 0;
  const check = useMutation({
    mutationKey: ["check-auth-rate-limit", row.id],
    mutationFn: () => retry
      ? api.codingCredentials.retryRateLimit(row.id, row.scope)
      : api.codingCredentials.checkRateLimit(row.id, row.scope),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeys.codingCredentials.all });
      if (retry) {
        notify.success(`${row.label} is ready to retry`, {
          description: "Saved cooldown cleared. If Claude is still rate limited, the next session will record a new cooldown.",
        });
      } else {
        notify.success(`Rate-limit status updated for ${row.label}`);
      }
    },
    onError: (error: Error) => {
      captureError(error, { feature: "check-auth-rate-limit" });
      notify.error(retry ? "Could not retry auth" : "Could not check rate limit", {
        description: error.message || "Please try again. The saved rate limit has not changed.",
      });
    },
  });

  if (row.status !== "rate_limited" || row.auth_type !== "subscription") return null;

  if (retry) return (
    <AlertDialog>
      <DisabledTooltip disabled={pending} content="Wait for the retry request to finish.">
        <AlertDialogTrigger asChild>
          <Button type="button" variant="ghost" size="sm" className="px-0 text-xs" aria-label={`Retry auth for ${row.label}`} disabled={pending}>
            {pending ? "Retrying…" : "Retry auth"}
          </Button>
        </AlertDialogTrigger>
      </DisabledTooltip>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Retry {row.label}?</AlertDialogTitle>
          <AlertDialogDescription>
            Claude setup tokens cannot read usage. This clears the saved cooldown so the next session can try this auth again. If Claude is still rate limited, the session will record a new cooldown. This does not reset your Claude usage.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction disabled={pending} onClick={() => check.mutate()}>Retry auth</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );

  return (
    <DisabledTooltip disabled={pending} content="Wait for the rate-limit check to finish.">
      <Button
        type="button"
        variant="ghost"
        size="sm"
        className="px-0 text-xs"
        aria-label={`Check rate limit for ${row.label}`}
        disabled={pending}
        onClick={() => check.mutate()}
      >
        {pending ? "Checking…" : "Check rate limit"}
      </Button>
    </DisabledTooltip>
  );
}
