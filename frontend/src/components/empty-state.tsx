import type { LucideIcon } from "lucide-react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";

interface EmptyStateProps {
  icon: LucideIcon;
  title: string;
  description: string;
  variant?: "card" | "inline" | "narrative";
  action?: {
    label: string;
    href?: string;
    onClick?: () => void;
  };
}

export function EmptyState({ icon: Icon, title, description, variant = "card", action }: EmptyStateProps) {
  const narrative = variant === "narrative";
  const content = (
    <div className={variant === "card" ? "flex flex-col items-center justify-center py-12" : narrative ? "flex flex-col items-center justify-center px-6 py-16 sm:py-20" : "flex flex-col items-center justify-center px-4 py-10"}>
      <div className={narrative ? "flex h-14 w-14 items-center justify-center rounded-2xl bg-accent/60 text-primary" : "flex h-11 w-11 items-center justify-center rounded-xl bg-surface-recessed text-muted-foreground"}>
        <Icon className={narrative ? "h-6 w-6" : "h-5 w-5"} />
      </div>
      <p className={narrative ? "mt-5 font-display text-lg font-semibold tracking-[-0.02em] text-foreground" : "mt-4 text-sm font-semibold text-foreground"}>{title}</p>
      <p className="mt-1.5 max-w-sm text-center text-sm leading-5 text-muted-foreground">
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

  if (variant === "inline" || variant === "narrative") {
    return content;
  }

  return (
    <Card variant="quiet" className="bg-surface-recessed/45">
      <CardContent>
        {content}
      </CardContent>
    </Card>
  );
}
