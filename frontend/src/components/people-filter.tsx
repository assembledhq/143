"use client";

import { ChevronDown, Users } from "lucide-react";
import { useMemo, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Command,
  CommandCheckItem,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandList,
} from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";
import { peopleFilterLabel, type PeopleFilterMode, type PeopleFilterParam } from "@/hooks/use-people-filter";
import type { User } from "@/lib/types";

interface PeopleFilterProps {
  mode: PeopleFilterMode;
  selectedUserIDs: string[];
  members: User[];
  currentUser: User | null;
  onFilterChange: (value: PeopleFilterParam) => void;
  align?: "start" | "center" | "end";
  className?: string;
}

function serializeSelection(selectedUserIDs: string[], currentUser: User | null): string | null {
  if (selectedUserIDs.length === 0) return null;
  if (currentUser && selectedUserIDs.length === 1 && selectedUserIDs[0] === currentUser.id) {
    return null;
  }
  return selectedUserIDs.join(",");
}

function filterChips(selectedUserIDs: string[], members: User[], currentUser: User | null) {
  return selectedUserIDs.map((id) => {
    if (id === currentUser?.id) return { id, label: "You" };
    const member = members.find((item) => item.id === id);
    return { id, label: member?.name.split(" ")[0] ?? "User" };
  });
}

export function PeopleFilter({
  mode,
  selectedUserIDs,
  members,
  currentUser,
  onFilterChange,
  align = "end",
  className,
}: PeopleFilterProps) {
  const [open, setOpen] = useState(false);
  const label = peopleFilterLabel(mode, selectedUserIDs, members, currentUser);
  const chips = useMemo(() => filterChips(selectedUserIDs, members, currentUser), [currentUser, members, selectedUserIDs]);

  function setMine() {
    onFilterChange(null);
    setOpen(false);
  }

  function setEveryone() {
    onFilterChange("all");
    setOpen(false);
  }

  function toggleMember(memberID: string) {
    const currentSet = new Set(
      mode === "mine" && currentUser
        ? [currentUser.id]
        : mode === "all"
          ? []
          : selectedUserIDs,
    );

    if (currentSet.has(memberID)) {
      currentSet.delete(memberID);
    } else {
      currentSet.add(memberID);
    }

    const next = Array.from(currentSet);
    next.sort((a, b) => {
      if (a === currentUser?.id) return -1;
      if (b === currentUser?.id) return 1;
      return a.localeCompare(b);
    });

    onFilterChange(serializeSelection(next, currentUser));
  }

  return (
    <div className={cn("min-w-0", className)}>
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <Button variant="outline" className="bg-background min-w-0">
            <Users className="h-3.5 w-3.5 text-muted-foreground" />
            <span className="truncate">{label}</span>
            <ChevronDown className="h-3 w-3 text-muted-foreground" />
          </Button>
        </PopoverTrigger>
        <PopoverContent align={align} className="w-72 p-0">
          <div className="border-b border-border px-3 py-3">
            <div className="flex items-center gap-2">
              <Button
                type="button"
                variant={mode === "mine" ? "default" : "outline"}
                size="sm"
                className="h-7"
                onClick={setMine}
              >
                Mine
              </Button>
              <Button
                type="button"
                variant={mode === "all" ? "default" : "outline"}
                size="sm"
                className="h-7"
                onClick={setEveryone}
              >
                Everyone
              </Button>
            </div>
            {mode === "custom" && chips.length > 0 && (
              <div className="mt-3 flex flex-wrap gap-1.5">
                {chips.map((chip) => (
                  <Badge key={chip.id} variant="secondary" className="text-xs">
                    {chip.label}
                  </Badge>
                ))}
              </div>
            )}
          </div>

          <Command shouldFilter>
            <CommandInput placeholder="Filter people..." />
            <CommandList className="max-h-64">
              <CommandEmpty>No people found.</CommandEmpty>
              <CommandGroup heading="Team members">
                {members.map((member) => {
                  const isChecked = mode === "mine"
                    ? member.id === currentUser?.id
                    : mode === "custom"
                      ? selectedUserIDs.includes(member.id)
                      : false;

                  return (
                    <CommandCheckItem
                      key={member.id}
                      checked={isChecked}
                      value={`${member.name} ${member.email}`}
                      onSelect={() => toggleMember(member.id)}
                      className="flex items-center"
                    >
                      <span className="min-w-0 flex-1 truncate text-sm">
                        {member.name}
                        {member.id === currentUser?.id && (
                          <span className="ml-1 text-xs text-muted-foreground">(you)</span>
                        )}
                      </span>
                    </CommandCheckItem>
                  );
                })}
              </CommandGroup>
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>

      {mode === "custom" && chips.length > 0 && (
        <div className="mt-2 flex flex-wrap gap-1.5">
          {chips.slice(0, 2).map((chip) => (
            <Badge key={chip.id} variant="outline" className="text-xs">
              {chip.label}
            </Badge>
          ))}
          {chips.length > 2 && (
            <Badge variant="outline" className="text-xs">
              +{chips.length - 2}
            </Badge>
          )}
        </div>
      )}
    </div>
  );
}
