import type { HTMLAttributes, ReactNode } from "react";

import { cn } from "@/lib/utils";

type ContextHeaderProps = Omit<HTMLAttributes<HTMLElement>, "title"> & {
  title: ReactNode;
  metadata?: ReactNode;
  status?: ReactNode;
  actions?: ReactNode;
  tabs?: ReactNode;
};

export function ContextHeader({ title, metadata, status, actions, tabs, className, ...props }: ContextHeaderProps) {
  return (
    <header data-slot="context-header" className={cn("border-b border-border bg-background", className)} {...props}>
      <div className="flex min-h-14 items-center gap-3 px-4 sm:px-5">
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 items-center gap-2.5">
            <div className="min-w-0 flex-1">{title}</div>
            {status}
          </div>
          {metadata ? <div className="mt-0.5 flex items-center gap-2 text-xs text-muted-foreground">{metadata}</div> : null}
        </div>
        {actions ? <div className="flex shrink-0 items-center gap-1.5">{actions}</div> : null}
      </div>
      {tabs ? <div className="px-4 sm:px-5">{tabs}</div> : null}
    </header>
  );
}
