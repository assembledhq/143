"use client";

import * as React from "react";

import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

type DisabledTooltipProps = {
  children: React.ReactElement;
  content?: React.ReactNode;
  disabled?: boolean;
};

export function DisabledTooltip({ children, content, disabled = false }: DisabledTooltipProps) {
  if (!disabled || !content) {
    return children;
  }

  return (
    <TooltipProvider delayDuration={150}>
      <Tooltip>
        <TooltipTrigger asChild>
          <span className="inline-flex cursor-not-allowed" tabIndex={0}>
            {children}
          </span>
        </TooltipTrigger>
        <TooltipContent sideOffset={6}>
          {content}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
