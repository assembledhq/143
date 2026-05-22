import type { LucideIcon } from "lucide-react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";

interface EmptyStateProps {
  icon: LucideIcon;
  title: string;
  description: string;
  variant?: "card" | "inline";
  action?: {
    label: string;
    href?: string;
    onClick?: () => void;
  };
}

export function EmptyState({ icon: Icon, title, description, variant = "card", action }: EmptyStateProps) {
  const content = (
    <div className={variant === "card" ? "flex flex-col items-center justify-center py-12" : "flex flex-col items-center justify-center px-4 py-10"}>
      <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted/50 dark:bg-white/5 ring-1 ring-border/50">
        <Icon className="h-6 w-6 text-muted-foreground/70" />
      </div>
      <p className="mt-4 text-sm font-semibold text-foreground">{title}</p>
      <p className="mt-1.5 max-w-xs text-center text-xs text-muted-foreground/80">
        {description}
      </p>
      {action?.href ? (
        <Button variant="outline" size="sm" className="mt-4" asChild>
          <Link href={action.href}>{action.label}</Link>
        </Button>
      ) : null}
      {action?.onClick ? (
        <Button type="button" variant="outline" size="sm" className="mt-4" onClick={action.onClick}>
          {action.label}
        </Button>
      ) : null}
    </div>
  );

  if (variant === "inline") {
    return content;
  }

  return (
    <Card>
      <CardContent>
        {content}
      </CardContent>
    </Card>
  );
}
