"use client";

import { useId, useState, type ReactNode } from "react";
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
  onOpenChange?: (open: boolean) => void;
  /**
   * `inset` drops the standalone card chrome so the disclosure can sit as the
   * last row inside another card's content. It draws no separator of its own:
   * the preceding setting row's bottom border already provides one.
   */
  variant?: "card" | "inset";
  showAriaLabel?: string;
  hideAriaLabel?: string;
  /**
   * Rendered beside the trigger, never inside it. Status chips are usually
   * `aria-live` regions, and a button's descendants are presentational, so a
   * chip nested in the trigger would never be announced. Chips reserve width
   * even when idle, so the header stacks below `sm` rather than squeezing the
   * title column.
   */
  status?: ReactNode;
  className?: string;
  actionTestId?: string;
};

function DisclosureCard({
  title,
  description,
  summary,
  children,
  open,
  onOpenChange,
  variant = "card",
  showAriaLabel = `Show ${title}`,
  hideAriaLabel = `Hide ${title}`,
  status,
  className,
  actionTestId,
}: DisclosureCardProps) {
  const isInset = variant === "inset";
  const descriptionId = useId();
  const [internalOpen, setInternalOpen] = useState(false);
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
        className={cn(isInset && "rounded-none", className)}
      >
        <div
          className={cn(
            "flex flex-col sm:flex-row sm:items-start",
            !isInset && "px-4",
          )}
        >
          <CollapsibleTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              aria-label={isOpen ? hideAriaLabel : showAriaLabel}
              aria-describedby={descriptionId}
              className="flex h-auto min-h-20 min-w-0 flex-1 items-start justify-between gap-4 rounded-none px-0 py-4 text-left hover:bg-transparent sm:items-center"
            >
              <span className="block min-w-0 flex-1 space-y-1">
                <span className="block text-sm font-medium whitespace-normal text-foreground">
                  {title}
                </span>
                <span id={descriptionId} className="block space-y-1">
                  <span className="block text-xs leading-5 whitespace-normal text-muted-foreground">
                    {description}
                  </span>
                  {summary ? (
                    <span className="block text-xs font-medium whitespace-normal text-muted-foreground">
                      {summary}
                    </span>
                  ) : null}
                </span>
              </span>
              {/* This wrapper groups the Hide/Show label with the chevron, and the
                  chevron has to stay inside it: as a direct child of the button it
                  would match the size variant's `has-[>svg]:px-2`, whose `:has()`
                  specificity outranks the `px-0` set above and restores a gutter. */}
              <span
                data-testid={actionTestId}
                className="flex shrink-0 items-center gap-2 pt-0.5 text-xs font-medium text-muted-foreground sm:pt-0"
              >
                {isOpen ? "Hide" : "Show"}
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
          {status ? (
            <span className="flex shrink-0 items-center pb-4 sm:self-center sm:pb-0 sm:pl-3">
              {status}
            </span>
          ) : null}
        </div>
        <CollapsibleContent className="border-t border-border/70">
          <div className={cn(!isInset && "px-4")}>{children}</div>
        </CollapsibleContent>
      </Card>
    </Collapsible>
  );
}

export { DisclosureCard };
export type { DisclosureCardProps };
