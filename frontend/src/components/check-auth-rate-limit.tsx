"use client";

import { useIsMutating, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import { queryKeys } from "@/lib/query-keys";
import type { CodingCredentialSummary } from "@/lib/types";

export function CheckAuthRateLimit({ row }: { row: CodingCredentialSummary }) {
  const queryClient = useQueryClient();
  const pending = useIsMutating({ mutationKey: ["check-auth-rate-limit", row.id] }) > 0;
  const check = useMutation({
    mutationKey: ["check-auth-rate-limit", row.id],
    mutationFn: () => api.codingCredentials.checkRateLimit(row.id, row.scope),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeys.codingCredentials.all });
      toast.success(`Rate-limit status updated for ${row.label}`);
    },
    onError: (error: Error) => {
      captureError(error, { feature: "check-auth-rate-limit" });
      toast.error(error.message || "Could not check rate limit");
    },
  });

  if (row.status !== "rate_limited" || row.auth_type !== "subscription") return null;

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
