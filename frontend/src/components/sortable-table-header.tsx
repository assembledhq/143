"use client";

import { ArrowDown, ArrowUp, ArrowUpDown } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export type SortDirection = "asc" | "desc";

// Tables that own a default order (the reviews list sorts newest-first until a
// column is picked) pass allowUnsorted so the cycle can return to it. Adapters
// over a table library that has no unsorted state leave it off.
export function nextSortDirection(direction: SortDirection | false, allowUnsorted = false): SortDirection | false {
  if (direction === "asc") return "desc";
  if (direction === "desc") return allowUnsorted ? false : "asc";
  return "asc";
}

export function sortDirectionAriaValue(direction: SortDirection | false): "ascending" | "descending" | "none" {
  if (direction === "asc") return "ascending";
  if (direction === "desc") return "descending";
  return "none";
}

export function SortableTableHeader({
  label,
  direction,
  onSort,
  align = "left",
  allowUnsorted = false,
  className,
}: {
  label: string;
  direction: SortDirection | false;
  onSort: (direction: SortDirection | false) => void;
  align?: "left" | "right";
  allowUnsorted?: boolean;
  className?: string;
}) {
  const nextDirection = nextSortDirection(direction, allowUnsorted);
  const Icon = direction === "asc" ? ArrowUp : direction === "desc" ? ArrowDown : ArrowUpDown;

  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      className={cn(
        "h-8 gap-1.5 px-2",
        align === "left" ? "-ml-2" : "-mr-2",
        className,
      )}
      aria-label={
        nextDirection === false
          ? `Stop sorting by ${label}`
          : `Sort by ${label} ${nextDirection === "asc" ? "ascending" : "descending"}`
      }
      data-sort-direction={direction || "none"}
      onClick={() => onSort(nextDirection)}
    >
      {label}
      <Icon className={cn("h-3.5 w-3.5", direction === false && "opacity-50")} />
    </Button>
  );
}
