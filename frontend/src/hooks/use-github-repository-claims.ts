"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";

export interface GitHubRepositoryClaimVariables {
  githubId: number;
  allowTransfer: boolean;
}

export function useGitHubRepositoryClaims({
  installationId,
  enabled,
  onClaimSuccess,
}: {
  installationId?: number;
  enabled: boolean;
  onClaimSuccess?: () => void;
}) {
  const queryClient = useQueryClient();
  const candidatesQuery = useQuery({
    queryKey: queryKeys.integrations.githubRepositories(installationId),
    queryFn: () => api.integrations.listGitHubRepositories(installationId),
    enabled: enabled && !!installationId,
  });
  const claimMutation = useMutation({
    mutationFn: ({ githubId, allowTransfer }: GitHubRepositoryClaimVariables) =>
      api.integrations.claimGitHubRepositories(installationId ?? 0, [githubId], allowTransfer),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.integrations.all });
      void queryClient.invalidateQueries({ queryKey: queryKeys.repositories.all });
      void queryClient.invalidateQueries({ queryKey: queryKeys.codeReviews.githubTriggers });
      onClaimSuccess?.();
    },
  });

  return { candidatesQuery, claimMutation };
}
