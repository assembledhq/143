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
const READY_HAPTIC_MS = 10;
const COMMIT_HAPTIC_PATTERN = [16, 24, 40];
const COMMIT_RESET_DELAY_MS = 200;
// Pre-measurement fallback when a gesture starts before the row has dimensions.
// Real width is captured from offsetWidth at touchstart.
const FALLBACK_ROW_WIDTH = ACTION_WIDTH * 4;

type DragState = {
  startX: number;
  startY: number;
  startOffset: number;
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

function isPromiseLike(value: unknown): value is PromiseLike<unknown> {
  return (
    (typeof value === "object" || typeof value === "function") &&
    value !== null &&
    typeof (value as PromiseLike<unknown>).then === "function"
  );
}

export function SwipeActionRow({
  actionLabel,
  actionText,
  actionIcon,
  onAction,
  children,
  className,
  desktopActionVisibility = "always",
}: {
  actionLabel: string;
  actionText: string;
  actionIcon?: ReactNode;
  onAction: () => void | Promise<unknown>;
  children: ReactNode;
  className?: string;
  desktopActionVisibility?: "always" | "hover";
}) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const dragRef = useRef<DragState | null>(null);
  const commitTimerRef = useRef<number | null>(null);
  const readyHapticPlayedRef = useRef(false);
  // Mirrors isCommitted for use inside touchmove handlers, where the rendered
  // closure can lag behind rapid state transitions.
  const committedRef = useRef(false);
  const offsetRef = useRef(0);
  const [offset, setOffset] = useState(0);
  const [isDragging, setIsDragging] = useState(false);
  const [isCommitted, setIsCommitted] = useState(false);
  const [gestureWidth, setGestureWidth] = useState(FALLBACK_ROW_WIDTH);
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

  const clearCommitTimer = () => {
    if (commitTimerRef.current !== null) {
      window.clearTimeout(commitTimerRef.current);
      commitTimerRef.current = null;
    }
  };

  const scheduleClose = () => {
    clearCommitTimer();
    commitTimerRef.current = window.setTimeout(() => {
      commitTimerRef.current = null;
      close();
    }, COMMIT_RESET_DELAY_MS);
  };

  const close = () => {
    clearCommitTimer();
    offsetRef.current = 0;
    setOffset(0);
    setIsDragging(false);
    setIsCommitted(false);
    committedRef.current = false;
    readyHapticPlayedRef.current = false;
    dragRef.current = null;
  };

  const open = () => {
    offsetRef.current = ACTION_WIDTH;
    setOffset(ACTION_WIDTH);
    setIsDragging(false);
    setIsCommitted(false);
    committedRef.current = false;
    readyHapticPlayedRef.current = false;
    dragRef.current = null;
  };

  const handleActionPromise = (
    result: void | Promise<unknown>,
    {
      onResolve,
      onReject,
    }: {
      onResolve?: () => void;
      onReject?: () => void;
    } = {},
  ) => {
    if (!isPromiseLike(result)) {
      onResolve?.();
      return;
    }

    void result.then(
      () => {
        onResolve?.();
      },
      (error) => {
        onReject?.();
        console.error("Swipe action failed", error);
      },
    );
  };

  // Slides the row fully off, fires onAction, then resets after the animation.
  const commitAction = (width: number) => {
    setIsDragging(false);
    offsetRef.current = width;
    setOffset(width);
    setIsCommitted(true);
    dragRef.current = null;
    committedRef.current = true;
    readyHapticPlayedRef.current = false;
    vibrate(COMMIT_HAPTIC_PATTERN);
    clearCommitTimer();
    try {
      handleActionPromise(onAction(), {
        onResolve: scheduleClose,
        onReject: close,
      });
    } catch (error) {
      close();
      throw error;
    }
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
    const width = containerRef.current?.offsetWidth || FALLBACK_ROW_WIDTH;
    setGestureWidth(width);
    dragRef.current = {
      startX: touch.clientX,
      startY: touch.clientY,
      startOffset: offsetRef.current,
      width,
      swiping: false,
      locked: false,
    };
    readyHapticPlayedRef.current = false;
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

    const nextOffset = Math.max(0, Math.min(drag.width, drag.startOffset - deltaX));
    offsetRef.current = nextOffset;
    setOffset(nextOffset);

    const willCommit = nextOffset >= commitThresholdFor(drag.width);
    if (willCommit !== committedRef.current) {
      committedRef.current = willCommit;
      setIsCommitted(willCommit);
      if (willCommit && !readyHapticPlayedRef.current) {
        // One light pulse signals that release will archive; avoid replaying it
        // if the user scrubs back and forth near the threshold.
        readyHapticPlayedRef.current = true;
        vibrate(READY_HAPTIC_MS);
      }
    }
  };

  const handleTouchEnd = () => {
    const drag = dragRef.current;
    const width =
      drag?.width ??
      containerRef.current?.offsetWidth ??
      FALLBACK_ROW_WIDTH;
    const latestOffset = offsetRef.current;
    if (drag && !drag.locked && drag.startOffset >= OPEN_THRESHOLD) {
      close();
      return;
    }
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
  const actionAreaWidth = trailingActionHidden ? 0 : Math.max(ACTION_WIDTH, offset);
  const commitThreshold = commitThresholdFor(gestureWidth);
  const swipeProgress = Math.max(0, Math.min(1, offset / commitThreshold));
  const progressFill = trailingActionHidden ? 0 : Math.max(0.18, swipeProgress);
  const actionHint = isCommitted
    ? `Release to ${actionText.toLowerCase()}`
    : "Keep swiping";

  const swipeSurfaceProps = isTouchDevice
    ? {
        className: cn(
          "relative z-10 bg-background touch-pan-y",
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
          className="absolute inset-y-0 right-0 overflow-hidden rounded-r-lg"
          style={{ width: `${actionAreaWidth}px` }}
        >
          <div
            aria-hidden="true"
            className={cn(
              "absolute inset-0 rounded-r-lg shadow-[inset_1px_0_0_rgba(255,255,255,0.18)] transition-colors duration-150 ease-out",
              isCommitted ? "bg-amber-700" : "bg-amber-500",
            )}
          />
          <div
            aria-hidden="true"
            className={cn(
              "absolute inset-y-0 right-0 rounded-r-lg transition-all duration-150 ease-out",
              isCommitted ? "bg-amber-800/90" : "bg-amber-600/88",
            )}
            style={{ width: `${progressFill * 100}%` }}
          />
          <Button
            variant="ghost"
            aria-label={actionLabel}
            aria-hidden={trailingActionHidden}
            tabIndex={trailingActionHidden ? -1 : 0}
            className="relative h-full w-full rounded-none rounded-r-lg bg-transparent px-0 text-white shadow-none hover:bg-transparent hover:text-white active:bg-transparent"
            onClick={() => {
              close();
              try {
                handleActionPromise(onAction());
              } catch (error) {
                throw error;
              }
            }}
          >
            <span className="relative flex h-full w-full flex-col items-center justify-center gap-0.5 px-4 text-center">
              <span className="text-sm font-semibold tracking-[0.01em] text-white whitespace-nowrap">
                {actionText}
              </span>
              <span className="min-h-[0.75rem] text-xs font-medium tracking-[0.04em] text-white/80 whitespace-nowrap">
                {actionHint}
              </span>
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
        className={cn(
          "absolute right-2 top-2 z-20 hidden border border-border/60 bg-background text-muted-foreground shadow-sm hover:text-foreground md:inline-flex",
          desktopActionVisibility === "hover" &&
            "md:opacity-0 md:transition-opacity md:duration-150 md:group-hover:opacity-100 md:focus-visible:opacity-100",
        )}
        onClick={(event) => {
          event.preventDefault();
          event.stopPropagation();
          close();
          try {
            handleActionPromise(onAction());
          } catch (error) {
            throw error;
          }
        }}
      >
        {actionIcon}
      </Button>
    </div>
  );
}
