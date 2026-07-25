import type { ReactNode } from "react";

import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";

interface ListPageProps {
  title: ReactNode;
  description?: string;
  subtitle?: string;
  action?: ReactNode;
  children: ReactNode;
}

/**
 * Canonical shell for dashboard pages centered on resource lists or tables.
 *
 * Keeping the wide container, page header, and section rhythm here prevents
 * list pages from drifting as new dashboard surfaces are added.
 */
export function ListPage({
  title,
  description,
  subtitle,
  action,
  children,
}: ListPageProps) {
  return (
    <PageContainer size="wide">
      <div data-slot="list-page" className="space-y-6">
        <PageHeader
          title={title}
          description={description}
          subtitle={subtitle}
          action={action}
        />
        {children}
      </div>
    </PageContainer>
  );
}
