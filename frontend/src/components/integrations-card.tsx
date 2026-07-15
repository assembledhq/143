import type { ReactNode } from "react";
import { ResourceRow } from "@/components/resource-row";
import { Card } from "@/components/ui/card";

type IntegrationsCardItem = {
  id: string;
  title: string;
  description: string;
  action: ReactNode;
  logo?: ReactNode;
  badge?: ReactNode;
  summary?: ReactNode;
};

type IntegrationsCardProps = {
  items: IntegrationsCardItem[];
};

export function IntegrationsCard({ items }: IntegrationsCardProps) {
  return (
    <Card className="divide-y divide-border/80" data-testid="integrations-list">
      {items.map((item) => (
        <ResourceRow
          key={item.id}
          data-testid="integration-card"
          leading={item.logo}
          title={<div className="flex items-center gap-2 text-sm">{item.title}{item.badge}</div>}
          metadata={<span className="text-sm leading-5">{item.description}</span>}
          detail={item.summary}
          actions={<div className="[&>*]:w-full sm:[&>*]:w-auto">{item.action}</div>}
          className="px-4 py-4"
        />
      ))}
    </Card>
  );
}
