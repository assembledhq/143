"use client";

import { useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

const ACTION_WIDTH = 92;
const OPEN_THRESHOLD = 44;
const HORIZONTAL_LOCK_THRESHOLD = 12;
const TOUCH_QUERY = "(pointer: coarse)";

type DragState = {
  startX: number;
  startY: number;
  swiping: boolean;
  locked: boolean;
};

// Resolved synchronously on first client render via useSyncExternalStore so
// non-touch desktops never paint the swipe overlay (which bleeds amber through
// the row's translucent background). SSR and jsdom (no matchMedia) get the
// touch-friendly variant — keeps tests passing without mocks.
function subscribeTouchDevice(callback: () => void): () => void {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return () => {};
  }
  const mql = window.matchMedia(TOUCH_QUERY);
  mql.addEventListener("change", callback);
  return () => mql.removeEventListener("change", callback);
}

function getTouchDeviceSnapshot(): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return true;
  }
  return window.matchMedia(TOUCH_QUERY).matches;
}

function getTouchDeviceServerSnapshot(): boolean {
  return true;
}

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
  const isTouchDevice = useSyncExternalStore(
    subscribeTouchDevice,
    getTouchDeviceSnapshot,
    getTouchDeviceServerSnapshot,
  );

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

  const swipeSurfaceProps = isTouchDevice
    ? {
        className: cn(
          "relative z-10 touch-pan-y",
          !isDragging && "transition-transform duration-200 ease-out",
        ),
        style: { transform: `translateX(-${offset}px)` },
        onTouchStart: handleTouchStart,
        onTouchMove: handleTouchMove,
        onTouchEnd: handleTouchEnd,
        onTouchCancel: handleTouchEnd,
        onClickCapture: (event: React.MouseEvent<HTMLDivElement>) => {
          if (offset === 0) return;
          event.preventDefault();
          event.stopPropagation();
          close();
        },
      }
    : { className: "relative z-10" };

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

      <div data-swipe-surface="true" {...swipeSurfaceProps}>
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
