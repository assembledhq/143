import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

type PageContainerSize = "narrow" | "default" | "wide" | "full";

const sizeClassMap: Record<PageContainerSize, string> = {
  narrow: "max-w-[1200px]",
  default: "max-w-[1200px]",
  wide: "max-w-[1200px]",
  full: "max-w-none",
};

interface PageContainerProps {
  children: ReactNode;
  size?: PageContainerSize;
  className?: string;
}

export function PageContainer({ children, size = "default", className }: PageContainerProps) {
  return (
    <div
      data-slot="page-container"
      data-size={size}
      className={cn("w-full mx-auto", sizeClassMap[size], className)}
    >
      {children}
    </div>
  );
}
