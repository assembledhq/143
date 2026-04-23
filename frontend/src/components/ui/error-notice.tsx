import type { ReactNode } from "react";
import { AlertTriangle } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { errorSurfaceClassNames } from "./error-styles";

interface ErrorNoticeAction {
  label: string;
  onClick: () => void;
}

interface ErrorNoticeProps {
  title: ReactNode;
  description: ReactNode;
  action?: ErrorNoticeAction;
  className?: string;
}

export function ErrorNotice({ title, description, action, className }: ErrorNoticeProps) {
  return (
    <Card
      role="alert"
      data-slot="error-notice"
      className={cn("rounded-xl shadow-sm", errorSurfaceClassNames.container, className)}
    >
      <CardContent className="flex flex-wrap items-start gap-3 p-3">
        <div className={errorSurfaceClassNames.iconContainer}>
          <AlertTriangle className="size-4" aria-hidden="true" />
        </div>
        <div className="min-w-0 flex-1 space-y-1">
          <p className={errorSurfaceClassNames.title}>{title}</p>
          <p className={errorSurfaceClassNames.description}>{description}</p>
        </div>
        {action && (
          <Button
            variant="outline"
            size="sm"
            className={cn("self-start", errorSurfaceClassNames.action)}
            onClick={action.onClick}
          >
            {action.label}
          </Button>
        )}
      </CardContent>
    </Card>
  );
}
