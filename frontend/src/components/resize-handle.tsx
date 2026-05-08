"use client";

import { useCallback, useEffect, useRef } from "react";
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
  const handleRef = useRef<HTMLDivElement>(null);
  const isDragging = useRef(false);
  const lastPos = useRef(0);

  const handleMouseMove = useCallback(
    (e: MouseEvent) => {
      if (!isDragging.current) return;
      const delta = e.clientX - lastPos.current;
      lastPos.current = e.clientX;
      onResize(delta);
    },
    [onResize],
  );

  const handleMouseUp = useCallback(() => {
    isDragging.current = false;
    document.body.style.cursor = "";
    document.body.style.userSelect = "";
  }, []);

  useEffect(() => {
    document.addEventListener("mousemove", handleMouseMove);
    document.addEventListener("mouseup", handleMouseUp);
    return () => {
      document.removeEventListener("mousemove", handleMouseMove);
      document.removeEventListener("mouseup", handleMouseUp);
    };
  }, [handleMouseMove, handleMouseUp]);

  function handleMouseDown(e: React.MouseEvent) {
    e.preventDefault();
    isDragging.current = true;
    lastPos.current = e.clientX;
    document.body.style.cursor = "col-resize";
    document.body.style.userSelect = "none";
  }

  return (
    <div
      ref={handleRef}
      onMouseDown={handleMouseDown}
      role="separator"
      aria-orientation="vertical"
      data-testid={testId}
      className={cn(
        "shrink-0 relative z-10 group",
        direction === "horizontal" && "w-0 cursor-col-resize",
        className,
      )}
    >
      {/* Invisible wider hit area */}
      <div className="absolute inset-y-0 -left-1.5 w-3" />
      {/* Visible indicator on hover/drag */}
      <div className="absolute inset-y-0 -left-px w-0.5 bg-transparent group-hover:bg-primary/30 transition-colors" />
    </div>
  );
}
