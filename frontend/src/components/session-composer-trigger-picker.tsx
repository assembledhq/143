"use client";

import { useEffect, useMemo } from "react";
import { createPortal } from "react-dom";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";

export type TriggerPickerItem = {
  id: string;
  primary: string;
  secondary?: string;
  icon?: React.ReactNode;
};

export type TriggerPickerGroup = {
  id: string;
  label: string;
  items: TriggerPickerItem[];
};

export type TriggerPickerPosition = {
  left: number;
  top: number;
  width: number;
  maxHeight: number;
  side: "top" | "bottom";
};

type TriggerPickerProps = {
  open: boolean;
  position: TriggerPickerPosition | null;
  groups: TriggerPickerGroup[];
  loading?: boolean;
  emptyLabel?: string;
  selectedIndex: number;
  onSelectedIndexChange: (index: number) => void;
  onSelect: (item: TriggerPickerItem, group: TriggerPickerGroup) => void;
  testId?: string;
};

// SessionComposerTriggerPicker is the shared overlay used by both the @ mention
// and `/` slash-command triggers in the session composer. It is intentionally
// dumb: it owns layout, keyboard highlighting, and click-to-insert; the parent
// owns the data, the trigger detection, and the keyboard navigation contract
// (the parent decides what arrow keys mean and forwards the result via
// `onSelectedIndexChange`).
export function SessionComposerTriggerPicker({
  open,
  position,
  groups,
  loading,
  emptyLabel,
  selectedIndex,
  onSelectedIndexChange,
  onSelect,
  testId = "trigger-picker-overlay",
}: TriggerPickerProps) {
  const flattened = useMemo(() => flattenGroups(groups), [groups]);
  const totalItems = flattened.length;

  // Clamp the selected index whenever the result set shrinks (e.g. the user
  // typed a more restrictive query). Without this guard, the parent's
  // wraparound math could leave the highlight pointing past the last item.
  useEffect(() => {
    if (totalItems === 0) {
      if (selectedIndex !== 0) onSelectedIndexChange(0);
      return;
    }
    if (selectedIndex >= totalItems) {
      onSelectedIndexChange(0);
    }
  }, [selectedIndex, totalItems, onSelectedIndexChange]);

  if (!open || !position || typeof document === "undefined") {
    return null;
  }

  return createPortal(
    <Card
      className="fixed z-50 overflow-hidden border-border/70 bg-popover shadow-xl"
      data-side={position.side}
      data-testid={testId}
      style={{
        left: position.left,
        top: position.top,
        width: position.width,
      }}
    >
      <CardContent className="p-2">
        {loading && (
          <p className="px-2 py-1 text-xs text-muted-foreground">Loading matches…</p>
        )}
        {!loading && totalItems === 0 && (
          <p className="px-2 py-1 text-xs text-muted-foreground">{emptyLabel ?? "No matches"}</p>
        )}
        {!loading && totalItems > 0 && (
          <div
            className="space-y-2 overflow-y-auto"
            style={{ maxHeight: position.maxHeight }}
            aria-label="Trigger suggestions"
            role="listbox"
          >
            {groups.map((group) => {
              if (group.items.length === 0) return null;
              return (
                <div key={group.id} className="space-y-1">
                  <div className="px-2 text-xs font-medium uppercase tracking-[0.14em] text-muted-foreground">
                    {group.label}
                  </div>
                  {group.items.map((item) => {
                    const flatIndex = flattened.findIndex((entry) => entry.group.id === group.id && entry.item.id === item.id);
                    const isSelected = flatIndex === selectedIndex;
                    return (
                      <Button
                        key={`${group.id}:${item.id}`}
                        type="button"
                        variant="ghost"
                        aria-selected={isSelected}
                        className={cn(
                          "flex h-auto w-full items-center justify-start gap-2 rounded-lg px-2 py-2 text-left",
                          isSelected && "bg-accent text-accent-foreground",
                        )}
                        onMouseDown={(event) => event.preventDefault()}
                        onClick={() => onSelect(item, group)}
                      >
                        {item.icon}
                        <span className="min-w-0 flex-1 truncate text-xs">
                          <span className="font-medium">{item.primary}</span>
                          {item.secondary && (
                            <span className="ml-2 text-muted-foreground">{item.secondary}</span>
                          )}
                        </span>
                      </Button>
                    );
                  })}
                </div>
              );
            })}
          </div>
        )}
      </CardContent>
    </Card>,
    document.body,
  );
}

// flattenGroups produces the flat array used for keyboard navigation and to
// resolve a selectedIndex back to the (group, item) pair on insert. Keeping
// this in one place ensures arrow keys and click handlers always agree about
// "which item is item N?".
export function flattenGroups(groups: TriggerPickerGroup[]): { group: TriggerPickerGroup; item: TriggerPickerItem }[] {
  const out: { group: TriggerPickerGroup; item: TriggerPickerItem }[] = [];
  for (const group of groups) {
    for (const item of group.items) {
      out.push({ group, item });
    }
  }
  return out;
}
