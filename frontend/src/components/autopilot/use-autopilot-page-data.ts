"use client";

import { useMemo } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { deriveAutopilotViewModel, DEFAULT_PRIORITY_WEIGHTS } from "./autopilot-helpers";
import { useSetupStatus } from "@/hooks/use-setup-status";
import type { OrgSettings, PMDocument, PMPlan, PMStatus, Organization, ListResponse, ProductContext, SingleResponse } from "@/lib/types";

const DEFAULT_SETTINGS: OrgSettings = {
  autonomy_level: "auto_simple",
  product_direction: "",
  product_context: {
    philosophy: "",
    direction: "",
    focus_areas: [],
    avoid_areas: [],
  },
  priority_weights: DEFAULT_PRIORITY_WEIGHTS,
};

const DEFAULT_PM_STATUS: PMStatus = {
  is_running: false,
  issues_reviewed: 0,
  success_rate: 0,
  success_count: 0,
  total_delegated: 0,
};

export function useAutopilotPageData(queueParams?: { source?: string | null; run_state?: string | null; automation?: string | null; sort?: string | null; q?: string | null }) {
  const { isLoading: setupLoading, isSetupComplete } = useSetupStatus();

  const { data: settingsResponse, isLoading: settingsLoading } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });

  const { data: pmStatusResponse, isLoading: statusLoading } = useQuery<SingleResponse<PMStatus>>({
    queryKey: queryKeys.pm.status,
    queryFn: () => api.pm.status(),
  });

  const { data: latestPlan, isLoading: latestPlanLoading } = useQuery<PMPlan | null>({
    queryKey: queryKeys.pm.latest,
    queryFn: async () => (await api.pm.latest()).data,
  });

  const { data: documentsResponse, isLoading: documentsLoading } = useQuery<ListResponse<PMDocument>>({
    queryKey: queryKeys.pm.documents,
    queryFn: () => api.pm.listDocuments(),
  });

  const {
    data: queuePages,
    isLoading: queueLoading,
    hasNextPage: hasNextQueuePage,
    fetchNextPage: fetchNextQueuePage,
    isFetchingNextPage: isFetchingNextQueuePage,
  } = useInfiniteQuery({
    queryKey: queryKeys.autopilot.queue(queueParams ?? {}),
    queryFn: ({ pageParam }) => api.autopilot.queue({ ...queueParams, cursor: pageParam, limit: 50 }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.meta.next_cursor || undefined,
    enabled: isSetupComplete,
  });

  const queue = useMemo(() => {
    if (!queuePages) return undefined;
    const lastPage = queuePages.pages[queuePages.pages.length - 1];
    return {
      data: queuePages.pages.flatMap((page) => page.data),
      meta: lastPage?.meta ?? queuePages.pages[0]?.meta,
    };
  }, [queuePages]);

  const rawSettings = settingsResponse?.data?.settings;
  const mergedSettings = useMemo<OrgSettings>(() => {
    const settings = (rawSettings ?? {}) as OrgSettings;
    const rawContext: Partial<ProductContext> = settings.product_context ?? {};
    const productContext: ProductContext = {
      philosophy: rawContext.philosophy ?? DEFAULT_SETTINGS.product_context?.philosophy ?? "",
      direction: rawContext.direction ?? DEFAULT_SETTINGS.product_context?.direction ?? "",
      focus_areas: rawContext.focus_areas ?? DEFAULT_SETTINGS.product_context?.focus_areas ?? [],
      avoid_areas: rawContext.avoid_areas ?? DEFAULT_SETTINGS.product_context?.avoid_areas ?? [],
    };

    return {
      ...DEFAULT_SETTINGS,
      ...settings,
      product_context: productContext,
      priority_weights: {
        ...DEFAULT_SETTINGS.priority_weights,
        ...(settings.priority_weights ?? {}),
      },
    };
  }, [rawSettings]);

  const pmStatus = pmStatusResponse?.data ?? DEFAULT_PM_STATUS;
  const documents = useMemo(() => documentsResponse?.data ?? [], [documentsResponse?.data]);

  const viewModel = useMemo(() => deriveAutopilotViewModel({
    settings: mergedSettings,
    pmStatus,
    latestPlan: latestPlan ?? null,
    documents,
  }), [documents, latestPlan, mergedSettings, pmStatus]);

  return {
    isLoading: setupLoading || settingsLoading || statusLoading || latestPlanLoading || documentsLoading,
    isSetupComplete,
    settings: mergedSettings,
    pmStatus,
    viewModel,
    queue,
    queueLoading,
    hasNextQueuePage,
    fetchNextQueuePage,
    isFetchingNextQueuePage,
  };
}
