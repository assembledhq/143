import { Loader2 } from "lucide-react";
import type { VariantProps } from "class-variance-authority";

import { Badge, badgeVariants } from "@/components/ui/badge";
import { formatPreviewStatus } from "@/lib/preview-types";
import { cn } from "@/lib/utils";

type BadgeVariant = NonNullable<VariantProps<typeof badgeVariants>["variant"]>;

export function PreviewStatusBadge({
  status,
  label,
  variant,
  className,
}: {
  status: string;
  label?: string;
  variant?: BadgeVariant;
  className?: string;
}) {
  const isStarting = status === "starting";

  return (
    <Badge
      variant={variant ?? (status === "ready" || status === "partially_ready" ? "default" : "secondary")}
      className={cn(isStarting && "gap-1.5", className)}
    >
      {isStarting ? (
        <Loader2
          data-slot="preview-status-spinner"
          className="size-3 animate-spin"
          aria-hidden="true"
        />
      ) : null}
      {label ?? formatPreviewStatus(status)}
    </Badge>
  );
}
