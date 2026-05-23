"use client";

import { useMemo, useState } from "react";
import { ChevronDown } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

type EmojiOption = {
  emoji: string;
  label: string;
  keywords: string;
};

const FEATURED_EMOJIS = [
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
  { emoji: "✨", label: "Sparkles" },
  { emoji: "🔥", label: "Fire" },
  { emoji: "✅", label: "Check mark" },
  { emoji: "🚨", label: "Siren" },
  { emoji: "🧠", label: "Brain" },
  { emoji: "💡", label: "Light bulb" },
  { emoji: "🧰", label: "Toolbox" },
  { emoji: "🧯", label: "Fire extinguisher" },
  { emoji: "🩺", label: "Stethoscope" },
  { emoji: "🧭", label: "Compass" },
] as const;

const EMOJI_RANGES = [
  { start: 0x1F300, end: 0x1F5FF, label: "Symbols and pictographs" },
  { start: 0x1F600, end: 0x1F64F, label: "Smileys and people" },
  { start: 0x1F680, end: 0x1F6FF, label: "Transport and map" },
  { start: 0x1F700, end: 0x1F77F, label: "Alchemical symbols" },
  { start: 0x1F780, end: 0x1F7FF, label: "Geometric symbols" },
  { start: 0x1F800, end: 0x1F8FF, label: "Supplemental arrows" },
  { start: 0x1F900, end: 0x1F9FF, label: "Supplemental symbols and pictographs" },
  { start: 0x1FA70, end: 0x1FAFF, label: "Symbols and pictographs extended" },
  { start: 0x2600, end: 0x27BF, label: "Miscellaneous symbols" },
] as const;

const emojiPresentation = (codePoint: number) =>
  codePoint >= 0x2600 && codePoint <= 0x27BF
    ? `${String.fromCodePoint(codePoint)}\uFE0F`
    : String.fromCodePoint(codePoint);

const unicodeLabel = (codePoint: number, category: string) =>
  `${category} U+${codePoint.toString(16).toUpperCase()}`;

const AUTOMATION_EMOJIS: EmojiOption[] = (() => {
  const seen = new Set<string>();
  const options: EmojiOption[] = [];

  const add = (emoji: string, label: string, keywords = "") => {
    if (seen.has(emoji)) return;
    seen.add(emoji);
    options.push({ emoji, label, keywords: `${label} ${emoji} ${keywords}` });
  };

  FEATURED_EMOJIS.forEach((item) => add(item.emoji, item.label, "automation"));
  EMOJI_RANGES.forEach((range) => {
    for (let codePoint = range.start; codePoint <= range.end; codePoint += 1) {
      add(emojiPresentation(codePoint), unicodeLabel(codePoint, range.label), range.label);
    }
  });

  return options;
})();

const INITIAL_EMOJI_COUNT = 128;

export function AutomationEmojiPicker({
  value,
  onChange,
  className,
  open,
  onOpenChange,
  trigger = "select",
  triggerLabel = "Automation emoji",
  disabled = false,
}: {
  value: string;
  onChange: (value: string) => void;
  className?: string;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  trigger?: "select" | "icon";
  triggerLabel?: string;
  disabled?: boolean;
}) {
  const [internalOpen, setInternalOpen] = useState(false);
  const [query, setQuery] = useState("");
  const pickerOpen = open ?? internalOpen;
  const setPickerOpen = onOpenChange ?? setInternalOpen;
  const selected = useMemo(
    () => AUTOMATION_EMOJIS.find((item) => item.emoji === value) ?? { emoji: value || "⚙️", label: "Selected emoji", keywords: value || "gear" },
    [value],
  );
  const visibleOptions = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase();
    if (!normalizedQuery) {
      return AUTOMATION_EMOJIS.slice(0, INITIAL_EMOJI_COUNT);
    }
    return AUTOMATION_EMOJIS.filter((item) => item.keywords.toLowerCase().includes(normalizedQuery));
  }, [query]);

  return (
    <Popover open={pickerOpen} onOpenChange={setPickerOpen}>
      <PopoverTrigger asChild>
        {trigger === "icon" ? (
          <Button
            type="button"
            variant="outline"
            size="icon-lg"
            aria-label={triggerLabel}
            disabled={disabled}
            className={cn("text-lg leading-none", className)}
          >
            {selected.emoji}
          </Button>
        ) : (
          <Button
            type="button"
            variant="outline"
            aria-label={triggerLabel}
            disabled={disabled}
            className={cn("h-9 w-16 justify-center gap-1 px-2", className)}
          >
            <span className="text-lg leading-none" aria-hidden="true">{selected.emoji}</span>
            <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          </Button>
        )}
      </PopoverTrigger>
      <PopoverContent className="w-80 p-0" align="start">
        <Command shouldFilter={false}>
          <CommandInput
            placeholder="Search emoji..."
            value={query}
            onValueChange={setQuery}
          />
          <CommandList className="max-h-80">
            <CommandEmpty>No emoji found.</CommandEmpty>
            <CommandGroup className="grid grid-cols-8 gap-1 p-2">
              {visibleOptions.map((item) => (
                <CommandItem
                  key={item.emoji}
                  value={item.keywords}
                  aria-label={item.label}
                  className={cn(
                    "flex h-8 w-8 cursor-pointer items-center justify-center rounded-md p-0 text-lg leading-none",
                    item.emoji === selected.emoji && "bg-primary text-primary-foreground data-[selected=true]:bg-primary data-[selected=true]:text-primary-foreground",
                  )}
                  onSelect={() => {
                    onChange(item.emoji);
                    setPickerOpen(false);
                  }}
                >
                  <span aria-hidden="true">{item.emoji}</span>
                  <span className="sr-only">{item.label}</span>
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
