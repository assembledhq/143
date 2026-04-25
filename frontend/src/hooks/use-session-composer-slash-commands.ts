"use client";

import { useQuery } from "@tanstack/react-query";

import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import type { SlashCommandListResponse } from "@/lib/types";

type Options = {
  agentType: string;
  query: string;
  repositoryId?: string;
  branch?: string;
  enabled?: boolean;
};

// useSessionComposerSlashCommands queries the slash-command catalog for the
// currently selected agent. Re-runs on agentType change so switching agents
// mid-compose immediately refilters the picker.
export function useSessionComposerSlashCommands({ agentType, query, repositoryId, branch, enabled = true }: Options) {
  return useQuery<SlashCommandListResponse>({
    queryKey: queryKeys.sessionComposer.slashCommands(agentType, repositoryId ?? "", branch ?? "", query),
    queryFn: () =>
      api.sessionComposer.slashCommands({
        agentType,
        query,
        repositoryId,
        branch,
      }),
    enabled: enabled && !!agentType,
    staleTime: 30 * 1000,
  });
}
