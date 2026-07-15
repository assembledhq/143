"use client";

import type { ReactNode } from "react";
import { cn } from "@/lib/utils";
import { Card, CardContent } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

export type ResponsiveResourceListColumn<TItem> = {
  id: string;
  header: ReactNode;
  className?: string;
  cellClassName?: string;
  render: (item: TItem) => ReactNode;
};

type ResponsiveResourceListProps<TItem> = {
  items: TItem[];
  getItemKey: (item: TItem) => string;
  columns: ResponsiveResourceListColumn<TItem>[];
  renderMobileItem: (item: TItem) => ReactNode;
  emptyState: ReactNode;
  ariaLabel: string;
  className?: string;
};

export function ResponsiveResourceList<TItem>({
  items,
  getItemKey,
  columns,
  renderMobileItem,
  emptyState,
  ariaLabel,
  className,
}: ResponsiveResourceListProps<TItem>) {
  if (items.length === 0) {
    return (
      <Card variant="quiet" className={cn("bg-surface-recessed/45", className)}>
        <CardContent className="px-4 py-10 text-center text-sm text-muted-foreground">
          {emptyState}
        </CardContent>
      </Card>
    );
  }

  return (
    <Card className={cn("overflow-hidden border-border/80 bg-card", className)}>
      <CardContent className="p-0">
        <div className="hidden md:block">
          <Table aria-label={ariaLabel}>
            <TableHeader>
              <TableRow>
                {columns.map((column) => (
                  <TableHead key={column.id} className={column.className}>
                    {column.header}
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((item) => (
                <TableRow key={getItemKey(item)}>
                  {columns.map((column) => (
                    <TableCell key={column.id} className={column.cellClassName}>
                      {column.render(item)}
                    </TableCell>
                  ))}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>

        <div className="divide-y divide-border/60 md:hidden">
          {items.map((item) => (
            <div key={getItemKey(item)}>{renderMobileItem(item)}</div>
          ))}
        </div>
      </CardContent>
    </Card>
  );
}
