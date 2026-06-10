"use client";

import { useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import {
  applyOrgSettingsPatch,
  coalesceSettingsPatch,
  type SettingsPatch,
} from "@/lib/settings-autosave";
import type { MembershipsResponse, SingleResponse } from "@/lib/types";
import { useAutosave } from "@/hooks/useAutosave";

export function useOrgSettingsAutosave() {
  const queryClient = useQueryClient();
  return useAutosave<SettingsPatch>({
    queryKey: queryKeys.settings.all,
    mutationFn: async (payload) => {
      const response = await api.settings.update(payload);
      queryClient.setQueryData(queryKeys.settings.all, response);
      if (payload.name !== undefined) {
        queryClient.setQueryData<SingleResponse<MembershipsResponse> | undefined>(
          queryKeys.auth.memberships,
          (previous) => {
            if (!previous?.data) return previous;
            return {
              ...previous,
              data: {
                ...previous.data,
                memberships: previous.data.memberships.map((membership) =>
                  membership.org_id === response.data.id
                    ? { ...membership, org_name: response.data.name }
                    : membership,
                ),
              },
            };
          },
        );
      }
      void queryClient.invalidateQueries({ queryKey: ["audit-logs", "latest"] });
      return response;
    },
    applyOptimistic: applyOrgSettingsPatch,
    coalesce: coalesceSettingsPatch,
    invalidateOnSettled: false,
  });
}
