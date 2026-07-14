"use client";

import type { QueryKey } from "@tanstack/react-query";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { registerLiveQuery, type LiveQueryFamily, type LiveQueryPriority } from "@/lib/live-events";

export interface LiveQueryRegistrationOptions {
  queryKey: QueryKey;
  families: readonly LiveQueryFamily[];
  resourceId?: string;
  priority?: LiveQueryPriority;
  visible: boolean;
  resourceStreamOwnsDetail?: boolean;
}

export function useLiveQueryRegistration(options: LiveQueryRegistrationOptions): void {
  const queryClient = useQueryClient();
  const key = JSON.stringify(options.queryKey);
  const families = options.families.join("\u0000");

  useEffect(
    () => registerLiveQuery(queryClient, options),
    // queryKey and families are represented by stable serialized values.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [queryClient, key, families, options.resourceId, options.priority, options.visible, options.resourceStreamOwnsDetail],
  );
}
