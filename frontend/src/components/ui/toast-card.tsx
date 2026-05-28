import type { ReactNode } from "react";
import {
  AlertTriangle,
  CheckCircle2,
  CircleAlert,
  Info,
  X,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardTitle,
} from "@/components/ui/card";
import { cn } from "@/lib/utils";

type ToastVariant = "success" | "info" | "warning" | "error";

interface ToastCardAction {
  label: ReactNode;
  onClick: () => void;
}

interface ToastCardProps {
  variant: ToastVariant;
  title: ReactNode;
  description?: ReactNode;
  action?: ToastCardAction;
  onDismiss?: () => void;
}

const variantClassNames: Record<
  ToastVariant,
  {
    container: string;
    iconWrap: string;
    icon: string;
  }
> = {
  success: {
    container:
      "border-emerald-500/20 bg-surface-raised/96 shadow-[0_18px_50px_-28px_rgba(16,185,129,0.35)]",
    iconWrap:
      "border-emerald-500/20 bg-emerald-500/[0.08] text-emerald-600 dark:text-emerald-300",
    icon: "text-emerald-600 dark:text-emerald-300",
  },
  info: {
    container:
      "border-sky-500/20 bg-surface-raised/96 shadow-[0_18px_50px_-28px_rgba(14,165,233,0.3)]",
    iconWrap:
      "border-sky-500/20 bg-sky-500/[0.08] text-sky-600 dark:text-sky-300",
    icon: "text-sky-600 dark:text-sky-300",
  },
  warning: {
    container:
      "border-amber-500/25 bg-surface-raised/96 shadow-[0_18px_50px_-28px_rgba(245,158,11,0.32)]",
    iconWrap:
      "border-amber-500/20 bg-amber-500/[0.09] text-amber-700 dark:text-amber-300",
    icon: "text-amber-700 dark:text-amber-300",
  },
  error: {
    container:
      "border-destructive/25 bg-surface-raised/96 shadow-[0_18px_50px_-28px_rgba(220,38,38,0.32)]",
    iconWrap:
      "border-destructive/20 bg-destructive/[0.07] text-destructive",
    icon: "text-destructive",
  },
};

const iconByVariant: Record<ToastVariant, typeof CheckCircle2> = {
  success: CheckCircle2,
  info: Info,
  warning: AlertTriangle,
  error: CircleAlert,
};

export function ToastCard({
  variant,
  title,
  description,
  action,
  onDismiss,
}: ToastCardProps) {
  const classes = variantClassNames[variant];
  const Icon = iconByVariant[variant];
  const compact = !description && !action;

  return (
    <Card
      role="status"
      aria-live={variant === "error" ? "assertive" : "polite"}
      data-slot="toast-card"
      className={cn(
        "relative w-[min(22rem,calc(100vw-2rem))] overflow-hidden rounded-2xl border backdrop-blur-md transition-shadow",
        classes.container,
      )}
    >
      <CardContent
        className={cn(
          "flex items-start gap-3 p-3.5",
          compact ? "min-h-0" : "min-h-0",
          onDismiss ? "pr-12" : "",
        )}
      >
        <div
          className={cn(
            "mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-full border shadow-sm",
            classes.iconWrap,
          )}
        >
          <Icon className={cn("size-4.5", classes.icon)} aria-hidden="true" />
        </div>
        <div className="min-w-0 flex-1 space-y-1">
          <CardTitle className="text-[13px] leading-5 text-foreground">
            {title}
          </CardTitle>
          {description ? (
            <CardDescription className="text-xs leading-5 text-muted-foreground">
              {description}
            </CardDescription>
          ) : null}
          {action ? (
            <div className="pt-1">
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="h-7 rounded-md border-border/70 bg-surface-raised/90 text-foreground hover:border-primary/25 hover:bg-surface-hover"
                onClick={action.onClick}
              >
                {action.label}
              </Button>
            </div>
          ) : null}
        </div>
        {onDismiss ? (
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            className="absolute right-3 top-3 rounded-full text-muted-foreground hover:bg-surface-hover hover:text-foreground"
            aria-label="Dismiss notification"
            onClick={onDismiss}
          >
            <X className="size-3.5" aria-hidden="true" />
          </Button>
        ) : null}
      </CardContent>
    </Card>
  );
}
