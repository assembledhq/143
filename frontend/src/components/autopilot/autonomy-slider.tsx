"use client";

import { cn } from "@/lib/utils";

const LEVELS = [
  {
    value: "manual" as const,
    label: "Suggest",
    description: "PM recommends, you decide",
  },
  {
    value: "auto_simple" as const,
    label: "Act on low-risk work",
    description: "PM auto-creates sessions for bounded work",
  },
  {
    value: "auto_all" as const,
    label: "Operate broadly",
    description: "PM acts automatically on most policy-compliant work",
  },
];

interface AutonomySliderProps {
  value: string;
  onChange: (value: "manual" | "auto_simple" | "auto_all") => void;
}

export function AutonomySlider({ value, onChange }: AutonomySliderProps) {
  return (
    <div className="space-y-2">
      <div className="flex rounded-lg border border-border overflow-hidden">
        {LEVELS.map((level) => (
          <button
            key={level.value}
            onClick={() => onChange(level.value)}
            className={cn(
              "flex-1 px-3 py-2.5 text-left transition-colors border-r border-border last:border-r-0",
              value === level.value
                ? "bg-surface-selected dark:bg-primary/10"
                : "hover:bg-surface-hover"
            )}
          >
            <div className={cn(
              "text-xs font-medium",
              value === level.value ? "text-primary" : "text-foreground"
            )}>
              {level.label}
            </div>
            <div className="text-xs text-muted-foreground mt-0.5">
              {level.description}
            </div>
          </button>
        ))}
      </div>
    </div>
  );
}
