"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { cn } from "@/lib/utils";

interface ResizeHandleProps {
  /** Direction of resize: "horizontal" means dragging left/right */
  direction?: "horizontal";
  /** Called continuously during drag with the delta in px (positive = right/down) */
  onResize: (delta: number) => void;
  className?: string;
  testId?: string;
}

export function ResizeHandle({ direction = "horizontal", onResize, className, testId }: ResizeHandleProps) {
  const activePointerId = useRef<number | null>(null);
  const lastPos = useRef(0);
  const [isDragging, setIsDragging] = useState(false);

  const resetDocumentDragStyles = useCallback(() => {
    document.body.style.cursor = "";
    document.body.style.userSelect = "";
  }, []);

  const handlePointerMove = useCallback(
    (e: PointerEvent) => {
      if (activePointerId.current !== e.pointerId) return;

      const delta = e.clientX - lastPos.current;
      if (delta === 0) return;

      lastPos.current = e.clientX;
      onResize(delta);
    },
    [onResize],
  );

  const stopDragging = useCallback((pointerId?: number) => {
    if (pointerId !== undefined && activePointerId.current !== pointerId) return;

    activePointerId.current = null;
    setIsDragging(false);
    resetDocumentDragStyles();
  }, [resetDocumentDragStyles]);

  const handlePointerUp = useCallback((e: PointerEvent) => {
    stopDragging(e.pointerId);
  }, [stopDragging]);

  useEffect(() => {
    document.addEventListener("pointermove", handlePointerMove);
    document.addEventListener("pointerup", handlePointerUp);
    document.addEventListener("pointercancel", handlePointerUp);

    return () => {
      document.removeEventListener("pointermove", handlePointerMove);
      document.removeEventListener("pointerup", handlePointerUp);
      document.removeEventListener("pointercancel", handlePointerUp);
      activePointerId.current = null;
      resetDocumentDragStyles();
    };
  }, [handlePointerMove, handlePointerUp, resetDocumentDragStyles]);

  function handlePointerDown(e: React.PointerEvent<HTMLDivElement>) {
    if (e.button !== 0) return;

    e.preventDefault();
    activePointerId.current = e.pointerId;
    lastPos.current = e.clientX;
    setIsDragging(true);
    document.body.style.cursor = "col-resize";
    document.body.style.userSelect = "none";
    e.currentTarget.setPointerCapture?.(e.pointerId);
  }

  return (
    <div
      onPointerDown={handlePointerDown}
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize panel"
      data-testid={testId}
      data-dragging={isDragging ? "true" : "false"}
      className={cn(
        "group relative z-10 h-full shrink-0 touch-none select-none",
        direction === "horizontal" && "-mx-1.5 w-3 cursor-col-resize",
        className,
      )}
    >
      <div
        data-testid="resize-handle-rail"
        aria-hidden="true"
        className="absolute inset-y-0 left-1/2 w-px -translate-x-1/2 bg-border/45 transition-colors duration-150 group-hover:bg-border/80 group-data-[dragging=true]:bg-primary/50"
      />
      <div
        data-testid="resize-handle-grip"
        aria-hidden="true"
        className="absolute left-1/2 top-1/2 h-14 w-1.5 -translate-x-1/2 -translate-y-1/2 rounded-full bg-border/90 opacity-0 shadow-sm transition-[background-color,opacity] duration-150 group-hover:opacity-100 group-focus-visible:opacity-100 group-data-[dragging=true]:bg-primary group-data-[dragging=true]:opacity-100"
      />
    </div>
  );
}
