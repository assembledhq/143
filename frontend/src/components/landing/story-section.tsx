"use client";

import { useEffect, useRef } from "react";
import { drawP80, DARK as DARK_THEME, LIGHT as LIGHT_THEME } from "./draw-p80";

interface StorySectionProps {
  isDark: boolean;
}

export default function StorySection({ isDark }: StorySectionProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const rafRef = useRef<number>(0);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    function resize() {
      const dpr = window.devicePixelRatio || 1;
      const rect = canvas!.getBoundingClientRect();
      canvas!.width = rect.width * dpr;
      canvas!.height = rect.height * dpr;
      ctx!.setTransform(dpr, 0, 0, dpr, 0, 0);
    }

    resize();
    window.addEventListener("resize", resize);

    const theme = isDark ? DARK_THEME : LIGHT_THEME;

    function draw(time: number) {
      const rect = canvas!.getBoundingClientRect();
      const w = rect.width;
      const h = rect.height;

      ctx!.clearRect(0, 0, w, h);

      // Plane slowly flies right-to-left across the canvas, looping
      const cycleDuration = 10000;
      const t = (time % cycleDuration) / cycleDuration;

      // Position: enters from right, exits left
      const planeX = w * (1.15 - t * 1.3);
      const planeY = h * 0.5 + Math.sin(t * Math.PI * 2) * h * 0.06;

      // Slight heading variation for natural movement
      const heading = Math.PI + Math.sin(t * Math.PI * 2) * 0.08;

      // Size relative to canvas
      const size = Math.min(w, h) * 0.32;

      // Fade in/out at edges
      const edgeFade =
        t < 0.08 ? t / 0.08 : t > 0.88 ? (1 - t) / 0.12 : 1;

      drawP80(ctx!, planeX, planeY, size, heading, 0.4, edgeFade * 0.8, theme);

      // Subtle contrail
      if (edgeFade > 0.3) {
        const cosH = Math.cos(heading);
        const sinH = Math.sin(heading);
        for (let i = 1; i <= 8; i++) {
          const tAlpha = edgeFade * 0.04 * (1 - i / 8);
          const tx = planeX - cosH * size * (0.9 + i * 0.2);
          const ty = planeY - sinH * size * (0.9 + i * 0.2);
          const tSize = 2 + i * 1.5;
          ctx!.fillStyle = isDark
            ? `rgba(180, 195, 220, ${tAlpha})`
            : `rgba(255, 255, 255, ${tAlpha * 1.5})`;
          ctx!.beginPath();
          ctx!.arc(tx, ty, tSize, 0, Math.PI * 2);
          ctx!.fill();
        }
      }

      rafRef.current = requestAnimationFrame(draw);
    }

    rafRef.current = requestAnimationFrame(draw);

    return () => {
      window.removeEventListener("resize", resize);
      cancelAnimationFrame(rafRef.current);
    };
  }, [isDark]);

  return (
    <section
      className="relative py-16 sm:py-20 px-6 sm:px-10 overflow-hidden"
      style={{ background: isDark ? "#0c0c14" : "#f5f7fa" }}
    >
      <div className="mx-auto max-w-4xl flex flex-col sm:flex-row items-center gap-8 sm:gap-12">
        <div className="w-full sm:w-[320px] h-[160px] flex-shrink-0">
          <canvas
            ref={canvasRef}
            style={{ width: "100%", height: "100%", display: "block" }}
          />
        </div>
        <div>
          <h3
            className={`text-lg sm:text-xl font-light tracking-tight mb-3 ${
              isDark ? "text-white/80" : "text-slate-800"
            }`}
          >
            Why 143?
          </h3>
          <p
            className={`text-sm leading-relaxed max-w-md ${
              isDark ? "text-white/40" : "text-slate-600"
            }`}
          >
            The first US jet fighter, the P-80 Shooting Star, was designed and
            built by Lockheed&apos;s Skunk Works in just 143&nbsp;days. We named
            this project after that same spirit of speed &mdash; the belief that
            a small team with the right tools can ship what seems impossible.
          </p>
        </div>
      </div>
    </section>
  );
}
