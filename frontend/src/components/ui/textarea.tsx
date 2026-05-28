import * as React from "react";

import { cn } from "@/lib/utils";
import { raisedSurface } from "@/lib/surfaces";

export type TextareaProps = React.TextareaHTMLAttributes<HTMLTextAreaElement>;

const Textarea = React.forwardRef<HTMLTextAreaElement, TextareaProps>(
  ({ className, ...props }, ref) => {
    return (
      <textarea
        ref={ref}
        className={cn(
          raisedSurface,
          "flex min-h-[72px] w-full rounded-md border border-input px-2.5 py-1.5 text-xs max-sm:text-base",
          "ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2",
          "focus-visible:ring-ring/40 focus-visible:ring-offset-0 disabled:cursor-not-allowed disabled:opacity-50",
          className
        )}
        {...props}
      />
    );
  }
);
Textarea.displayName = "Textarea";

export { Textarea };
