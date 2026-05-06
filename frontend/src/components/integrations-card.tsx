import type { ReactNode } from "react";
import { Card, CardContent } from "@/components/ui/card";

type IntegrationsCardItem = {
  id: string;
  title: string;
  description: string;
  action: ReactNode;
  logo?: ReactNode;
  badge?: ReactNode;
  extra?: ReactNode;
};

type IntegrationsCardProps = {
  items: IntegrationsCardItem[];
};

export function IntegrationsCard({ items }: IntegrationsCardProps) {
  return (
    <div className="space-y-3">
      {items.map((item) => (
        <Card key={item.id} className="group py-0" data-testid="integration-card">
          <CardContent className="flex flex-col items-start gap-4 py-4 sm:flex-row sm:items-center sm:justify-between">
            <div className="flex min-w-0 flex-1 items-center gap-3">
              {item.logo ? <div className="shrink-0">{item.logo}</div> : null}
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                <p className="text-sm font-medium text-foreground">{item.title}</p>
                  {item.badge}
                </div>
                <p className="mt-0.5 text-sm text-muted-foreground">{item.description}</p>
                {item.extra}
              </div>
            </div>
            <div className="w-full shrink-0 sm:w-auto [&>*]:w-full sm:[&>*]:w-auto">{item.action}</div>
          </CardContent>
        </Card>
      ))}
    </div>
  );
}
