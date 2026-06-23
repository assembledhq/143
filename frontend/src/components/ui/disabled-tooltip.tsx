"use client";

import * as React from "react";

import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

type DisabledTooltipProps = {
  children: React.ReactElement;
  content?: React.ReactNode;
  disabled?: boolean;
};

export function DisabledTooltip({ children, content, disabled = false }: DisabledTooltipProps) {
  // Once the wrapper span has been rendered, keep it even if content later
  // becomes undefined. Removing the wrapper unmounts the child element, which
  // invalidates any external DOM reference held to it.
  const [everHadContent, setEverHadContent] = React.useState(() => !!content);
  React.useEffect(() => {
    if (content) setEverHadContent(true);
  }, [content]);

  if (!everHadContent) {
    return children;
  }

  return (
    <TooltipProvider delayDuration={150}>
      <Tooltip open={disabled && !!content ? undefined : false}>
        <TooltipTrigger asChild>
          <span
            className={disabled ? "inline-flex cursor-not-allowed" : "inline-flex"}
            tabIndex={disabled ? 0 : undefined}
          >
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
