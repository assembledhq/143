import type { ReactNode } from "react";

interface PageHeaderProps {
  title: ReactNode;
  description?: string;
  subtitle?: string;
  action?: ReactNode;
}

export function PageHeader({ title, description, subtitle, action }: PageHeaderProps) {
  return (
    <div data-slot="page-header" className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
      <div>
        <h1 className="font-display text-2xl leading-[1.25] font-semibold tracking-[-0.035em] text-foreground sm:text-[1.75rem] sm:leading-[2.125rem]">{title}</h1>
        {description && (
          <p className="mt-1.5 max-w-2xl text-sm leading-5 text-muted-foreground">{description}</p>
        )}
        {subtitle && (
          <p className="mt-1 type-dense text-muted-foreground">{subtitle}</p>
        )}
      </div>
      {action && (
        <div className="w-full shrink-0 sm:w-auto [&>*]:w-full sm:[&>*]:w-auto">
          {action}
        </div>
      )}
    </div>
  );
}
