import type { ComponentProps } from "react";

import { TabsContent } from "@/components/ui/tabs";
import { cn } from "@/lib/utils";

/**
 * Canonical tab panel for dashboard pages.
 *
 * Major page sections use a consistent rhythm while the base TabsContent
 * remains unopinionated for compact tabs inside cards, dialogs, and panels.
 */
export function PageTabContent({
  className,
  ...props
}: ComponentProps<typeof TabsContent>) {
  return (
    <TabsContent
      data-slot="page-tab-content"
      className={cn("space-y-6", className)}
      {...props}
    />
  );
}
