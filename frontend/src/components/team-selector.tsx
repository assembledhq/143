"use client";

import { Users } from "lucide-react";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { Team } from "@/lib/types";

interface TeamSelectorProps {
  teams: Team[];
  selectedTeamId: string | undefined;
  onSelect: (teamId: string | null) => void;
  className?: string;
}

export function TeamSelector({
  teams,
  selectedTeamId,
  onSelect,
  className,
}: TeamSelectorProps) {
  if (teams.length === 0) return null;

  return (
    <Select
      value={selectedTeamId ?? "__all__"}
      onValueChange={(v) => onSelect(v === "__all__" ? null : v)}
    >
      <SelectTrigger className={className} size="sm">
        <div className="flex items-center gap-1.5 text-xs">
          <Users className="h-3.5 w-3.5 text-muted-foreground" />
          <SelectValue placeholder="All teams" />
        </div>
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="__all__">All teams</SelectItem>
        {teams.map((team) => (
          <SelectItem key={team.id} value={team.id}>
            {team.name}
            {team.member_count > 0 && (
              <span className="text-muted-foreground ml-1">({team.member_count})</span>
            )}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
