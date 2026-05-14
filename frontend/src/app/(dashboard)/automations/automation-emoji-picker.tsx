"use client";

import { useMemo, useState } from "react";
import { ChevronsUpDown } from "lucide-react";
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

const AUTOMATION_EMOJIS = [
  { emoji: "⚙️", label: "Gear" },
  { emoji: "🧹", label: "Broom" },
  { emoji: "🧪", label: "Test tube" },
  { emoji: "🚀", label: "Rocket" },
  { emoji: "🔒", label: "Lock" },
  { emoji: "📦", label: "Package" },
  { emoji: "🔍", label: "Magnifying glass" },
  { emoji: "🛠️", label: "Tools" },
  { emoji: "📈", label: "Chart" },
  { emoji: "🤖", label: "Robot" },
] as const;

export function AutomationEmojiPicker({
  value,
  onChange,
  className,
}: {
  value: string;
  onChange: (value: string) => void;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const selected = useMemo(
    () => AUTOMATION_EMOJIS.find((item) => item.emoji === value) ?? AUTOMATION_EMOJIS[0],
    [value],
  );

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="outline"
          aria-label="Automation emoji"
          className={cn("h-10 justify-between", className)}
        >
          <span className="flex min-w-0 items-center gap-2">
            <span className="text-lg leading-none" aria-hidden="true">{selected.emoji}</span>
            <span className="truncate">{selected.label}</span>
          </span>
          <ChevronsUpDown className="h-4 w-4 shrink-0 text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-72 p-0" align="start">
        <Command>
          <CommandInput placeholder="Search emoji..." />
          <CommandList>
            <CommandEmpty>No emoji found.</CommandEmpty>
            <CommandGroup>
              {AUTOMATION_EMOJIS.map((item) => (
                <CommandCheckItem
                  key={item.emoji}
                  value={`${item.label} ${item.emoji}`}
                  checked={item.emoji === selected.emoji}
                  onSelect={() => {
                    onChange(item.emoji);
                    setOpen(false);
                  }}
                >
                  <span className="text-lg leading-none" aria-hidden="true">{item.emoji}</span>
                  <span>{item.label}</span>
                </CommandCheckItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
