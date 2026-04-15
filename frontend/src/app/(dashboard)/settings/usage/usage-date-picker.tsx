"use client";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export type DatePreset = "7d" | "30d" | "this_month";

interface UsageDatePickerProps {
  activePreset: DatePreset;
  onPresetChange: (preset: DatePreset) => void;
}

const presets: { value: DatePreset; label: string }[] = [
  { value: "7d", label: "Last 7d" },
  { value: "30d", label: "Last 30d" },
  { value: "this_month", label: "This month" },
];

export function UsageDatePicker({ activePreset, onPresetChange }: UsageDatePickerProps) {
  return (
    <div className="flex items-center gap-1">
      {presets.map((p) => (
        <Button
          key={p.value}
          variant={activePreset === p.value ? "default" : "outline"}
          size="sm"
          className={cn(
            "h-8 text-xs",
            activePreset !== p.value && "text-muted-foreground"
          )}
          onClick={() => onPresetChange(p.value)}
        >
          {p.label}
        </Button>
      ))}
    </div>
  );
}
