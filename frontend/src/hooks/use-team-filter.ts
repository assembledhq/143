"use client";

import { useQuery } from "@tanstack/react-query";
import { useQueryState, parseAsString } from "nuqs";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";

export function useTeamFilter() {
  const [teamParam, setTeamParam] = useQueryState("team", parseAsString);

  const { data: teamsData } = useQuery({
    queryKey: queryKeys.teams.mine,
    queryFn: () => api.teams.listMine(),
    staleTime: 60_000,
  });

  const myTeams = teamsData?.data ?? [];

  const selectedTeamId = teamParam ?? undefined;

  return { selectedTeamId, myTeams, setTeamFilter: setTeamParam };
}
