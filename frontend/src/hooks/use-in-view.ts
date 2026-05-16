import { useRef, useState, useEffect } from "react";

interface InViewOptions {
  /** Fraction from top of viewport where element triggers. Default 0.85. */
  threshold?: number;
  /** Stay true once triggered. Default true. */
  once?: boolean;
}

export function useInView<T extends HTMLElement = HTMLDivElement>(
  options: InViewOptions = {}
): { ref: React.RefObject<T | null>; inView: boolean } {
  const { threshold = 0.85, once = true } = options;
  const ref = useRef<T | null>(null);
  const reducedMotion =
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  const [inView, setInView] = useState(true);

  useEffect(() => {
    if (reducedMotion) return;
    if (typeof IntersectionObserver === "undefined") return;

    const el = ref.current;
    if (!el) return;

    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry.isIntersecting) {
          setInView(true);
          if (once) observer.disconnect();
        } else if (!once) {
          setInView(false);
        }
      },
      {
        rootMargin: `-${Math.round((1 - threshold) * 100)}% 0px 0px 0px`,
        threshold: 0,
      }
    );

    observer.observe(el);
    return () => observer.disconnect();
  }, [threshold, once, reducedMotion]);

  return { ref, inView };
}
