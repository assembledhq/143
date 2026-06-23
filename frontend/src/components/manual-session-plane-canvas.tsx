"use client";

import { useEffect, useRef } from "react";
import { drawP80, DARK, LIGHT, type PlaneTheme } from "@/components/landing/draw-p80";
import { usePrefersDark } from "@/hooks/use-prefers-dark";

type Plane = {
  x: number;
  y: number;
  size: number;
  speed: number;
  heading: number;
  baseY: number;
  phase: number;
  opacity: number;
  trail: Array<{ x: number; y: number }>;
};

const PLANE_COUNT = 2;

function createPlane(width: number, height: number, index: number): Plane {
  const leftToRight = index % 2 === 0;
  const y = height * (index === 0 ? 0.26 : 0.58);

  return {
    x: leftToRight ? -width * 0.18 : width * 1.18,
    y,
    size: Math.max(14, Math.min(24, width * 0.024)) * (index === 0 ? 1 : 0.82),
    speed: (leftToRight ? 0.24 : -0.18) * Math.max(0.85, Math.min(1.2, width / 900)),
    heading: leftToRight ? -0.06 : Math.PI + 0.05,
    baseY: y,
    phase: index * Math.PI * 0.7,
    opacity: index === 0 ? 0.36 : 0.24,
    trail: [],
  };
}

function subtleTheme(theme: PlaneTheme): PlaneTheme {
  return {
    ...theme,
    planeFill: (a: number) => theme.planeFill(a * 0.72),
    planeStroke: (a: number) => theme.planeStroke(a * 0.55),
    planeHighlight: (a: number) => theme.planeHighlight(a * 0.45),
    planeShadow: (a: number) => theme.planeShadow(a * 0.3),
    canopy: (a: number) => theme.canopy(a * 0.5),
    canopyEdge: (a: number) => theme.canopyEdge(a * 0.45),
    panelLine: (a: number) => theme.panelLine(a * 0.35),
    trail: (a: number) => theme.trail(a * 0.18),
  };
}

export function ManualSessionPlaneCanvas() {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const planesRef = useRef<Plane[]>([]);
  const isDark = usePrefersDark();
  const isDarkRef = useRef(isDark);

  useEffect(() => {
    isDarkRef.current = isDark;
  }, [isDark]);

  useEffect(() => {
    if (process.env.NODE_ENV === "test") {
      return;
    }

    const canvas = canvasRef.current;
    const parent = canvas?.parentElement;
    const ctx = canvas?.getContext("2d");

    if (!canvas || !parent || !ctx) {
      return;
    }

    let frameId = 0;
    let reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    let width = 0;
    let height = 0;

    const resize = () => {
      const rect = parent.getBoundingClientRect();
      width = Math.max(1, rect.width);
      height = Math.max(1, rect.height);

      const dpr = window.devicePixelRatio || 1;
      canvas.width = Math.round(width * dpr);
      canvas.height = Math.round(height * dpr);
      canvas.style.width = `${width}px`;
      canvas.style.height = `${height}px`;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

      planesRef.current = Array.from({ length: PLANE_COUNT }, (_, index) =>
        createPlane(width, height, index),
      );
    };

    const draw = (time: number) => {
      ctx.clearRect(0, 0, width, height);
      const theme = subtleTheme(isDarkRef.current ? DARK : LIGHT);

      for (const plane of planesRef.current) {
        if (!reducedMotion) {
          plane.x += plane.speed;
          plane.y = plane.baseY + Math.sin(time * 0.00045 + plane.phase) * 8;

          const exitingRight = plane.speed > 0 && plane.x > width * 1.16;
          const exitingLeft = plane.speed < 0 && plane.x < -width * 0.16;
          if (exitingRight || exitingLeft) {
            plane.x = plane.speed > 0 ? -width * 0.14 : width * 1.14;
            plane.trail = [];
          }
        }

        plane.trail.push({ x: plane.x, y: plane.y });
        while (plane.trail.length > 78) {
          plane.trail.shift();
        }

        for (let i = 1; i < plane.trail.length; i += 1) {
          const prev = plane.trail[i - 1];
          const current = plane.trail[i];
          const progress = i / plane.trail.length;

          ctx.beginPath();
          ctx.moveTo(prev.x, prev.y);
          ctx.lineTo(current.x, current.y);
          ctx.strokeStyle = theme.trail(progress * plane.opacity);
          ctx.lineWidth = 0.45 + progress * 1.3;
          ctx.lineCap = "round";
          ctx.stroke();
        }

        drawP80(ctx, plane.x, plane.y, plane.size, plane.heading, 0.58, plane.opacity, theme);
      }

      frameId = window.requestAnimationFrame(draw);
    };

    const motionQuery = window.matchMedia("(prefers-reduced-motion: reduce)");
    const onMotionChange = (event: MediaQueryListEvent) => {
      reducedMotion = event.matches;
    };
    const observer = new ResizeObserver(resize);

    resize();
    observer.observe(parent);
    motionQuery.addEventListener("change", onMotionChange);
    frameId = window.requestAnimationFrame(draw);

    return () => {
      window.cancelAnimationFrame(frameId);
      observer.disconnect();
      motionQuery.removeEventListener("change", onMotionChange);
    };
  }, []);

  return (
    <canvas
      ref={canvasRef}
      aria-hidden="true"
      data-testid="manual-session-plane-canvas"
      className="pointer-events-none absolute inset-0 h-full w-full"
    />
  );
}
