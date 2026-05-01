"use client";

import {
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

const ACTION_WIDTH = 92;
const OPEN_THRESHOLD = 44;
const HORIZONTAL_LOCK_THRESHOLD = 12;
const TOUCH_QUERY = "(pointer: coarse)";
const COMMIT_THRESHOLD_RATIO = 0.36;
const MIN_COMMIT_THRESHOLD = 140;
const COMMIT_ANIMATION_MS = 220;
// Pre-measurement fallback when a gesture starts before the row has dimensions.
// Real width is captured from offsetWidth at touchstart.
const FALLBACK_ROW_WIDTH = ACTION_WIDTH * 4;

type DragState = {
  startX: number;
  startY: number;
  width: number;
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

function vibrate(pattern: number | number[]) {
  if (typeof navigator === "undefined") return;
  // iOS Safari does not implement the Vibration API; this is a no-op there.
  if (typeof navigator.vibrate !== "function") return;
  try {
    navigator.vibrate(pattern);
  } catch (error) {
    console.error("Failed to trigger swipe haptic feedback", error);
  }
}

function commitThresholdFor(width: number) {
  return Math.max(MIN_COMMIT_THRESHOLD, width * COMMIT_THRESHOLD_RATIO);
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
  const containerRef = useRef<HTMLDivElement | null>(null);
  const dragRef = useRef<DragState | null>(null);
  const commitTimerRef = useRef<number | null>(null);
  // Mirrors isCommitted for use inside touchmove handlers, where the rendered
  // closure can lag behind rapid state transitions.
  const committedRef = useRef(false);
  const offsetRef = useRef(0);
  const [offset, setOffset] = useState(0);
  const [isDragging, setIsDragging] = useState(false);
  const [isCommitted, setIsCommitted] = useState(false);
  const isTouchDevice = useSyncExternalStore(
    subscribeTouchDevice,
    getTouchDeviceSnapshot,
    getTouchDeviceServerSnapshot,
  );

  useEffect(() => {
    return () => {
      if (commitTimerRef.current !== null) {
        window.clearTimeout(commitTimerRef.current);
      }
    };
  }, []);

  const close = () => {
    offsetRef.current = 0;
    setOffset(0);
    setIsDragging(false);
    setIsCommitted(false);
    committedRef.current = false;
    dragRef.current = null;
  };

  const open = () => {
    offsetRef.current = ACTION_WIDTH;
    setOffset(ACTION_WIDTH);
    setIsDragging(false);
    setIsCommitted(false);
    committedRef.current = false;
    dragRef.current = null;
  };

  // Slides the row fully off, fires onAction, then resets after the animation.
  // Common case: onAction unmounts the row and the unmount effect clears the
  // timer. Fallback: caller keeps the row mounted, the timer snaps offset back.
  const commitAction = (width: number) => {
    setIsDragging(false);
    offsetRef.current = width;
    setOffset(width);
    dragRef.current = null;
    committedRef.current = false;
    vibrate([15, 30, 40]);
    onAction();
    if (commitTimerRef.current !== null) {
      window.clearTimeout(commitTimerRef.current);
    }
    commitTimerRef.current = window.setTimeout(() => {
      offsetRef.current = 0;
      setOffset(0);
      setIsCommitted(false);
      commitTimerRef.current = null;
    }, COMMIT_ANIMATION_MS);
  };

  const handleTouchStart = (event: React.TouchEvent<HTMLDivElement>) => {
    const touch = event.touches[0];
    if (!touch) return;
    // Cancel a pending post-commit reset so a quick follow-up swipe isn't
    // clobbered by the trailing setTimeout firing mid-gesture.
    if (commitTimerRef.current !== null) {
      window.clearTimeout(commitTimerRef.current);
      commitTimerRef.current = null;
    }
    dragRef.current = {
      startX: touch.clientX,
      startY: touch.clientY,
      width: containerRef.current?.offsetWidth || FALLBACK_ROW_WIDTH,
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

    if (event.cancelable) {
      event.preventDefault();
    }

    const nextOffset = Math.max(0, Math.min(drag.width, -deltaX));
    offsetRef.current = nextOffset;
    setOffset(nextOffset);

    const willCommit = nextOffset >= commitThresholdFor(drag.width);
    if (willCommit !== committedRef.current) {
      committedRef.current = willCommit;
      setIsCommitted(willCommit);
      if (willCommit) {
        // Light tick when the user crosses into the auto-commit zone.
        vibrate(8);
      }
    }
  };

  const handleTouchEnd = () => {
    const width =
      dragRef.current?.width ??
      containerRef.current?.offsetWidth ??
      FALLBACK_ROW_WIDTH;
    const latestOffset = offsetRef.current;
    if (latestOffset >= commitThresholdFor(width)) {
      commitAction(width);
      return;
    }
    if (latestOffset >= OPEN_THRESHOLD) {
      open();
      return;
    }
    close();
  };

  const handleTouchCancel = () => {
    close();
  };

  const state =
    isTouchDevice && offset > 0
      ? isCommitted
        ? "committed"
        : "open"
      : "closed";
  const trailingActionHidden = state === "closed";
  const actionAreaWidth = Math.max(ACTION_WIDTH, offset);

  const swipeSurfaceProps = isTouchDevice
    ? {
        className: cn(
          "relative z-10 touch-pan-y",
          offset > 0 && "bg-background",
          !isDragging && "transition-transform duration-200 ease-out",
        ),
        style: { transform: `translateX(-${offset}px)` },
        onTouchStart: handleTouchStart,
        onTouchMove: handleTouchMove,
        onTouchEnd: handleTouchEnd,
        onTouchCancel: handleTouchCancel,
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
      ref={containerRef}
      className={cn("group relative overflow-hidden rounded-lg", className)}
      data-swipe-state={state}
    >
      {isTouchDevice && (
        <div
          className="absolute inset-y-0 right-0"
          style={{ width: `${actionAreaWidth}px` }}
        >
          <Button
            aria-label={actionLabel}
            aria-hidden={trailingActionHidden}
            tabIndex={trailingActionHidden ? -1 : 0}
            className={cn(
              "h-full w-full rounded-none rounded-r-lg px-0 text-white transition-colors duration-150",
              isCommitted
                ? "bg-amber-600 hover:bg-amber-700 active:bg-amber-700"
                : "bg-amber-500 hover:bg-amber-600 active:bg-amber-600",
            )}
            onClick={() => {
              close();
              onAction();
            }}
          >
            <span
              className={cn(
                "flex flex-col items-center justify-center gap-1 text-xs font-medium transition-transform duration-150",
                isCommitted && "scale-110",
              )}
            >
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
