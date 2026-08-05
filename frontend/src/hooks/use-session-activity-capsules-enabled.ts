"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";

export const SESSION_ACTIVITY_CONFIG_FRESHNESS_MS = 30_000;

export function useSessionActivityCapsulesEnabled() {
  const query = useQuery({
    queryKey: ["application-config"],
    queryFn: () => api.applicationConfig.get(),
    staleTime: SESSION_ACTIVITY_CONFIG_FRESHNESS_MS,
    refetchInterval: SESSION_ACTIVITY_CONFIG_FRESHNESS_MS,
    refetchOnWindowFocus: "always",
    retry: false,
  });

  return {
    enabled: query.data?.data.session_activity_capsules_enabled === true,
    query,
  };
}
