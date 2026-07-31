import type { ReactNode } from "react";
import { Info } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  TableCell,
  TableFooter,
  TableHead,
  TableRow,
} from "@/components/ui/table";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

export interface DataTableSummaryCell {
  content: ReactNode;
  className?: string;
  ariaLabel?: string;
}

export interface DataTableSummaryRowProps {
  label?: ReactNode;
  description: string;
  cells: DataTableSummaryCell[];
  className?: string;
}

/**
 * Displays server-provided aggregates aligned with their table columns.
 *
 * Callers are responsible for supplying correctly scoped values. In
 * particular, rates and medians should be computed over the full filtered
 * result set rather than derived from the rows currently rendered.
 *
 * The row renders one leading header cell plus one cell per `cells` entry, so
 * `cells.length` must equal the table's column count minus one. A shorter or
 * longer list silently misaligns the summary with the columns above it.
 */
export function DataTableSummaryRow({
  label = "Overall",
  description,
  cells,
  className,
}: DataTableSummaryRowProps) {
  return (
    <TableFooter>
      <TableRow className={cn("hover:bg-transparent", className)}>
        <TableHead
          scope="row"
          className="h-auto py-2.5 text-foreground"
        >
          <span className="inline-flex items-center gap-1.5">
            {label}
            <TooltipProvider delayDuration={150}>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-compact"
                    className="rounded-full text-muted-foreground hover:text-foreground"
                    aria-label="About this summary"
                  >
                    {/* `size-*` (not `h-*`/`w-*`) so the button's
                        `[&_svg:not([class*='size-'])]:size-4` default backs off. */}
                    <Info className="size-3.5" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent side="top" sideOffset={6} className="max-w-72 leading-5">
                  {description}
                </TooltipContent>
              </Tooltip>
            </TooltipProvider>
          </span>
        </TableHead>
        {cells.map((cell, index) => (
          <TableCell
            key={index}
            aria-label={cell.ariaLabel}
            className={cn("font-medium tabular-nums", cell.className)}
          >
            {cell.content}
          </TableCell>
        ))}
      </TableRow>
    </TableFooter>
  );
}
