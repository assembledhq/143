"use client";

import Image from "next/image";
import { ShieldAlert } from "lucide-react";
import { ReleaseStageBadge, type ReleaseStage } from "@/components/release-stage-badge";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";

export interface CodingAuthProviderOption<T extends string> {
  key: T;
  label: string;
  badge?: ReleaseStage;
  iconSrc?: string;
}

interface CodingAuthProviderCardsProps<T extends string> {
  options: Array<CodingAuthProviderOption<T>>;
  value: T;
  onValueChange: (value: T) => void;
  className?: string;
  idPrefix?: string;
}

export function CodingAuthProviderCards<T extends string>({
  options,
  value,
  onValueChange,
  className = "grid gap-3 md:grid-cols-2 xl:grid-cols-3",
  idPrefix = "provider",
}: CodingAuthProviderCardsProps<T>) {
  return (
    <RadioGroup
      value={value}
      onValueChange={(next) => onValueChange(next as T)}
      className={className}
    >
      {options.map((option) => (
        <Label
          key={option.key}
          htmlFor={`${idPrefix}-${option.key}`}
          className="flex cursor-pointer items-center gap-3 rounded-xl border border-border p-4"
        >
          <RadioGroupItem value={option.key} id={`${idPrefix}-${option.key}`} />
          <span className="flex h-8 w-8 items-center justify-center rounded-lg bg-muted text-muted-foreground">
            {option.iconSrc ? (
              <Image
                src={option.iconSrc}
                alt=""
                width={16}
                height={16}
                className="h-4 w-4"
                aria-hidden="true"
              />
            ) : (
              <ShieldAlert className="h-4 w-4" aria-hidden="true" />
            )}
          </span>
          <span className="flex min-w-0 items-center gap-2">
            <span className="font-medium text-sm">{option.label}</span>
            {option.badge ? (
              <ReleaseStageBadge stage={option.badge} decorative />
            ) : null}
          </span>
        </Label>
      ))}
    </RadioGroup>
  );
}
