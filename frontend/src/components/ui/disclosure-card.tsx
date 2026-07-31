"use client";

import { useState, type ReactNode } from "react";
import { ChevronDown } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { cn } from "@/lib/utils";

type DisclosureCardProps = {
  title: string;
  description: string;
  summary?: ReactNode;
  children: ReactNode;
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  variant?: "card" | "inset";
  showLabel?: string;
  hideLabel?: string;
  showAriaLabel?: string;
  hideAriaLabel?: string;
  status?: ReactNode;
  className?: string;
  contentClassName?: string;
  actionTestId?: string;
};

function DisclosureCard({
  title,
  description,
  summary,
  children,
  open,
  defaultOpen,
  onOpenChange,
  variant = "card",
  showLabel = "Show",
  hideLabel = "Hide",
  showAriaLabel = `Show ${title}`,
  hideAriaLabel = `Hide ${title}`,
  status,
  className,
  contentClassName,
  actionTestId,
}: DisclosureCardProps) {
  const isInset = variant === "inset";
  const [internalOpen, setInternalOpen] = useState(defaultOpen ?? false);
  const isOpen = open ?? internalOpen;
  const handleOpenChange = (nextOpen: boolean) => {
    if (open === undefined) {
      setInternalOpen(nextOpen);
    }
    onOpenChange?.(nextOpen);
  };

  return (
    <Collapsible open={isOpen} onOpenChange={handleOpenChange}>
      <Card
        variant={isInset ? "quiet" : "default"}
        className={cn(
          isInset && "rounded-none border-t border-border/70",
          className,
        )}
      >
        <CollapsibleTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            aria-label={isOpen ? hideAriaLabel : showAriaLabel}
            className={cn(
              "flex h-auto min-h-20 w-full items-start justify-between gap-4 rounded-none py-4 text-left hover:bg-transparent sm:items-center",
              isInset ? "px-0" : "px-4",
            )}
          >
            <span className="block min-w-0 flex-1 space-y-1">
              <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <span className="text-sm font-medium whitespace-normal text-foreground">
                  {title}
                </span>
                {status}
              </span>
              <span className="block text-xs leading-5 whitespace-normal text-muted-foreground">
                {description}
              </span>
              {summary ? (
                <span className="block text-xs font-medium whitespace-normal text-muted-foreground">
                  {summary}
                </span>
              ) : null}
            </span>
            <span
              data-testid={actionTestId}
              className="flex shrink-0 items-center gap-2 pt-0.5 text-xs font-medium text-muted-foreground sm:pt-0"
            >
              {isOpen ? hideLabel : showLabel}
              <ChevronDown
                className={cn(
                  "size-4 transition-transform",
                  isOpen && "rotate-180",
                )}
                aria-hidden="true"
              />
            </span>
          </Button>
        </CollapsibleTrigger>
        <CollapsibleContent className="border-t border-border/70">
          <div className={cn(isInset ? "px-0" : "px-4", contentClassName)}>
            {children}
          </div>
        </CollapsibleContent>
      </Card>
    </Collapsible>
  );
}

export { DisclosureCard };
export type { DisclosureCardProps };
