"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronsUpDown, GitBranch } from "lucide-react";

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
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { cn } from "@/lib/utils";
import type { ListResponse } from "@/lib/types";

type BranchInfo = {
  name: string;
  protected: boolean;
};

interface BranchPickerProps {
  repositoryId: string;
  value: string;
  defaultBranch?: string;
  onValueChange: (branch: string) => void;
  label: string;
  id?: string;
  className?: string;
  buttonClassName?: string;
  contentClassName?: string;
  disabled?: boolean;
}

export function BranchPicker({
  repositoryId,
  value,
  defaultBranch,
  onValueChange,
  label,
  id,
  className,
  buttonClassName,
  contentClassName,
  disabled = false,
}: BranchPickerProps) {
  const [open, setOpen] = useState(false);

  const { data, isLoading, isError, refetch } = useQuery<ListResponse<BranchInfo>>({
    queryKey: queryKeys.repositories.branches(repositoryId),
    queryFn: () => api.repositories.branches(repositoryId),
    enabled: !!repositoryId && open,
    staleTime: 0,
  });

  const branches = useMemo(() => data?.data ?? [], [data]);
  const selectedBranch = value || defaultBranch || "";

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="outline"
          id={id}
          aria-label={label}
          aria-expanded={open}
          disabled={disabled || !repositoryId}
          className={cn("justify-between gap-2 font-normal", className, buttonClassName)}
        >
          <span className="flex min-w-0 items-center gap-2">
            <GitBranch className="h-4 w-4 shrink-0 text-muted-foreground" />
            <span className="truncate">{selectedBranch || "Select branch"}</span>
          </span>
          <ChevronsUpDown className="h-4 w-4 shrink-0 text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className={cn("w-[var(--radix-popover-trigger-width)] p-0", contentClassName)}>
        <Command>
          <CommandInput placeholder="Search branches..." />
          <CommandList>
            {isLoading && (
              <div className="px-3 py-4 text-sm text-muted-foreground">Loading branches...</div>
            )}
            {!isLoading && isError && (
              <div className="px-3 py-4 text-sm text-muted-foreground">
                <p>Could not load branches.</p>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="mt-2 h-7 px-2"
                  onClick={() => {
                    void refetch();
                  }}
                >
                  Retry
                </Button>
              </div>
            )}
            {!isLoading && !isError && branches.length === 0 && (
              <CommandEmpty>No branches found.</CommandEmpty>
            )}
            {!isLoading && !isError && branches.length > 0 && (
              <CommandGroup>
                {branches.map((branch) => (
                  <CommandCheckItem
                    key={branch.name}
                    checked={selectedBranch === branch.name}
                    value={branch.name}
                    keywords={branch.protected ? ["protected"] : undefined}
                    onSelect={() => {
                      onValueChange(branch.name);
                      setOpen(false);
                    }}
                  >
                    <span className="truncate">{branch.name}</span>
                  </CommandCheckItem>
                ))}
              </CommandGroup>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
