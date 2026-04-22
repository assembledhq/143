"use client";

import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import type { OwnerScopeParam } from "@/hooks/use-owner-scope-filter";

interface OwnerScopeToggleProps {
  currentUserFilter: string;
  onFilterChange: (value: OwnerScopeParam) => void;
  className?: string;
}

export function OwnerScopeToggle({
  currentUserFilter,
  onFilterChange,
  className,
}: OwnerScopeToggleProps) {
  return (
    <ToggleGroup
      type="single"
      size="sm"
      value={currentUserFilter}
      onValueChange={(value: string) => {
        if (!value) return;
        onFilterChange(value === "mine" ? null : "all");
      }}
      className={className}
    >
      <ToggleGroupItem value="all">Everyone</ToggleGroupItem>
      <ToggleGroupItem value="mine">Mine</ToggleGroupItem>
    </ToggleGroup>
  );
}
