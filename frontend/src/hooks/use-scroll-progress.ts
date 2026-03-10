import { useRef, useState, useEffect, useCallback } from "react";

interface ScrollProgressOptions {
  /** Viewport fraction where progress = 0 (1.0 = bottom). Default 0.95. */
  startViewport?: number;
  /** Viewport fraction where progress = 1 (0.0 = top). Default 0.15. */
  endViewport?: number;
}

export function useScrollProgress<T extends HTMLElement = HTMLDivElement>(
  options: ScrollProgressOptions = {}
): { ref: React.RefObject<T | null>; progress: number } {
  const { startViewport = 0.95, endViewport = 0.15 } = options;
  const ref = useRef<T | null>(null);
  const [progress, setProgress] = useState(0);

  const handleScroll = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    const rect = el.getBoundingClientRect();
    const vh = window.innerHeight;
    const startY = vh * startViewport;
    const endY = vh * endViewport;
    const raw = (startY - rect.top) / (startY - endY);
    setProgress(Math.min(1, Math.max(0, raw)));
  }, [startViewport, endViewport]);

  useEffect(() => {
    // Respect reduced motion
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      setProgress(1);
      return;
    }

    handleScroll();
    window.addEventListener("scroll", handleScroll, { passive: true });
    window.addEventListener("resize", handleScroll, { passive: true });
    return () => {
      window.removeEventListener("scroll", handleScroll);
      window.removeEventListener("resize", handleScroll);
    };
  }, [handleScroll]);

  return { ref, progress };
}
