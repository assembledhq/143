"use client";

import { CircleHelp, ExternalLink } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

interface APIKeyHelpTooltipProps {
  ariaLabel: string;
  description: string;
  href: string;
  linkLabel: string;
}

export function APIKeyHelpTooltip({ ariaLabel, description, href, linkLabel }: APIKeyHelpTooltipProps) {
  return (
    <TooltipProvider delayDuration={150}>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="h-5 w-5 rounded-full text-muted-foreground hover:text-foreground"
            aria-label={ariaLabel}
          >
            <CircleHelp className="h-3.5 w-3.5" />
          </Button>
        </TooltipTrigger>
        <TooltipContent side="top" sideOffset={6} className="max-w-72 space-y-2">
          <p>{description}</p>
          <a
            href={href}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1 underline underline-offset-2"
          >
            {linkLabel}
            <ExternalLink className="h-3 w-3" />
          </a>
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
