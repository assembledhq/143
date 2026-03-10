"use client";

import { useEffect, useRef } from "react";
import {
  hudText,
  drawCRTGrain,
  drawVignette,
  NOISE_BLIPS,
} from "./canvas-utils";

interface RadarCanvasProps {
  isDark: boolean;
  className?: string;
}

const CYCLE_DURATION = 8000; // 8 seconds per full loop

export default function RadarCanvas({ isDark, className }: RadarCanvasProps) {
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

    function draw(time: number) {
      const rect = canvas!.getBoundingClientRect();
      const w = rect.width;
      const h = rect.height;

      ctx!.clearRect(0, 0, w, h);
      drawRadar(ctx!, w, h, time);
      rafRef.current = requestAnimationFrame(draw);
    }

    rafRef.current = requestAnimationFrame(draw);

    return () => {
      window.removeEventListener("resize", resize);
      cancelAnimationFrame(rafRef.current);
    };
  }, [isDark]);

  return (
    <canvas
      ref={canvasRef}
      className={className}
      style={{ width: "100%", height: "100%", display: "block" }}
    />
  );
}

// ── Radar drawing ────────────────────────────────────────────

function drawRadar(
  ctx: CanvasRenderingContext2D,
  w: number,
  h: number,
  time: number,
) {
  const p = (time % CYCLE_DURATION) / CYCLE_DURATION;

  // Fade out at end of cycle for clean loop
  const cycleFade = p > 0.88 ? (1 - p) / 0.12 : Math.min(1, p / 0.05);

  // CRT background
  ctx.fillStyle = "#020802";
  ctx.fillRect(0, 0, w, h);

  const cx = w * 0.5,
    cy = h * 0.46;
  const r = Math.min(w, h) * 0.36;

  // Scope bg with slight gradient
  const scopeGrad = ctx.createRadialGradient(cx, cy, 0, cx, cy, r);
  scopeGrad.addColorStop(0, "#081808");
  scopeGrad.addColorStop(1, "#041004");
  ctx.fillStyle = scopeGrad;
  ctx.beginPath();
  ctx.arc(cx, cy, r, 0, Math.PI * 2);
  ctx.fill();

  // Range rings with labels
  ctx.lineWidth = 1;
  for (let i = 1; i <= 4; i++) {
    const ringR = r * (i / 4);
    ctx.strokeStyle = `rgba(0, 180, 60, ${i === 2 ? 0.18 : 0.1})`;
    ctx.beginPath();
    ctx.arc(cx, cy, ringR, 0, Math.PI * 2);
    ctx.stroke();
    ctx.fillStyle = "rgba(0, 180, 60, 0.25)";
    ctx.font = "10px monospace";
    ctx.textAlign = "left";
    ctx.textBaseline = "middle";
    ctx.fillText(`${i * 50}`, cx + ringR + 3, cy);
  }

  // Compass ticks and labels around rim
  ctx.fillStyle = "rgba(0, 180, 60, 0.3)";
  ctx.font = "11px monospace";
  ctx.textAlign = "center";
  ctx.textBaseline = "middle";
  const labels = ["N", "E", "S", "W"];
  for (let deg = 0; deg < 360; deg += 10) {
    const a = ((deg - 90) * Math.PI) / 180;
    const isMajor = deg % 90 === 0;
    const isMinor = deg % 30 === 0;
    const tickInner = r * (isMajor ? 0.92 : isMinor ? 0.95 : 0.97);
    const tickOuter = r * 0.99;

    ctx.strokeStyle = `rgba(0, 180, 60, ${isMajor ? 0.3 : isMinor ? 0.18 : 0.08})`;
    ctx.lineWidth = isMajor ? 1.5 : 1;
    ctx.beginPath();
    ctx.moveTo(cx + Math.cos(a) * tickInner, cy + Math.sin(a) * tickInner);
    ctx.lineTo(cx + Math.cos(a) * tickOuter, cy + Math.sin(a) * tickOuter);
    ctx.stroke();

    if (isMajor) {
      const labelR = r * 0.86;
      ctx.fillText(
        labels[deg / 90],
        cx + Math.cos(a) * labelR,
        cy + Math.sin(a) * labelR,
      );
    }
  }

  // Crosshairs
  ctx.strokeStyle = "rgba(0, 180, 60, 0.07)";
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(cx - r, cy);
  ctx.lineTo(cx + r, cy);
  ctx.moveTo(cx, cy - r);
  ctx.lineTo(cx, cy + r);
  ctx.stroke();

  // Diagonal crosshairs
  for (const a of [Math.PI / 4, -Math.PI / 4]) {
    ctx.beginPath();
    ctx.moveTo(cx + Math.cos(a) * r, cy + Math.sin(a) * r);
    ctx.lineTo(cx - Math.cos(a) * r, cy - Math.sin(a) * r);
    ctx.stroke();
  }

  // Sweep line with fading trail
  const sweepAngle = (time * 0.0008) % (Math.PI * 2);
  for (let i = 0; i < 22; i++) {
    const a = sweepAngle - (i / 22) * Math.PI * 0.4;
    const alpha = (1 - i / 22) * 0.28;
    ctx.strokeStyle = `rgba(0, 255, 80, ${alpha})`;
    ctx.lineWidth = 1.5;
    ctx.beginPath();
    ctx.moveTo(cx, cy);
    ctx.lineTo(cx + Math.cos(a) * r, cy + Math.sin(a) * r);
    ctx.stroke();
  }
  // Bright sweep
  ctx.strokeStyle = "rgba(0, 255, 80, 0.7)";
  ctx.lineWidth = 2;
  ctx.beginPath();
  ctx.moveTo(cx, cy);
  ctx.lineTo(cx + Math.cos(sweepAngle) * r, cy + Math.sin(sweepAngle) * r);
  ctx.stroke();

  // Center dot
  ctx.fillStyle = "rgba(0, 200, 60, 0.6)";
  ctx.beginPath();
  ctx.arc(cx, cy, 3, 0, Math.PI * 2);
  ctx.fill();

  // Noise blips
  for (const nb of NOISE_BLIPS) {
    const na = (sweepAngle + nb.a) % (Math.PI * 2);
    const age =
      ((time * 0.0008 - nb.a + Math.PI * 4) % (Math.PI * 2)) / (Math.PI * 2);
    if (age < 0.3) {
      const nbAlpha = (1 - age / 0.3) * 0.12;
      ctx.fillStyle = `rgba(0, 255, 80, ${nbAlpha})`;
      ctx.beginPath();
      ctx.arc(
        cx + Math.cos(na) * r * nb.d,
        cy + Math.sin(na) * r * nb.d,
        2,
        0,
        Math.PI * 2,
      );
      ctx.fill();
    }
  }

  // Signal blips — multiple types appearing across the production surface
  const signals = [
    {
      angle: -Math.PI * 0.3,
      dist: 0.62,
      label: "BUG",
      detail: "TypeError: null ref",
      color: "255, 60, 40",
      threshold: 0.15,
    },
    {
      angle: Math.PI * 0.15,
      dist: 0.45,
      label: "ISSUE",
      detail: "LIN-342: auth flow",
      color: "255, 180, 40",
      threshold: 0.3,
    },
    {
      angle: -Math.PI * 0.7,
      dist: 0.55,
      label: "PROJECT",
      detail: "DB migration v3",
      color: "100, 140, 255",
      threshold: 0.45,
    },
    {
      angle: Math.PI * 0.6,
      dist: 0.38,
      label: "TECH DEBT",
      detail: "deprecated API calls",
      color: "180, 120, 255",
      threshold: 0.6,
    },
    {
      angle: -Math.PI * 0.05,
      dist: 0.75,
      label: "SUPPORT",
      detail: '"login broken on mobile"',
      color: "255, 140, 80",
      threshold: 0.75,
    },
  ];

  for (const sig of signals) {
    if (p <= sig.threshold) continue;
    const ba = Math.min(1, (p - sig.threshold) / 0.12) * cycleFade;
    const bx = cx + Math.cos(sig.angle) * r * sig.dist;
    const by = cy + Math.sin(sig.angle) * r * sig.dist;

    // Glow
    const glow = ctx.createRadialGradient(bx, by, 0, bx, by, 16);
    glow.addColorStop(0, `rgba(${sig.color}, ${ba * 0.6})`);
    glow.addColorStop(1, `rgba(${sig.color}, 0)`);
    ctx.fillStyle = glow;
    ctx.beginPath();
    ctx.arc(bx, by, 16, 0, Math.PI * 2);
    ctx.fill();

    // Dot
    const blink =
      Math.sin(time * 0.005 + sig.threshold * 10) > 0 ? 1 : 0.4;
    ctx.fillStyle = `rgba(${sig.color}, ${ba * blink})`;
    ctx.beginPath();
    ctx.arc(bx, by, 4, 0, Math.PI * 2);
    ctx.fill();

    // Label
    if (p > sig.threshold + 0.06) {
      const la = Math.min(1, (p - sig.threshold - 0.06) / 0.08) * cycleFade;
      ctx.fillStyle = `rgba(${sig.color}, ${la * 0.9})`;
      ctx.font = "bold 11px monospace";
      ctx.textAlign = "left";
      ctx.textBaseline = "middle";
      ctx.fillText(sig.label, bx + 14, by - 6);
      ctx.fillStyle = `rgba(0, 220, 80, ${la * 0.5})`;
      ctx.font = "10px monospace";
      ctx.fillText(sig.detail, bx + 14, by + 8);
    }
  }

  // Scope border
  ctx.strokeStyle = "rgba(0, 180, 60, 0.3)";
  ctx.lineWidth = 2;
  ctx.beginPath();
  ctx.arc(cx, cy, r, 0, Math.PI * 2);
  ctx.stroke();

  // CRT grain + vignette
  drawCRTGrain(ctx, w, h, time, 0.025);
  drawVignette(ctx, w, h, 0.5);

  // Corner data blocks
  const signalCount = signals.filter((s) => p > s.threshold).length;
  hudText(ctx, 24, 22, "PRODUCTION SURFACE", 0.6 * cycleFade, 14);
  hudText(
    ctx,
    24,
    40,
    "SENTRY \u2022 LINEAR \u2022 SUPPORT \u2022 ROADMAP",
    0.25 * cycleFade,
    10,
  );
  hudText(
    ctx,
    w - 24,
    22,
    signalCount > 0 ? `${signalCount} SIGNALS` : "MONITORING",
    signalCount > 0 ? 0.6 * cycleFade : 0.3 * cycleFade,
    10,
    "right",
  );

  // Bottom status bar
  ctx.fillStyle = "rgba(0, 180, 60, 0.08)";
  ctx.fillRect(0, h - 32, w, 32);
  hudText(ctx, 24, h - 16, "UPTIME 99.97%", 0.3 * cycleFade, 10);
  hudText(
    ctx,
    w * 0.35,
    h - 16,
    "LATENCY P99 142ms",
    0.3 * cycleFade,
    10,
  );
  hudText(ctx, w * 0.65, h - 16, "REQUESTS 14.2K/s", 0.3 * cycleFade, 10);
}
