import type { VariantProps } from "class-variance-authority";

import { badgeVariants } from "@/components/ui/badge";
import { StatusLabel, type StatusTone } from "@/components/status-label";
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
  const isStarting = status === "starting" || status === "recycling" || status === "queued";
  let tone: StatusTone = "neutral";
  if (status === "ready" || status === "partially_ready") tone = "success";
  else if (status === "failed" || status === "unavailable" || status === "blocked" || status === "config_invalid") tone = "destructive";
  else if (status === "outdated" || status === "capacity_blocked") tone = "warning";
  else if (isStarting) tone = "primary";
  else if (variant === "destructive") tone = "destructive";
  else if (variant === "default") tone = "success";

  return (
    <StatusLabel
      label={label ?? formatPreviewStatus(status)}
      tone={tone}
      active={isStarting}
      className={cn(className)}
    />
  );
}
