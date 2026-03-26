"use client";

import { cn } from "@/lib/utils";
import type { UserFilterParam } from "@/hooks/use-session-user-filter";

interface SessionOwnerToggleProps {
  currentUserFilter: string;
  onFilterChange: (value: UserFilterParam) => void;
  className?: string;
}

const options = [
  { label: "Everyone", value: "all" },
  { label: "Mine", value: "mine" },
] as const;

export function SessionOwnerToggle({
  currentUserFilter,
  onFilterChange,
  className,
}: SessionOwnerToggleProps) {
  return (
    <div
      className={cn(
        "inline-flex items-center rounded-md bg-muted/60 p-0.5",
        className,
      )}
    >
      {options.map((opt) => {
        const isActive = currentUserFilter === opt.value;
        return (
          <button
            key={opt.value}
            onClick={() => onFilterChange(opt.value === "all" ? null : "mine")}
            className={cn(
              "px-2.5 py-1 text-[11px] font-medium rounded-[5px] transition-all duration-150",
              isActive
                ? "bg-background text-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground/80",
            )}
          >
            {opt.label}
          </button>
        );
      })}
    </div>
  );
}
