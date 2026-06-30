import type { ComponentProps } from "react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export type ReleaseStage = "alpha" | "beta";

const RELEASE_STAGE_LABELS: Record<ReleaseStage, string> = {
  alpha: "Alpha",
  beta: "Beta",
};

type ReleaseStageBadgeProps = Omit<ComponentProps<typeof Badge>, "children" | "variant"> & {
  stage: ReleaseStage;
  decorative?: boolean;
};

export function ReleaseStageBadge({
  stage,
  decorative = false,
  className,
  ...props
}: ReleaseStageBadgeProps) {
  const label = RELEASE_STAGE_LABELS[stage];
  const baseClassName = cn(
    "rounded-md px-1.5 py-0.5 text-xs uppercase leading-none",
    decorative && "after:content-[attr(data-badge)]",
    className,
  );

  if (decorative) {
    return (
      <Badge
        variant="secondary"
        className={baseClassName}
        data-badge={label}
        data-release-stage={stage}
        aria-hidden="true"
        {...props}
      />
    );
  }

  return (
    <Badge
      variant="secondary"
      className={baseClassName}
      data-release-stage={stage}
      {...props}
    >
      {label}
    </Badge>
  );
}

type StageBadgeProps = Omit<ReleaseStageBadgeProps, "stage">;

export function AlphaBadge(props: StageBadgeProps) {
  return <ReleaseStageBadge stage="alpha" {...props} />;
}

export function BetaBadge(props: StageBadgeProps) {
  return <ReleaseStageBadge stage="beta" {...props} />;
}
