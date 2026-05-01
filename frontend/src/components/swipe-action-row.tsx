"use client";

import { useEffect, useRef, useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

const ACTION_WIDTH = 92;
const OPEN_THRESHOLD = 44;
const HORIZONTAL_LOCK_THRESHOLD = 12;

type DragState = {
  startX: number;
  startY: number;
  swiping: boolean;
  locked: boolean;
};

export function SwipeActionRow({
  actionLabel,
  actionText,
  actionIcon,
  onAction,
  children,
  className,
}: {
  actionLabel: string;
  actionText: string;
  actionIcon?: ReactNode;
  onAction: () => void;
  children: ReactNode;
  className?: string;
}) {
  const dragRef = useRef<DragState | null>(null);
  const [offset, setOffset] = useState(0);
  const [isDragging, setIsDragging] = useState(false);
  // Default to true so SSR and test environments (no matchMedia) get the
  // touch-friendly variant — non-touch desktops flip to false in the effect.
  const [isTouchDevice, setIsTouchDevice] = useState(true);

  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
      return;
    }
    const mql = window.matchMedia("(pointer: coarse)");
    setIsTouchDevice(mql.matches);
    const onChange = (event: MediaQueryListEvent) => setIsTouchDevice(event.matches);
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }, []);

  const close = () => {
    setOffset(0);
    setIsDragging(false);
    dragRef.current = null;
  };

  const open = () => {
    setOffset(ACTION_WIDTH);
    setIsDragging(false);
    dragRef.current = null;
  };

  const handleTouchStart = (event: React.TouchEvent<HTMLDivElement>) => {
    const touch = event.touches[0];
    if (!touch) return;
    dragRef.current = {
      startX: touch.clientX,
      startY: touch.clientY,
      swiping: false,
      locked: false,
    };
    setIsDragging(true);
  };

  const handleTouchMove = (event: React.TouchEvent<HTMLDivElement>) => {
    const touch = event.touches[0];
    const drag = dragRef.current;
    if (!touch || !drag) return;

    const deltaX = touch.clientX - drag.startX;
    const deltaY = touch.clientY - drag.startY;

    if (!drag.locked) {
      if (Math.abs(deltaY) > HORIZONTAL_LOCK_THRESHOLD && Math.abs(deltaY) > Math.abs(deltaX)) {
        dragRef.current = null;
        setIsDragging(false);
        return;
      }
      if (Math.abs(deltaX) < HORIZONTAL_LOCK_THRESHOLD) {
        return;
      }
      drag.locked = true;
      drag.swiping = true;
    }

    if (!drag.swiping) return;

    const nextOffset = Math.max(0, Math.min(ACTION_WIDTH, -deltaX));
    setOffset(nextOffset);
  };

  const handleTouchEnd = () => {
    if (offset >= OPEN_THRESHOLD) {
      open();
      return;
    }
    close();
  };

  const state = isTouchDevice && offset > 0 ? "open" : "closed";
  const trailingActionHidden = state === "closed";

  return (
    <div
      className={cn("group relative overflow-hidden rounded-lg", className)}
      data-swipe-state={state}
    >
      {isTouchDevice && (
        <div className="absolute inset-y-0 right-0 flex w-[92px] items-stretch justify-end">
          <Button
            aria-label={actionLabel}
            aria-hidden={trailingActionHidden}
            tabIndex={trailingActionHidden ? -1 : 0}
            className="h-full w-full rounded-none rounded-r-lg bg-amber-500 px-0 text-white hover:bg-amber-600 active:bg-amber-600"
            onClick={() => {
              close();
              onAction();
            }}
          >
            <span className="flex flex-col items-center justify-center gap-1 text-xs font-medium">
              {actionIcon}
              <span>{actionText}</span>
            </span>
          </Button>
        </div>
      )}

      <div
        data-swipe-surface="true"
        className={cn(
          "relative z-10",
          isTouchDevice && "touch-pan-y",
          isTouchDevice && !isDragging && "transition-transform duration-200 ease-out",
        )}
        style={isTouchDevice ? { transform: `translateX(-${offset}px)` } : undefined}
        onTouchStart={isTouchDevice ? handleTouchStart : undefined}
        onTouchMove={isTouchDevice ? handleTouchMove : undefined}
        onTouchEnd={isTouchDevice ? handleTouchEnd : undefined}
        onTouchCancel={isTouchDevice ? handleTouchEnd : undefined}
        onClickCapture={(event) => {
          if (offset === 0) return;
          event.preventDefault();
          event.stopPropagation();
          close();
        }}
      >
        {children}
      </div>

      <Button
        type="button"
        variant="ghost"
        size="icon-xs"
        aria-label={actionLabel}
        title={actionLabel}
        className="absolute right-2 top-2 z-20 hidden border border-border/60 bg-background text-muted-foreground shadow-sm hover:text-foreground md:inline-flex"
        onClick={(event) => {
          event.preventDefault();
          event.stopPropagation();
          close();
          onAction();
        }}
      >
        {actionIcon}
      </Button>
    </div>
  );
}
