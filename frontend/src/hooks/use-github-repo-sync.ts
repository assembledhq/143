import { useEffect, useRef } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";

export function useGitHubRepoSync() {
  const queryClient = useQueryClient();
  const autoSyncTriggered = useRef(false);

  const mutation = useMutation({
    mutationFn: () => api.integrations.syncGitHub(),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.repositories.all });
      queryClient.invalidateQueries({ queryKey: ["integrations"] });
    },
  });

  const autoSyncIfNeeded = (hasGitHub: boolean, hasRepos: boolean) => {
    if (hasGitHub && !hasRepos && !autoSyncTriggered.current && !mutation.isPending) {
      autoSyncTriggered.current = true;
      mutation.mutate();
    }
  };

  // Reset the ref if the component remounts
  useEffect(() => {
    return () => {
      autoSyncTriggered.current = false;
    };
  }, []);

  return {
    sync: () => mutation.mutate(),
    isSyncing: mutation.isPending,
    syncResult: mutation.data?.data,
    syncError: mutation.error,
    autoSyncIfNeeded,
  };
}
