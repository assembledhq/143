"use client";

import { OwnerScopeToggle } from "@/components/owner-scope-toggle";
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
    <OwnerScopeToggle
      currentUserFilter={currentUserFilter}
      onFilterChange={onFilterChange}
      className={className}
    />
  );
}
