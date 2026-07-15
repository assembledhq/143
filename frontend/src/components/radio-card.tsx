import type { ReactNode } from "react";
import { RadioGroupItem } from "@/components/ui/radio-group";

export function RadioCard({
  value,
  label,
  description,
  selected,
  icon,
  ariaLabel,
}: {
  value: string;
  label: string;
  description?: string;
  selected: boolean;
  icon?: ReactNode;
  ariaLabel?: string;
}) {
  const indent = icon ? "pl-10" : "pl-6";
  return (
    <label
      className={`relative flex cursor-pointer flex-col rounded-lg border p-3 shadow-sm transition-all duration-150 ${
        selected
          ? "border-primary/50 bg-accent/55 ring-1 ring-primary/20"
          : "border-input hover:bg-muted/40 hover:border-border"
      }`}
    >
      <div className="flex items-center gap-2">
        <RadioGroupItem value={value} {...(ariaLabel ? { "aria-label": ariaLabel } : {})} />
        {icon}
        <span className="text-xs font-medium">{label}</span>
      </div>
      {description && (
        <span className={`mt-1 ${indent} text-xs text-muted-foreground`}>
          {description}
        </span>
      )}
    </label>
  );
}
