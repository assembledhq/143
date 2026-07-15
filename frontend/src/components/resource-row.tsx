import type { HTMLAttributes, ReactNode } from "react";

import { cn } from "@/lib/utils";

type ResourceRowProps = Omit<HTMLAttributes<HTMLDivElement>, "title"> & {
  leading?: ReactNode;
  title: ReactNode;
  metadata?: ReactNode;
  status?: ReactNode;
  detail?: ReactNode;
  actions?: ReactNode;
  actionLayout?: "wrap" | "side";
  selected?: boolean;
};

export function ResourceRow({ leading, title, metadata, status, detail, actions, actionLayout = "wrap", selected = false, className, ...props }: ResourceRowProps) {
  return (
    <div
      data-slot="resource-row"
      data-selected={selected || undefined}
      className={cn(
        "group/resource-row relative flex min-w-0 items-start gap-3 px-3.5 py-3 type-dense transition-colors duration-[175ms] hover:bg-accent/25 data-[selected=true]:bg-accent/55",
        actionLayout === "wrap" && "flex-wrap sm:flex-nowrap",
        selected && "bg-accent/55 before:absolute before:inset-y-2 before:left-0 before:w-0.5 before:rounded-full before:bg-primary",
        className,
      )}
      {...props}
    >
      {leading ? <div className="mt-0.5 shrink-0 text-muted-foreground">{leading}</div> : null}
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-start justify-between gap-3">
          <div className="min-w-0 font-medium text-foreground">{title}</div>
          {status ? <div className="shrink-0">{status}</div> : null}
        </div>
        {metadata ? <div className="mt-0.5 truncate text-muted-foreground">{metadata}</div> : null}
        {detail ? <div className="mt-1.5 text-muted-foreground">{detail}</div> : null}
      </div>
      {actions ? (
        <div
          data-slot="resource-row-actions"
          className={cn(
            "shrink-0 self-center",
            actionLayout === "wrap" && "ml-7 w-full sm:ml-0 sm:w-auto",
          )}
        >
          {actions}
        </div>
      ) : null}
    </div>
  );
}
