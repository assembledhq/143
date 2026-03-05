import type { ReactNode } from "react";
import { Card, CardContent } from "@/components/ui/card";

type IntegrationsCardItem = {
  id: string;
  title: string;
  description: string;
  action: ReactNode;
  icon?: ReactNode;
  badge?: ReactNode;
};

type IntegrationsCardProps = {
  items: IntegrationsCardItem[];
};

export function IntegrationsCard({ items }: IntegrationsCardProps) {
  return (
    <div className="space-y-3">
      {items.map((item) => (
        <Card key={item.id} className="py-0" data-testid="integration-card">
          <CardContent className="flex items-center justify-between gap-4 py-4">
            <div className="flex items-center gap-3 min-w-0 flex-1">
              {item.icon && (
                <div className="flex shrink-0 items-center justify-center h-9 w-9 rounded-lg bg-muted text-muted-foreground">
                  {item.icon}
                </div>
              )}
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <p className="text-sm font-medium text-foreground">{item.title}</p>
                  {item.badge}
                </div>
                <p className="mt-0.5 text-sm text-muted-foreground">{item.description}</p>
              </div>
            </div>
            <div className="shrink-0">{item.action}</div>
          </CardContent>
        </Card>
      ))}
    </div>
  );
}
