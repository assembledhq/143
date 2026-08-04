"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useAuth } from "@/hooks/use-auth";
import { api } from "@/lib/api";
import { notify as toast } from "@/lib/notify";
import type { SessionActivityDetail, SingleResponse, User } from "@/lib/types";
import { recordSessionActivityEvent } from "@/lib/session-activity-events";

export function useSessionActivityDetail() {
  const { user } = useAuth();
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: (detail: SessionActivityDetail) => api.auth.updateSettings({ session_activity_detail: detail }),
    onMutate: async (detail) => {
      await queryClient.cancelQueries({ queryKey: ["auth", "me"] });
      const previous = queryClient.getQueryData<SingleResponse<User>>(["auth", "me"]);
      queryClient.setQueryData<SingleResponse<User>>(["auth", "me"], (current) => current ? ({
        ...current,
        data: { ...current.data, settings: { ...current.data.settings, session_activity_detail: detail } },
      }) : current);
      return { previous };
    },
    onSuccess: (response) => {
      queryClient.setQueryData(["auth", "me"], { data: response.data });
      recordSessionActivityEvent({ event: "preference_changed", detail: response.data.settings?.session_activity_detail ?? "compact", trigger: "preference" });
    },
    onError: (_error, _detail, context) => {
      if (context?.previous) queryClient.setQueryData(["auth", "me"], context.previous);
      toast.error("Could not save activity detail preference");
    },
  });
  return {
    detail: (user?.settings?.session_activity_detail ?? "compact") as SessionActivityDetail,
    setDetail: mutation.mutate,
    mutation,
  };
}
