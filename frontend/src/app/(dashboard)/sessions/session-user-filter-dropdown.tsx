"use client";

import { ChevronDown, Users } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  userFilterLabel,
  userFilterParamForMember,
  type UserFilterParam,
} from "@/hooks/use-session-user-filter";
import type { User } from "@/lib/types";

interface SessionUserFilterDropdownProps {
  currentUserFilter: string;
  members: User[];
  currentUser: User | null;
  onFilterChange: (value: UserFilterParam) => void;
  /** Dropdown content alignment. */
  align?: "start" | "end";
  /** Additional class names for the trigger button. */
  className?: string;
}

export function SessionUserFilterDropdown({
  currentUserFilter,
  members,
  currentUser,
  onFilterChange,
  align = "end",
  className,
}: SessionUserFilterDropdownProps) {
  const label = userFilterLabel(currentUserFilter, members);

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          className={cn(
            "flex items-center gap-1.5 px-2.5 py-1.5 text-[12px] font-medium rounded-md border border-border bg-muted/50 hover:bg-muted transition-colors text-foreground",
            className,
          )}
        >
          <Users className="h-3.5 w-3.5 text-muted-foreground" />
          {label}
          <ChevronDown className="h-3 w-3 text-muted-foreground" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align={align} className="w-48">
        <DropdownMenuItem
          className={cn("text-[12px]", currentUserFilter === "mine" && "font-semibold")}
          onClick={() => onFilterChange(null)}
        >
          Mine
        </DropdownMenuItem>
        <DropdownMenuItem
          className={cn("text-[12px]", currentUserFilter === "all" && "font-semibold")}
          onClick={() => onFilterChange("all")}
        >
          Everyone
        </DropdownMenuItem>
        {members.length > 0 && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuLabel className="text-[11px] text-muted-foreground font-normal">
              Team members
            </DropdownMenuLabel>
            {members.map((member) => (
              <DropdownMenuItem
                key={member.id}
                className={cn("text-[12px]", currentUserFilter === member.id && "font-semibold")}
                onClick={() => onFilterChange(userFilterParamForMember(member.id, currentUser?.id))}
              >
                {member.name}
                {member.id === currentUser?.id && (
                  <span className="text-[10px] text-muted-foreground ml-1">(you)</span>
                )}
              </DropdownMenuItem>
            ))}
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
