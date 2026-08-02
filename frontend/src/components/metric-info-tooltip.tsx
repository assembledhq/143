"use client";

import { Info } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";

export function MetricInfoTooltip({ label, definition }: { label: string; definition: string }) {
  return (
    <TooltipProvider delayDuration={150}>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            size="icon-compact"
            className="rounded-full text-muted-foreground hover:text-foreground"
            aria-label={`About ${label}`}
          >
            <Info className="size-3.5" />
          </Button>
        </TooltipTrigger>
        <TooltipContent side="top" sideOffset={6} className="max-w-72 leading-5">
          {definition}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
