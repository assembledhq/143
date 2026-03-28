"use client";

import { cn } from "@/lib/utils";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import type { UserFilterParam } from "@/hooks/use-session-user-filter";

interface SessionOwnerToggleProps {
  currentUserFilter: string;
  onFilterChange: (value: UserFilterParam) => void;
  className?: string;
}

export function SessionOwnerToggle({
  currentUserFilter,
  onFilterChange,
  className,
}: SessionOwnerToggleProps) {
  return (
    <ToggleGroup
      type="single"
      size="sm"
      value={currentUserFilter}
      onValueChange={(value: string) => {
        if (!value) return; // prevent deselecting
        onFilterChange(value === "all" ? null : "mine");
      }}
      className={cn(className)}
    >
      <ToggleGroupItem value="all">Everyone</ToggleGroupItem>
      <ToggleGroupItem value="mine">Mine</ToggleGroupItem>
    </ToggleGroup>
  );
}
