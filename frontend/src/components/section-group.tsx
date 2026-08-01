import type { HTMLAttributes, ReactNode } from "react";

import { cn } from "@/lib/utils";

type SectionGroupProps = Omit<HTMLAttributes<HTMLElement>, "title"> & {
  title?: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
  variant?: "plain" | "bordered" | "recessed";
  /**
   * Heading level for `title`. Raise it when a group nests inside another
   * titled group so the page keeps a real outline instead of a run of h2s.
   * Size is deliberately not tied to this: level expresses structure, and
   * containment plus position express prominence.
   */
  headingLevel?: 2 | 3;
};

export function SectionGroup({ title, description, action, variant = "plain", headingLevel = 2, className, children, ...props }: SectionGroupProps) {
  const Heading = `h${headingLevel}` as const;
  return (
    <section
      data-slot="section-group"
      data-variant={variant}
      className={cn(
        "space-y-4",
        variant === "bordered" && "rounded-xl border border-border bg-card p-4 sm:p-5",
        variant === "recessed" && "rounded-xl bg-surface-recessed p-4 sm:p-5",
        className,
      )}
      {...props}
    >
      {title || description || action ? (
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0 space-y-1">
            {title ? <Heading className="font-display text-lg leading-6 font-semibold tracking-[-0.025em] text-foreground">{title}</Heading> : null}
            {description ? <div className="max-w-2xl text-sm leading-5 text-muted-foreground">{description}</div> : null}
          </div>
          {action ? <div className="shrink-0">{action}</div> : null}
        </div>
      ) : null}
      {children}
    </section>
  );
}
