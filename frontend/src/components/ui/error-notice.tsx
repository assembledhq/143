import type { HTMLAttributes, ReactNode } from "react";
import { AlertTriangle, X } from "lucide-react";
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
  description?: ReactNode;
  action?: ErrorNoticeAction;
  onDismiss?: () => void;
  dismissLabel?: string;
  className?: string;
}

type ErrorTextProps = HTMLAttributes<HTMLParagraphElement>;

export function ErrorText({ className, ...props }: ErrorTextProps) {
  return (
    <p
      className={cn(
        errorSurfaceClassNames.textWrap,
        "text-xs text-destructive",
        className,
      )}
      {...props}
    />
  );
}

export function ErrorNotice({
  title,
  description,
  action,
  onDismiss,
  dismissLabel = "Dismiss error",
  className,
}: ErrorNoticeProps) {
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
          {description && (
            <p className={errorSurfaceClassNames.description}>{description}</p>
          )}
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
        {onDismiss && (
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            className="shrink-0 rounded p-0.5 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
            aria-label={dismissLabel}
            onClick={onDismiss}
          >
            <X className="size-3.5" aria-hidden="true" />
          </Button>
        )}
      </CardContent>
    </Card>
  );
}
