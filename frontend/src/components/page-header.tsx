import type { ReactNode } from "react";

interface PageHeaderProps {
  title: string;
  description?: string;
  subtitle?: string;
  action?: ReactNode;
}

export function PageHeader({ title, description, subtitle, action }: PageHeaderProps) {
  return (
    <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-foreground">{title}</h1>
        {description && (
          <p className="mt-1.5 text-[13px] text-muted-foreground/80">{description}</p>
        )}
        {subtitle && (
          <p className="mt-1 text-[13px] text-muted-foreground">{subtitle}</p>
        )}
      </div>
      {action && <div className="shrink-0">{action}</div>}
    </div>
  );
}
