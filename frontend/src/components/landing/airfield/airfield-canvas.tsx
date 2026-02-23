"use client";

import { useEffect, useRef } from "react";
import { ZONES, getActiveZone, getZoneProgress } from "./zones";
import { drawP80, drawP80Side } from "../draw-p80";

interface AirfieldCanvasProps {
  progress: number;
  isDark: boolean;
}

export default function AirfieldCanvas({ progress, isDark }: AirfieldCanvasProps) {
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
      const w = canvas!.getBoundingClientRect().width;
      const h = canvas!.getBoundingClientRect().height;
      const activeZone = getActiveZone(progress);
      const zp = activeZone >= 0 ? getZoneProgress(progress, activeZone) : 0;

      ctx!.clearRect(0, 0, w, h);

      switch (activeZone) {
        case 0: drawRadarRoom(ctx!, w, h, zp, time); break;
        case 1: drawScramble(ctx!, w, h, zp, time); break;
        case 2: drawCockpitLaunch(ctx!, w, h, zp, time); break;
        case 3: drawLockOn(ctx!, w, h, zp, time); break;
        case 4: drawNeutralized(ctx!, w, h, zp, time); break;
        case 5: drawReturnToBase(ctx!, w, h, zp, time); break;
        default: drawRadarRoom(ctx!, w, h, 0, time); break;
      }

      // Overlay: zone info panel + progress bar (drawn last, on top of everything)
      drawZoneInfoPanel(ctx!, w, h, activeZone, zp, time);
      drawProgressBar(ctx!, w, h, activeZone, progress, time);

      rafRef.current = requestAnimationFrame(draw);
    }

    rafRef.current = requestAnimationFrame(draw);

    return () => {
      window.removeEventListener("resize", resize);
      cancelAnimationFrame(rafRef.current);
    };
  }, [progress, isDark]);

  return (
    <canvas
      ref={canvasRef}
      style={{ width: "100%", height: "100%", display: "block" }}
    />
  );
}

// ── Shared drawing utilities ─────────────────────────────────

function hudText(
  ctx: CanvasRenderingContext2D,
  x: number, y: number, text: string,
  alpha = 1, size = 14, align: CanvasTextAlign = "left",
) {
  ctx.fillStyle = `rgba(0, 255, 100, ${alpha})`;
  ctx.font = `bold ${size}px monospace`;
  ctx.textAlign = align;
  ctx.textBaseline = "middle";
  ctx.fillText(text, x, y);
}

/** Canvas-rendered zone info panel — replaces the old HTML ZoneCard */
function drawZoneInfoPanel(
  ctx: CanvasRenderingContext2D,
  w: number, h: number,
  activeZone: number,
  _zp: number,
  time: number,
) {
  if (activeZone < 0 || activeZone >= ZONES.length) return;
  const zone = ZONES[activeZone];

  const panelX = w * 0.03;
  const panelY = h * 0.72;
  const panelW = Math.min(320, w * 0.28);
  const panelH = 80;

  // Glass panel background
  ctx.save();
  ctx.fillStyle = "rgba(2, 8, 4, 0.65)";
  ctx.beginPath();
  ctx.roundRect(panelX, panelY, panelW, panelH, 4);
  ctx.fill();

  // Left accent bar (green, pulsing slightly)
  const pulse = 0.6 + 0.15 * Math.sin(time * 0.003);
  ctx.fillStyle = `rgba(0, 255, 100, ${pulse})`;
  ctx.fillRect(panelX, panelY, 2, panelH);

  // Top border line (subtle)
  ctx.strokeStyle = "rgba(0, 255, 100, 0.12)";
  ctx.lineWidth = 0.5;
  ctx.beginPath();
  ctx.moveTo(panelX, panelY);
  ctx.lineTo(panelX + panelW, panelY);
  ctx.stroke();

  // Step number — small mono label
  ctx.fillStyle = "rgba(0, 255, 100, 0.5)";
  ctx.font = "bold 11px monospace";
  ctx.textAlign = "left";
  ctx.textBaseline = "middle";
  ctx.fillText(`STEP ${zone.number}`, panelX + 14, panelY + 18);

  // Zone name — larger, brighter
  ctx.fillStyle = "rgba(0, 255, 100, 0.85)";
  ctx.font = "bold 16px monospace";
  ctx.fillText(zone.name.toUpperCase(), panelX + 14, panelY + 38);

  // Description — softer, smaller, wrapping
  ctx.fillStyle = "rgba(0, 255, 100, 0.35)";
  ctx.font = "12px monospace";
  const desc = zone.description;
  const maxLineW = panelW - 28;
  const words = desc.split(" ");
  let line = "";
  let lineY = panelY + 58;
  for (const word of words) {
    const test = line ? `${line} ${word}` : word;
    if (ctx.measureText(test).width > maxLineW && line) {
      ctx.fillText(line, panelX + 14, lineY);
      line = word;
      lineY += 15;
    } else {
      line = test;
    }
  }
  if (line) ctx.fillText(line, panelX + 14, lineY);

  ctx.restore();
}

/** Canvas-rendered slim progress bar with tick marks */
function drawProgressBar(
  ctx: CanvasRenderingContext2D,
  w: number, h: number,
  activeZone: number,
  progress: number,
  time: number,
) {
  const barY = h - 32;
  const barX = w * 0.3;
  const barW = w * 0.4;
  const barH = 2;

  ctx.save();

  // Track
  ctx.fillStyle = "rgba(0, 255, 100, 0.08)";
  ctx.fillRect(barX, barY, barW, barH);

  // Filled portion
  const fillW = barW * Math.min(1, progress);
  ctx.fillStyle = "rgba(0, 255, 100, 0.35)";
  ctx.fillRect(barX, barY, fillW, barH);

  // Bright leading edge
  if (progress > 0 && progress < 1) {
    const glow = ctx.createRadialGradient(barX + fillW, barY + 1, 0, barX + fillW, barY + 1, 8);
    glow.addColorStop(0, "rgba(0, 255, 100, 0.5)");
    glow.addColorStop(1, "rgba(0, 255, 100, 0)");
    ctx.fillStyle = glow;
    ctx.beginPath();
    ctx.arc(barX + fillW, barY + 1, 8, 0, Math.PI * 2);
    ctx.fill();
  }

  // Tick marks and labels for each zone
  for (let i = 0; i < ZONES.length; i++) {
    const zone = ZONES[i];
    const tickX = barX + barW * zone.progressStart;
    const isReached = progress >= zone.progressStart;
    const isCurrent = i === activeZone;

    // Tick
    ctx.fillStyle = isReached ? "rgba(0, 255, 100, 0.5)" : "rgba(0, 255, 100, 0.12)";
    ctx.fillRect(tickX, barY - 3, 1, barH + 6);

    // Dot
    const dotR = isCurrent ? 4 : 2.5;
    ctx.beginPath();
    ctx.arc(tickX, barY + 1, dotR, 0, Math.PI * 2);
    ctx.fillStyle = isReached ? "rgba(0, 255, 100, 0.7)" : "rgba(0, 255, 100, 0.15)";
    ctx.fill();

    // Current zone glow
    if (isCurrent) {
      const glowPulse = 0.3 + 0.15 * Math.sin(time * 0.004);
      const dotGlow = ctx.createRadialGradient(tickX, barY + 1, 0, tickX, barY + 1, 10);
      dotGlow.addColorStop(0, `rgba(0, 255, 100, ${glowPulse})`);
      dotGlow.addColorStop(1, "rgba(0, 255, 100, 0)");
      ctx.fillStyle = dotGlow;
      ctx.beginPath();
      ctx.arc(tickX, barY + 1, 10, 0, Math.PI * 2);
      ctx.fill();
    }

    // Zone number below
    ctx.fillStyle = isReached ? "rgba(0, 255, 100, 0.4)" : "rgba(0, 255, 100, 0.12)";
    ctx.font = `${isCurrent ? "bold " : ""}9px monospace`;
    ctx.textAlign = "center";
    ctx.textBaseline = "top";
    ctx.fillText(zone.number, tickX, barY + 8);
  }

  // End tick
  ctx.fillStyle = progress >= 1 ? "rgba(0, 255, 100, 0.5)" : "rgba(0, 255, 100, 0.12)";
  ctx.fillRect(barX + barW, barY - 3, 1, barH + 6);

  ctx.restore();
}

function cockpitFrame(ctx: CanvasRenderingContext2D, w: number, h: number) {
  const panelTop = h * 0.78;

  // Instrument panel body
  ctx.fillStyle = "#06080a";
  ctx.beginPath();
  ctx.moveTo(0, panelTop);
  ctx.quadraticCurveTo(w * 0.5, panelTop - h * 0.025, w, panelTop);
  ctx.lineTo(w, h);
  ctx.lineTo(0, h);
  ctx.closePath();
  ctx.fill();

  // Panel surface detail
  ctx.fillStyle = "#0a0d12";
  ctx.fillRect(0, panelTop + 4, w, h - panelTop - 4);

  // Panel edge highlight
  ctx.strokeStyle = "rgba(100, 130, 150, 0.12)";
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(0, panelTop);
  ctx.quadraticCurveTo(w * 0.5, panelTop - h * 0.025, w, panelTop);
  ctx.stroke();

  // Rivets along panel edge
  for (let rx = w * 0.08; rx < w * 0.92; rx += w * 0.04) {
    ctx.fillStyle = "rgba(80, 90, 100, 0.12)";
    ctx.beginPath();
    ctx.arc(rx, panelTop + 2, 1.2, 0, Math.PI * 2);
    ctx.fill();
  }

  // Subtle wear marks / scratches
  ctx.strokeStyle = "rgba(60, 70, 80, 0.06)";
  ctx.lineWidth = 0.5;
  for (let i = 0; i < 5; i++) {
    const sx = w * (0.2 + pseudoRandom(i * 17) * 0.6);
    const sy = panelTop + 8 + pseudoRandom(i * 17 + 1) * (h * 0.05);
    ctx.beginPath();
    ctx.moveTo(sx, sy);
    ctx.lineTo(sx + pseudoRandom(i * 17 + 2) * 30 - 15, sy + pseudoRandom(i * 17 + 3) * 6);
    ctx.stroke();
  }

  // MFD screens (two green-tinted rectangles)
  const mfdW = w * 0.08, mfdH = h * 0.08;
  const mfdY = panelTop + 12;
  // Left MFD
  ctx.fillStyle = "rgba(0, 20, 10, 0.8)";
  ctx.fillRect(w * 0.18, mfdY, mfdW, mfdH);
  ctx.strokeStyle = "rgba(0, 180, 60, 0.15)";
  ctx.lineWidth = 1;
  ctx.strokeRect(w * 0.18, mfdY, mfdW, mfdH);
  // Right MFD
  ctx.fillRect(w * 0.74, mfdY, mfdW, mfdH);
  ctx.strokeRect(w * 0.74, mfdY, mfdW, mfdH);

  // Gauge circles on panel
  const gaugeR = Math.min(w, h) * 0.018;
  const gaugeY = mfdY + mfdH * 0.5;
  for (const gx of [0.35, 0.42, 0.58, 0.65]) {
    ctx.strokeStyle = "rgba(0, 180, 60, 0.1)";
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.arc(w * gx, gaugeY, gaugeR, 0, Math.PI * 2);
    ctx.stroke();
    // Needle
    const angle = -Math.PI * 0.3 + gx * Math.PI * 0.6;
    ctx.strokeStyle = "rgba(0, 255, 100, 0.15)";
    ctx.beginPath();
    ctx.moveTo(w * gx, gaugeY);
    ctx.lineTo(w * gx + Math.cos(angle) * gaugeR * 0.8, gaugeY + Math.sin(angle) * gaugeR * 0.8);
    ctx.stroke();
  }

  // Canopy struts
  ctx.strokeStyle = "rgba(20, 25, 35, 0.5)";
  ctx.lineWidth = 6;
  ctx.beginPath(); ctx.moveTo(0, panelTop); ctx.lineTo(w * 0.10, 0); ctx.stroke();
  ctx.beginPath(); ctx.moveTo(w, panelTop); ctx.lineTo(w * 0.90, 0); ctx.stroke();
  // Top bar
  ctx.lineWidth = 5;
  ctx.beginPath(); ctx.moveTo(w * 0.10, 0); ctx.lineTo(w * 0.90, 0); ctx.stroke();

  // Center post (thin)
  ctx.strokeStyle = "rgba(20, 25, 35, 0.25)";
  ctx.lineWidth = 2;
  ctx.beginPath(); ctx.moveTo(w * 0.5, 0); ctx.lineTo(w * 0.5, panelTop * 0.08); ctx.stroke();
}

/** Subtle vignette overlay for realism */
function drawVignette(ctx: CanvasRenderingContext2D, w: number, h: number, intensity = 0.4) {
  const cx = w * 0.5, cy = h * 0.5;
  const r = Math.max(w, h) * 0.7;
  const grad = ctx.createRadialGradient(cx, cy, r * 0.3, cx, cy, r);
  grad.addColorStop(0, "rgba(0,0,0,0)");
  grad.addColorStop(1, `rgba(0,0,0,${intensity})`);
  ctx.fillStyle = grad;
  ctx.fillRect(0, 0, w, h);
}

/** Atmospheric haze band near horizon for aerial scenes */
function drawHorizonHaze(ctx: CanvasRenderingContext2D, w: number, h: number, horizonY: number) {
  const hazeGrad = ctx.createLinearGradient(0, horizonY - h * 0.08, 0, horizonY + h * 0.05);
  hazeGrad.addColorStop(0, "rgba(20, 30, 45, 0)");
  hazeGrad.addColorStop(0.5, "rgba(20, 30, 45, 0.12)");
  hazeGrad.addColorStop(1, "rgba(20, 30, 45, 0)");
  ctx.fillStyle = hazeGrad;
  ctx.fillRect(0, horizonY - h * 0.08, w, h * 0.13);
}

function drawHudPitchLadder(ctx: CanvasRenderingContext2D, w: number, h: number, alpha: number) {
  const cx = w * 0.5, cy = h * 0.42;
  ctx.strokeStyle = `rgba(0, 255, 100, ${alpha * 0.3})`;
  ctx.lineWidth = 1;
  const lineW = w * 0.08;
  for (let i = -3; i <= 3; i++) {
    if (i === 0) continue;
    const ly = cy + i * h * 0.06;
    const halfW = lineW * (1 - Math.abs(i) * 0.1);
    ctx.beginPath();
    ctx.moveTo(cx - halfW, ly);
    ctx.lineTo(cx - halfW * 0.3, ly);
    ctx.stroke();
    ctx.beginPath();
    ctx.moveTo(cx + halfW * 0.3, ly);
    ctx.lineTo(cx + halfW, ly);
    ctx.stroke();
    ctx.fillStyle = `rgba(0, 255, 100, ${alpha * 0.2})`;
    ctx.font = "10px monospace";
    ctx.textAlign = "right";
    ctx.textBaseline = "middle";
    ctx.fillText(`${Math.abs(i * 5)}`, cx - halfW - 4, ly);
  }
}

/** CRT / film grain overlay for cockpit scenes */
function drawCRTGrain(ctx: CanvasRenderingContext2D, w: number, h: number, time: number, intensity = 0.03) {
  // Scanlines
  ctx.fillStyle = `rgba(0, 20, 0, ${intensity * 4})`;
  for (let sy = 0; sy < h; sy += 3) {
    ctx.fillRect(0, sy, w, 1);
  }

  // Animated noise dots (sparse, using time-seeded positions)
  const seed = Math.floor(time * 0.05);
  ctx.fillStyle = `rgba(0, 255, 100, ${intensity})`;
  for (let i = 0; i < 40; i++) {
    const nx = pseudoRandom(seed + i * 7) * w;
    const ny = pseudoRandom(seed + i * 7 + 3) * h;
    ctx.fillRect(nx, ny, 1, 1);
  }

  // Subtle green tint overlay
  ctx.fillStyle = `rgba(0, 30, 10, ${intensity * 2})`;
  ctx.fillRect(0, 0, w, h);
}

// ── Scene 1: Radar Room ──────────────────────────────────────

function drawRadarRoom(
  ctx: CanvasRenderingContext2D, w: number, h: number, p: number, time: number,
) {
  // CRT background
  ctx.fillStyle = "#020802";
  ctx.fillRect(0, 0, w, h);

  const cx = w * 0.5, cy = h * 0.46;
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
    ctx.strokeStyle = `rgba(0, 180, 60, ${i === 2 ? 0.18 : 0.10})`;
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
    const a = (deg - 90) * Math.PI / 180;
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
      ctx.fillText(labels[deg / 90], cx + Math.cos(a) * labelR, cy + Math.sin(a) * labelR);
    }
  }

  // Crosshairs
  ctx.strokeStyle = "rgba(0, 180, 60, 0.07)";
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(cx - r, cy); ctx.lineTo(cx + r, cy);
  ctx.moveTo(cx, cy - r); ctx.lineTo(cx, cy + r);
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
  ctx.beginPath(); ctx.arc(cx, cy, 3, 0, Math.PI * 2); ctx.fill();

  // Noise blips
  for (const nb of NOISE_BLIPS) {
    const na = (sweepAngle + nb.a) % (Math.PI * 2);
    const age = ((time * 0.0008 - nb.a + Math.PI * 4) % (Math.PI * 2)) / (Math.PI * 2);
    if (age < 0.3) {
      const nbAlpha = (1 - age / 0.3) * 0.12;
      ctx.fillStyle = `rgba(0, 255, 80, ${nbAlpha})`;
      ctx.beginPath();
      ctx.arc(cx + Math.cos(na) * r * nb.d, cy + Math.sin(na) * r * nb.d, 2, 0, Math.PI * 2);
      ctx.fill();
    }
  }

  // Main blip
  if (p > 0.25) {
    const ba = Math.min(1, (p - 0.25) / 0.15);
    const bAngle = -Math.PI * 0.3;
    const bDist = r * 0.62;
    const bx = cx + Math.cos(bAngle) * bDist;
    const by = cy + Math.sin(bAngle) * bDist;

    const glow = ctx.createRadialGradient(bx, by, 0, bx, by, 20);
    glow.addColorStop(0, `rgba(255, 50, 30, ${ba * 0.7})`);
    glow.addColorStop(1, "rgba(255, 50, 30, 0)");
    ctx.fillStyle = glow;
    ctx.beginPath(); ctx.arc(bx, by, 20, 0, Math.PI * 2); ctx.fill();

    const blink = Math.sin(time * 0.005) > 0 ? 1 : 0.4;
    ctx.fillStyle = `rgba(255, 60, 40, ${ba * blink})`;
    ctx.beginPath(); ctx.arc(bx, by, 5, 0, Math.PI * 2); ctx.fill();

    // Tracking brackets
    if (p > 0.4) {
      const tb = Math.min(1, (p - 0.4) / 0.1);
      ctx.strokeStyle = `rgba(255, 60, 40, ${tb * 0.7})`;
      ctx.lineWidth = 1.5;
      const bs = 14;
      const corners = [[-bs, -bs], [bs, -bs], [bs, bs], [-bs, bs]];
      for (const [dx, dy] of corners) {
        ctx.beginPath();
        ctx.moveTo(bx + dx, by + dy + (dy < 0 ? 5 : -5) * Math.sign(dy));
        ctx.lineTo(bx + dx, by + dy);
        ctx.lineTo(bx + dx + (dx < 0 ? 5 : -5) * Math.sign(dx), by + dy);
        ctx.stroke();
      }
    }

    if (p > 0.45) {
      const la = Math.min(1, (p - 0.45) / 0.15);
      ctx.fillStyle = `rgba(255, 60, 40, ${la})`;
      ctx.font = "bold 14px monospace";
      ctx.textAlign = "left";
      ctx.textBaseline = "middle";
      ctx.fillText("INCIDENT", bx + 18, by - 8);

      if (p > 0.6) {
        const da = Math.min(1, (p - 0.6) / 0.15);
        ctx.fillStyle = `rgba(0, 220, 80, ${da * 0.8})`;
        ctx.font = "12px monospace";
        ctx.fillText("TypeError: null ref", bx + 18, by + 8);
        ctx.fillText("POST /api/users 500", bx + 18, by + 22);
        ctx.fillText("src/handlers/user.ts:142", bx + 18, by + 36);
      }
    }
  }

  // Scope border
  ctx.strokeStyle = "rgba(0, 180, 60, 0.3)";
  ctx.lineWidth = 2;
  ctx.beginPath(); ctx.arc(cx, cy, r, 0, Math.PI * 2); ctx.stroke();

  // CRT grain + vignette
  drawCRTGrain(ctx, w, h, time, 0.025);
  drawVignette(ctx, w, h, 0.5);

  // Corner data blocks (bumped sizes)
  hudText(ctx, 24, 22, "ALERT RECEIVED", 0.6, 14);
  hudText(ctx, 24, 40, "SENTRY \u2022 LINEAR \u2022 PAGERDUTY", 0.25, 10);
  hudText(ctx, w - 24, 22, "STEP 1/6", 0.35, 11, "right");
  hudText(ctx, w - 24, 38, p > 0.25 ? "1 INCIDENT" : "MONITORING", p > 0.25 ? 0.6 : 0.3, 10, "right");

  // Bottom status bar
  ctx.fillStyle = "rgba(0, 180, 60, 0.08)";
  ctx.fillRect(0, h - 32, w, 32);
  hudText(ctx, 24, h - 16, "UPTIME 99.97%", 0.3, 10);
  hudText(ctx, w * 0.35, h - 16, "LATENCY P99 142ms", 0.3, 10);
  hudText(ctx, w * 0.65, h - 16, "REQUESTS 14.2K/s", 0.3, 10);
}

// ── Scene 2: Scramble ────────────────────────────────────────

function drawScramble(
  ctx: CanvasRenderingContext2D, w: number, h: number, p: number, time: number,
) {
  // ── 1. Night sky — gradient top 38% ──
  const horizonY = h * 0.38;
  const skyGrad = ctx.createLinearGradient(0, 0, 0, horizonY);
  skyGrad.addColorStop(0, "#020510");
  skyGrad.addColorStop(0.4, "#060c18");
  skyGrad.addColorStop(0.75, "#0a1220");
  skyGrad.addColorStop(1, "#0e1628");
  ctx.fillStyle = skyGrad;
  ctx.fillRect(0, 0, w, horizonY);

  // Stars with twinkle
  for (const star of STARS.slice(0, 50)) {
    if (star.y >= 0.38) continue;
    const twinkle = 0.06 + 0.12 * Math.sin(time * 0.0008 + star.x * 60 + star.y * 40);
    ctx.fillStyle = `rgba(255, 255, 255, ${twinkle})`;
    ctx.beginPath();
    ctx.arc(star.x * w, star.y * h, star.s * 0.6, 0, Math.PI * 2);
    ctx.fill();
  }

  // ── 2. Horizon silhouette — treeline + hangar roofline + control tower spike ──
  ctx.fillStyle = "#060a10";
  ctx.beginPath();
  ctx.moveTo(0, horizonY);

  // Treeline from left edge to hangar area
  for (let tx = 0; tx < w * 0.15; tx += 5) {
    const treeH = 3 + pseudoRandom(tx * 0.13 + 600) * 8 + Math.sin(tx * 0.04) * 2;
    ctx.lineTo(tx, horizonY - treeH);
  }

  // Hangar roofline (blocky shape rising above treeline)
  ctx.lineTo(w * 0.15, horizonY - 12);
  ctx.lineTo(w * 0.16, horizonY - 22);
  ctx.lineTo(w * 0.20, horizonY - 24);
  ctx.lineTo(w * 0.24, horizonY - 22);
  ctx.lineTo(w * 0.25, horizonY - 12);

  // Control tower spike
  ctx.lineTo(w * 0.28, horizonY - 10);
  ctx.lineTo(w * 0.29, horizonY - 35);
  ctx.lineTo(w * 0.295, horizonY - 38);
  ctx.lineTo(w * 0.30, horizonY - 35);
  ctx.lineTo(w * 0.31, horizonY - 10);

  // Continue treeline across the rest of the horizon
  for (let tx = w * 0.32; tx <= w; tx += 5) {
    const treeH = 3 + pseudoRandom(tx * 0.11 + 700) * 7 + Math.sin(tx * 0.035) * 2.5;
    ctx.lineTo(tx, horizonY - treeH);
  }

  ctx.lineTo(w, horizonY);
  ctx.closePath();
  ctx.fill();

  // ── 3. Sky glow behind hangar — warm interior light spilling upward ──
  const hangarCenterX = w * 0.20;
  const doorOpen = Math.min(1, Math.max(0, (p - 0.05) / 0.10));
  if (doorOpen > 0) {
    const glowR = h * 0.15;
    const skyGlowGrad = ctx.createRadialGradient(
      hangarCenterX, horizonY, 0,
      hangarCenterX, horizonY, glowR,
    );
    skyGlowGrad.addColorStop(0, `rgba(255, 190, 100, ${doorOpen * 0.08})`);
    skyGlowGrad.addColorStop(0.4, `rgba(255, 160, 80, ${doorOpen * 0.04})`);
    skyGlowGrad.addColorStop(1, "rgba(255, 140, 60, 0)");
    ctx.fillStyle = skyGlowGrad;
    ctx.beginPath();
    ctx.arc(hangarCenterX, horizonY, glowR, 0, Math.PI * 2);
    ctx.fill();
  }

  // ── 4. Tarmac surface — dark ground from horizon to bottom ──
  const tarmacGrad = ctx.createLinearGradient(0, horizonY, 0, h);
  tarmacGrad.addColorStop(0, "#0a0e14");
  tarmacGrad.addColorStop(0.3, "#0c1018");
  tarmacGrad.addColorStop(1, "#080c12");
  ctx.fillStyle = tarmacGrad;
  ctx.fillRect(0, horizonY, w, h - horizonY);

  // Subtle concrete texture patches
  for (let i = 0; i < 25; i++) {
    const px = pseudoRandom(i * 17 + 300) * w;
    const py = horizonY + pseudoRandom(i * 17 + 301) * (h - horizonY) * 0.85;
    const ps = 6 + pseudoRandom(i * 17 + 302) * 18;
    const shade = pseudoRandom(i * 17 + 303) > 0.5 ? "10,14,20" : "8,11,16";
    const pa = 0.08 + pseudoRandom(i * 17 + 304) * 0.06;
    ctx.fillStyle = `rgba(${shade}, ${pa})`;
    ctx.beginPath();
    ctx.ellipse(px, py, ps, ps * 0.35, pseudoRandom(i * 17 + 305) * Math.PI, 0, Math.PI * 2);
    ctx.fill();
  }

  // ── 5. Runway perspective lights — two parallel rows converging to vanishing point ──
  const vpX = w * 0.35;
  const vpY = horizonY;
  const numLights = 18;
  for (let i = 0; i < numLights; i++) {
    const t = i / (numLights - 1);
    const t2 = t * t; // quadratic spacing for depth compression
    const lx = vpX + (w * 0.90 - vpX) * t2;
    const spreadY = (h * 0.48 - horizonY) * t2;
    const ly = horizonY + spreadY;
    const lightSize = 1.0 + t2 * 2.5;
    const lightAlpha = 0.15 + t2 * 0.45;

    // Left row
    const leftOffY = ly - 4 * t2 - 2;
    ctx.fillStyle = `rgba(255, 220, 150, ${lightAlpha})`;
    ctx.beginPath();
    ctx.arc(lx, leftOffY, lightSize, 0, Math.PI * 2);
    ctx.fill();
    // Light bloom
    if (t2 > 0.1) {
      const bloomGrad = ctx.createRadialGradient(lx, leftOffY, 0, lx, leftOffY, lightSize * 3);
      bloomGrad.addColorStop(0, `rgba(255, 220, 150, ${lightAlpha * 0.15})`);
      bloomGrad.addColorStop(1, "rgba(255, 220, 150, 0)");
      ctx.fillStyle = bloomGrad;
      ctx.beginPath();
      ctx.arc(lx, leftOffY, lightSize * 3, 0, Math.PI * 2);
      ctx.fill();
    }

    // Right row
    const rightOffY = ly + 4 * t2 + 2;
    ctx.fillStyle = `rgba(255, 220, 150, ${lightAlpha})`;
    ctx.beginPath();
    ctx.arc(lx, rightOffY, lightSize, 0, Math.PI * 2);
    ctx.fill();
    if (t2 > 0.1) {
      const bloomGrad2 = ctx.createRadialGradient(lx, rightOffY, 0, lx, rightOffY, lightSize * 3);
      bloomGrad2.addColorStop(0, `rgba(255, 220, 150, ${lightAlpha * 0.15})`);
      bloomGrad2.addColorStop(1, "rgba(255, 220, 150, 0)");
      ctx.fillStyle = bloomGrad2;
      ctx.beginPath();
      ctx.arc(lx, rightOffY, lightSize * 3, 0, Math.PI * 2);
      ctx.fill();
    }
  }

  // ── 6. Taxiway blue lights — line from hangar area toward runway junction ──
  const taxiStartX = w * 0.22;
  const taxiStartY = horizonY + 6;
  const taxiEndX = w * 0.38;
  const taxiEndY = horizonY + (h * 0.48 - horizonY) * 0.12;
  const numTaxiLights = 10;
  for (let i = 0; i < numTaxiLights; i++) {
    const t = i / (numTaxiLights - 1);
    const tlx = taxiStartX + (taxiEndX - taxiStartX) * t;
    const tly = taxiStartY + (taxiEndY - taxiStartY) * t;
    const tSize = 1.0 + t * 1.0;
    ctx.fillStyle = `rgba(50, 120, 255, ${0.25 + t * 0.2})`;
    ctx.beginPath();
    ctx.arc(tlx, tly, tSize, 0, Math.PI * 2);
    ctx.fill();
    // Blue bloom
    const bGrad = ctx.createRadialGradient(tlx, tly, 0, tlx, tly, tSize * 4);
    bGrad.addColorStop(0, `rgba(50, 120, 255, ${0.06 + t * 0.04})`);
    bGrad.addColorStop(1, "rgba(50, 120, 255, 0)");
    ctx.fillStyle = bGrad;
    ctx.beginPath();
    ctx.arc(tlx, tly, tSize * 4, 0, Math.PI * 2);
    ctx.fill();
  }

  // ── 7. Hangar building — dark block above horizon on left side ──
  const hx = w * 0.14;
  const hw = w * 0.12;
  const hTop = horizonY - 24;
  const hBottom = horizonY + 8;
  const hh = hBottom - hTop;

  // Hangar body
  ctx.fillStyle = "#0a0e16";
  ctx.fillRect(hx, hTop, hw, hh);

  // Roof ridge
  ctx.strokeStyle = "rgba(60, 80, 100, 0.15)";
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(hx, hTop);
  ctx.lineTo(hx + hw * 0.5, hTop - 5);
  ctx.lineTo(hx + hw, hTop);
  ctx.stroke();

  // Hangar doors sliding open (p 0.05-0.15)
  const doorW = hw * 0.45;
  if (doorOpen < 1) {
    ctx.fillStyle = "#0c1018";
    const closedW = doorW * (1 - doorOpen);
    ctx.fillRect(hx, hTop, closedW, hh);
    ctx.fillRect(hx + hw - closedW, hTop, closedW, hh);
  }

  // Interior warm glow through open doors
  if (doorOpen > 0) {
    const interiorGrad = ctx.createRadialGradient(
      hx + hw * 0.5, hTop + hh * 0.5, 0,
      hx + hw * 0.5, hTop + hh * 0.5, hw * 0.6,
    );
    interiorGrad.addColorStop(0, `rgba(255, 200, 120, ${doorOpen * 0.18})`);
    interiorGrad.addColorStop(0.5, `rgba(255, 170, 90, ${doorOpen * 0.08})`);
    interiorGrad.addColorStop(1, "rgba(255, 150, 70, 0)");
    ctx.fillStyle = interiorGrad;
    ctx.fillRect(hx, hTop, hw, hh);

    // Light spill on ground in front of hangar
    const spillGrad = ctx.createRadialGradient(
      hx + hw * 0.5, hBottom, 0,
      hx + hw * 0.5, hBottom, hw * 0.8,
    );
    spillGrad.addColorStop(0, `rgba(255, 200, 120, ${doorOpen * 0.06})`);
    spillGrad.addColorStop(1, "rgba(255, 180, 100, 0)");
    ctx.fillStyle = spillGrad;
    ctx.beginPath();
    ctx.arc(hx + hw * 0.5, hBottom, hw * 0.8, 0, Math.PI * 2);
    ctx.fill();
  }

  // Hangar outline
  ctx.strokeStyle = "rgba(60, 80, 100, 0.12)";
  ctx.lineWidth = 1;
  ctx.strokeRect(hx, hTop, hw, hh);

  // ── 8. Floodlight pools — warm radial gradients on tarmac near hangar ──
  for (const flPos of [
    { x: hx + hw + w * 0.04, y: horizonY + 4 },
    { x: hx - w * 0.02, y: horizonY + 6 },
  ]) {
    const poolR = h * 0.06 + Math.min(w, h) * 0.02;
    const poolGrad = ctx.createRadialGradient(flPos.x, flPos.y, 0, flPos.x, flPos.y, poolR);
    poolGrad.addColorStop(0, `rgba(255, 230, 180, ${p > 0.1 ? 0.05 : 0.02})`);
    poolGrad.addColorStop(0.5, `rgba(255, 210, 160, ${p > 0.1 ? 0.02 : 0.008})`);
    poolGrad.addColorStop(1, "rgba(255, 200, 140, 0)");
    ctx.fillStyle = poolGrad;
    ctx.beginPath();
    ctx.arc(flPos.x, flPos.y, poolR, 0, Math.PI * 2);
    ctx.fill();
  }

  // ── 9. Jet — emerges from hangar, taxis along runway toward camera ──
  // The plane faces along the runway's perspective direction: nose points TOWARD
  // the vanishing point (away from camera). The rotation is the angle from the
  // jet's position back to the VP. Near the VP this is nearly horizontal; as the
  // jet moves closer to the camera the angle tilts slightly up-left.
  if (p > 0.15) {
    const jp = Math.min(1, (p - 0.15) / 0.85); // 0..1 over p 0.15..1.0

    const minSize = Math.min(w, h) * 0.035;
    const maxSize = Math.min(w, h) * 0.11;

    // Runway centerline function (matches the light geometry exactly)
    const rwyFarX = w * 0.90;
    const rwySpreadMax = h * 0.48 - horizonY;
    function runwayPos(t: number) {
      const tt = t * t;
      return {
        x: vpX + (rwyFarX - vpX) * tt,
        y: horizonY + rwySpreadMax * tt,
      };
    }

    // Compute heading angle: nose points AWAY from VP (direction of travel,
    // toward camera). drawP80Side nose is +X, so we need the angle from
    // VP to the jet's position (away from VP).
    function runwayHeading(pos: { x: number; y: number }) {
      return Math.atan2(pos.y - vpY, pos.x - vpX); // away from VP
    }

    let jetX: number;
    let jetY: number;
    let jetAlpha: number;
    let jetSize: number;
    let jetRotation: number;

    if (jp < 0.25) {
      // Phase 1: emerge from hangar, taxi to runway threshold
      const t = jp / 0.25;
      const ease = t * t * (3 - 2 * t);
      const startX = hx + hw * 0.7;
      const startY = horizonY + 2;
      const rwyStart = runwayPos(0.18);
      jetX = startX + (rwyStart.x - startX) * ease;
      jetY = startY + (rwyStart.y - startY) * ease;
      jetAlpha = Math.min(1, t * 4);
      jetSize = minSize + (maxSize - minSize) * 0.05 * ease;
      // Blend from facing right (hangar exit) to runway heading
      const rwyAngle = runwayHeading(rwyStart);
      jetRotation = ease * rwyAngle; // 0 → runway angle
    } else if (jp < 0.45) {
      // Phase 2: on the runway, taxiing — moderate growth
      const t = (jp - 0.25) / 0.20;
      const ease = t * t * (3 - 2 * t);
      const rwyA = runwayPos(0.18);
      const rwyB = runwayPos(0.40);
      jetX = rwyA.x + (rwyB.x - rwyA.x) * ease;
      jetY = rwyA.y + (rwyB.y - rwyA.y) * ease;
      jetAlpha = 1;
      jetSize = minSize + (maxSize - minSize) * (0.05 + 0.15 * ease);
      jetRotation = runwayHeading({ x: jetX, y: jetY });
    } else {
      // Phase 3: approaching camera — accelerating size growth
      const t = (jp - 0.45) / 0.55;
      const ease = t * t;
      const rwyC = runwayPos(0.40);
      const rwyD = runwayPos(0.80);
      jetX = rwyC.x + (rwyD.x - rwyC.x) * ease;
      jetY = rwyC.y + (rwyD.y - rwyC.y) * ease;
      jetAlpha = 1;
      jetSize = minSize + (maxSize - minSize) * (0.20 + 0.80 * ease);
      jetRotation = runwayHeading({ x: jetX, y: jetY });
    }

    // Apply 3D perspective skew: slight vertical compression + shear to simulate
    // viewing the plane from a slightly elevated camera angle. The skew increases
    // as the plane gets closer to the camera (more foreshortened when near).
    const depthT = (jetY - horizonY) / (h * 0.5 - horizonY); // 0 at horizon, ~1 near camera
    const ySquash = 0.55 + 0.15 * (1 - depthT); // more squashed when closer
    const skewAmount = -0.12 * depthT; // slight lean, increases with proximity
    ctx.save();
    ctx.translate(jetX, jetY);
    ctx.transform(1, skewAmount, 0, ySquash, 0, 0);
    ctx.translate(-jetX, -jetY);
    drawP80Side(ctx, jetX, jetY, jetSize, jetRotation, jetAlpha, 0, { gearDown: true });
    ctx.restore();

    // Taxi light (forward-pointing cone from nose, along heading)
    if (jp > 0.05) {
      const cosR = Math.cos(jetRotation);
      const sinR = Math.sin(jetRotation);
      const noseX = jetX + cosR * jetSize * 0.90;
      const noseY = jetY + sinR * jetSize * 0.90;
      const lightReach = jetSize * 0.45;
      const taxiLightGrad = ctx.createRadialGradient(
        noseX, noseY, 0,
        noseX + cosR * lightReach, noseY + sinR * lightReach, lightReach,
      );
      taxiLightGrad.addColorStop(0, `rgba(255, 250, 230, ${jetAlpha * 0.10})`);
      taxiLightGrad.addColorStop(0.5, `rgba(255, 240, 200, ${jetAlpha * 0.03})`);
      taxiLightGrad.addColorStop(1, "rgba(255, 230, 180, 0)");
      ctx.fillStyle = taxiLightGrad;
      ctx.beginPath();
      ctx.arc(noseX + cosR * lightReach * 0.4, noseY + sinR * lightReach * 0.4, lightReach, 0, Math.PI * 2);
      ctx.fill();
    }

    // Wheel ground contact glow
    if (jp > 0.05) {
      const gcAlpha = jetAlpha * 0.03;
      ctx.fillStyle = `rgba(255, 230, 180, ${gcAlpha})`;
      ctx.beginPath();
      ctx.ellipse(jetX, jetY + jetSize * 0.24, jetSize * 0.06, jetSize * 0.01, jetRotation, 0, Math.PI * 2);
      ctx.fill();
    }

    // ── 10. Engine glow trail — at tail, along heading ──
    if (jp > 0.1) {
      const trailAlpha = Math.min(0.2, (jp - 0.1) * 0.4);
      const cosR = Math.cos(jetRotation);
      const sinR = Math.sin(jetRotation);
      // Tail is opposite to nose direction
      const tailX = jetX - cosR * jetSize * 0.95;
      const tailY = jetY - sinR * jetSize * 0.95;
      const glowR = jetSize * 0.22;
      const engineGlow = ctx.createRadialGradient(tailX, tailY, 0, tailX, tailY, glowR);
      engineGlow.addColorStop(0, `rgba(255, 160, 50, ${trailAlpha})`);
      engineGlow.addColorStop(0.4, `rgba(255, 120, 30, ${trailAlpha * 0.5})`);
      engineGlow.addColorStop(1, "rgba(255, 100, 20, 0)");
      ctx.fillStyle = engineGlow;
      ctx.beginPath();
      ctx.arc(tailX, tailY, glowR, 0, Math.PI * 2);
      ctx.fill();

      // Exhaust plume (away from nose, along tail direction)
      if (jp > 0.35) {
        const plumeAlpha = Math.min(0.08, (jp - 0.35) * 0.15);
        const plumeLen = jetSize * 0.5;
        const plumeTipX = tailX - cosR * plumeLen;
        const plumeTipY = tailY - sinR * plumeLen;
        const perpX = -sinR * jetSize * 0.03;
        const perpY = cosR * jetSize * 0.03;
        const plumeGrad = ctx.createLinearGradient(tailX, tailY, plumeTipX, plumeTipY);
        plumeGrad.addColorStop(0, `rgba(255, 140, 50, ${plumeAlpha})`);
        plumeGrad.addColorStop(0.5, `rgba(255, 100, 30, ${plumeAlpha * 0.4})`);
        plumeGrad.addColorStop(1, "rgba(255, 80, 20, 0)");
        ctx.fillStyle = plumeGrad;
        ctx.beginPath();
        ctx.moveTo(tailX + perpX, tailY + perpY);
        ctx.lineTo(plumeTipX, plumeTipY);
        ctx.lineTo(tailX - perpX, tailY - perpY);
        ctx.closePath();
        ctx.fill();
      }
    }
  }

  // ── 11. Ground mist — semi-transparent horizontal band near horizon ──
  const mistGrad = ctx.createLinearGradient(0, horizonY - 8, 0, horizonY + 30);
  mistGrad.addColorStop(0, "rgba(12, 18, 28, 0)");
  mistGrad.addColorStop(0.3, "rgba(14, 22, 34, 0.12)");
  mistGrad.addColorStop(0.6, "rgba(16, 24, 36, 0.08)");
  mistGrad.addColorStop(1, "rgba(12, 18, 28, 0)");
  ctx.fillStyle = mistGrad;
  ctx.fillRect(0, horizonY - 8, w, 38);

  // ── 12. Post-processing ──
  drawHorizonHaze(ctx, w, h, horizonY);
  drawVignette(ctx, w, h, 0.35);

  // ── 13. HUD text ──
  hudText(ctx, 24, 22, "AGENT ACTIVATED", 0.6, 14);
  hudText(ctx, 24, 40, p > 0.4 ? "CODEBASE LOADED" : "LOADING CONTEXT...", 0.25, 10);
  hudText(ctx, w - 24, 22, "STEP 2/6", 0.35, 11, "right");
}

// ── Scene 3: Cockpit Launch ──────────────────────────────────

function drawCockpitLaunch(
  ctx: CanvasRenderingContext2D, w: number, h: number, p: number, time: number,
) {
  // Night sky
  const skyGrad = ctx.createLinearGradient(0, 0, 0, h);
  skyGrad.addColorStop(0, "#010306");
  skyGrad.addColorStop(1, "#080e16");
  ctx.fillStyle = skyGrad;
  ctx.fillRect(0, 0, w, h);

  // Stars (visible before takeoff)
  if (p < 0.6) {
    for (const star of STARS.slice(0, 25)) {
      if (star.y > 0.35) continue;
      const twinkle = 0.06 + 0.06 * Math.sin(time * 0.001 + star.x * 50);
      ctx.fillStyle = `rgba(255,255,255,${twinkle * (1 - p * 1.5)})`;
      ctx.beginPath(); ctx.arc(star.x * w, star.y * h, star.s * 0.5, 0, Math.PI * 2); ctx.fill();
    }
  }

  // Vanishing point rises during takeoff
  const vpY = h * (0.40 - p * 0.28);
  const vpX = w * 0.5;

  // Runway surface in perspective
  if (p < 0.75) {
    const rAlpha = 1 - Math.max(0, (p - 0.55) / 0.2);
    ctx.fillStyle = `rgba(28,30,34,${rAlpha})`;
    ctx.beginPath();
    ctx.moveTo(vpX - 2, vpY);
    ctx.lineTo(w * 0.05, h * 0.80);
    ctx.lineTo(w * 0.95, h * 0.80);
    ctx.lineTo(vpX + 2, vpY);
    ctx.closePath();
    ctx.fill();

    // Runway edge markings
    ctx.strokeStyle = `rgba(255,255,255,${0.35 * rAlpha})`;
    ctx.lineWidth = 1.5;
    ctx.beginPath(); ctx.moveTo(vpX, vpY); ctx.lineTo(w * 0.18, h * 0.80); ctx.stroke();
    ctx.beginPath(); ctx.moveTo(vpX, vpY); ctx.lineTo(w * 0.82, h * 0.80); ctx.stroke();

    // Centerline dashes streaming
    for (let i = 0; i < 15; i++) {
      const offset = (time * 0.0004 * (1 + p * 4)) % 1;
      const t1 = ((i / 15) + offset) % 1;
      const t2 = Math.min(1, ((i + 0.35) / 15 + offset) % 1);
      if (t2 < t1) continue;
      const py1 = vpY + (h * 0.80 - vpY) * t1;
      const py2 = vpY + (h * 0.80 - vpY) * t2;
      ctx.strokeStyle = `rgba(255,255,255,${t1 * 0.5 * rAlpha})`;
      ctx.lineWidth = 1 + t1 * 2.5;
      ctx.beginPath(); ctx.moveTo(vpX, py1); ctx.lineTo(vpX, py2); ctx.stroke();
    }

    // Edge lights streaming past
    for (let i = 0; i < 14; i++) {
      const t = ((i / 14) + (time * 0.00025 * (1 + p * 5))) % 1;
      const py = vpY + (h * 0.80 - vpY) * t;
      const spread = t * w * 0.32;
      const ls = 1.5 + t * 3.5;
      const la = t * 0.7 * rAlpha;
      ctx.fillStyle = `rgba(255,220,150,${la})`;
      ctx.beginPath(); ctx.arc(vpX - spread, py, ls, 0, Math.PI * 2); ctx.fill();
      ctx.beginPath(); ctx.arc(vpX + spread, py, ls, 0, Math.PI * 2); ctx.fill();
    }
  }

  // Afterburner glow
  if (p > 0.08) {
    const abA = Math.min(0.35, (p - 0.08) * 0.5);
    const glow = ctx.createRadialGradient(w * 0.5, h, 0, w * 0.5, h, h * 0.45);
    glow.addColorStop(0, `rgba(255,140,40,${abA})`);
    glow.addColorStop(0.4, `rgba(255,80,20,${abA * 0.3})`);
    glow.addColorStop(1, "rgba(255,80,20,0)");
    ctx.fillStyle = glow;
    ctx.fillRect(0, h * 0.4, w, h * 0.6);
  }

  // Pitch ladder
  drawHudPitchLadder(ctx, w, h, Math.min(1, p * 2));

  cockpitFrame(ctx, w, h);

  // HUD readouts (bumped sizes)
  const filesScanned = Math.round(20 + p * 2400);
  const depth = p > 0.55 ? Math.round((p - 0.55) * 12) : 0;
  hudText(ctx, w * 0.14, h * 0.30, `${filesScanned} FILES`, 0.7, 16);
  hudText(ctx, w * 0.14, h * 0.35, `${Math.round(p * 48)} MODULES`, 0.4, 12);
  if (depth > 0) {
    hudText(ctx, w * 0.86, h * 0.30, `DEPTH ${depth}`, 0.7, 16, "right");
    hudText(ctx, w * 0.86, h * 0.35, `${Math.round(depth * 3)} REFS`, 0.4, 12, "right");
  }
  hudText(ctx, w * 0.5, h * 0.12, p > 0.55 ? "SCANNING CALL GRAPH" : "LOADING CODEBASE", 0.5, 14, "center");

  // Blinking cursor after status text
  if (Math.sin(time * 0.005) > 0) {
    const statusText = p > 0.55 ? "SCANNING CALL GRAPH" : "LOADING CODEBASE";
    ctx.font = "bold 14px monospace";
    const textW = ctx.measureText(statusText).width;
    hudText(ctx, w * 0.5 + textW / 2 + 6, h * 0.12, "\u2588", 0.4, 14, "left");
  }

  // Heading tape
  ctx.strokeStyle = "rgba(0, 255, 100, 0.2)";
  ctx.lineWidth = 1;
  ctx.beginPath(); ctx.moveTo(w * 0.3, h * 0.06); ctx.lineTo(w * 0.7, h * 0.06); ctx.stroke();
  hudText(ctx, w * 0.5, h * 0.06, "\u25BD", 0.4, 12, "center");

  drawCRTGrain(ctx, w, h, time, 0.02);
  drawVignette(ctx, w, h, 0.3);

  hudText(ctx, 24, 22, "ROOT CAUSE ANALYSIS", 0.6, 14);
  hudText(ctx, 24, 40, "TRACING CALL GRAPH", 0.25, 10);
  hudText(ctx, w - 24, 22, "STEP 3/6", 0.35, 11, "right");
}

// ── Scene 4: Lock On ─────────────────────────────────────────

function drawLockOn(
  ctx: CanvasRenderingContext2D, w: number, h: number, p: number, time: number,
) {
  // Fade-in factor for first 10% of animation
  const fadeIn = Math.min(1, p / 0.1);

  // ── 1. Background sky + clouds ──────────────────────────────
  const skyGrad = ctx.createLinearGradient(0, 0, 0, h);
  skyGrad.addColorStop(0, "#020510");
  skyGrad.addColorStop(1, "#081020");
  ctx.fillStyle = skyGrad;
  ctx.fillRect(0, 0, w, h);

  for (const c of CLOUDS) {
    const drift = time * 0.00003;
    const ccx = ((c.x + drift) % 1.3 - 0.15) * w;
    ctx.fillStyle = `rgba(15,22,40,${c.a * 1.3})`;
    ctx.beginPath();
    ctx.ellipse(ccx, c.y * h, c.rx * w * 0.12, c.ry * h * 0.06, 0, 0, Math.PI * 2);
    ctx.fill();
  }

  // ── 2. Code rain ────────────────────────────────────────────
  const codeChars = "{}()[];=>";
  const colCount = Math.floor(w / 18);
  ctx.font = "10px monospace";
  ctx.textBaseline = "top";
  for (let col = 0; col < colCount; col += 3) {
    for (let row = 0; row < 5; row++) {
      const seed = col * 7 + row * 13;
      const ch = codeChars[Math.floor(pseudoRandom(seed) * codeChars.length)];
      const cx = col * 18 + 4;
      const cy = ((row * 28 + time * 0.02 * (1 + pseudoRandom(seed + 1))) % (h + 40)) - 20;
      ctx.fillStyle = `rgba(0,255,100,0.04)`;
      ctx.fillText(ch, cx, cy);
    }
  }

  // ── 3. Pitch ladder ─────────────────────────────────────────
  drawHudPitchLadder(ctx, w, h, 0.6 * fadeIn);

  // ── 4. Heading tape (top) ───────────────────────────────────
  const htY = h * 0.06;
  const htH = 22;
  const htLeft = w * 0.3;
  const htRight = w * 0.7;
  const htWidth = htRight - htLeft;
  ctx.fillStyle = `rgba(0,10,5,${0.35 * fadeIn})`;
  ctx.fillRect(htLeft, htY - htH / 2, htWidth, htH);
  ctx.strokeStyle = `rgba(0,255,100,${0.3 * fadeIn})`;
  ctx.lineWidth = 1;
  ctx.strokeRect(htLeft, htY - htH / 2, htWidth, htH);

  // Scrolling degree markings
  const headingOffset = (time * 0.003) % 360;
  const degPerPx = 0.15;
  const centerDeg = headingOffset;
  ctx.save();
  ctx.beginPath();
  ctx.rect(htLeft, htY - htH / 2, htWidth, htH);
  ctx.clip();
  ctx.font = "9px monospace";
  ctx.textAlign = "center";
  ctx.textBaseline = "middle";
  for (let deg = -30; deg <= 30; deg++) {
    const actualDeg = Math.round(centerDeg + deg);
    const normalizedDeg = ((actualDeg % 360) + 360) % 360;
    const px = w * 0.5 + (deg / degPerPx) * 0.08;
    if (px < htLeft || px > htRight) continue;
    if (normalizedDeg % 5 === 0) {
      const isMajor = normalizedDeg % 10 === 0;
      const tickH = isMajor ? 6 : 3;
      ctx.strokeStyle = `rgba(0,255,100,${(isMajor ? 0.5 : 0.25) * fadeIn})`;
      ctx.beginPath();
      ctx.moveTo(px, htY + htH / 2 - tickH);
      ctx.lineTo(px, htY + htH / 2);
      ctx.stroke();
      if (isMajor) {
        const label = String(normalizedDeg).padStart(3, "0");
        ctx.fillStyle = `rgba(0,255,100,${0.5 * fadeIn})`;
        ctx.fillText(label, px, htY - 2);
      }
    }
  }
  ctx.restore();

  // Center caret
  ctx.fillStyle = `rgba(0,255,100,${0.6 * fadeIn})`;
  ctx.beginPath();
  ctx.moveTo(w * 0.5, htY - htH / 2);
  ctx.lineTo(w * 0.5 - 5, htY - htH / 2 - 6);
  ctx.lineTo(w * 0.5 + 5, htY - htH / 2 - 6);
  ctx.closePath();
  ctx.fill();

  // ── 5. Airspeed tape (left) ─────────────────────────────────
  const asX = w * 0.12;
  const asW = 44;
  const asTop = h * 0.2;
  const asBot = h * 0.65;
  const asH = asBot - asTop;
  ctx.fillStyle = `rgba(0,10,5,${0.3 * fadeIn})`;
  ctx.fillRect(asX - asW / 2, asTop, asW, asH);
  ctx.strokeStyle = `rgba(0,255,100,${0.25 * fadeIn})`;
  ctx.lineWidth = 1;
  ctx.strokeRect(asX - asW / 2, asTop, asW, asH);

  const baseSpeed = 420;
  const speedDrift = Math.sin(time * 0.0008) * 3;
  const currentSpeed = baseSpeed + speedDrift;
  ctx.save();
  ctx.beginPath();
  ctx.rect(asX - asW / 2, asTop, asW, asH);
  ctx.clip();
  ctx.font = "9px monospace";
  ctx.textAlign = "right";
  ctx.textBaseline = "middle";
  for (let spd = baseSpeed - 20; spd <= baseSpeed + 20; spd += 2) {
    const frac = (spd - currentSpeed) / 40;
    const yy = asTop + asH / 2 + frac * asH;
    if (yy < asTop || yy > asBot) continue;
    const isMajor = spd % 10 === 0;
    if (isMajor) {
      ctx.fillStyle = `rgba(0,255,100,${0.5 * fadeIn})`;
      ctx.fillText(String(spd), asX + asW / 2 - 14, yy);
      ctx.strokeStyle = `rgba(0,255,100,${0.4 * fadeIn})`;
      ctx.beginPath();
      ctx.moveTo(asX + asW / 2 - 10, yy);
      ctx.lineTo(asX + asW / 2, yy);
      ctx.stroke();
    } else if (spd % 5 === 0) {
      ctx.strokeStyle = `rgba(0,255,100,${0.2 * fadeIn})`;
      ctx.beginPath();
      ctx.moveTo(asX + asW / 2 - 5, yy);
      ctx.lineTo(asX + asW / 2, yy);
      ctx.stroke();
    }
  }
  ctx.restore();

  // Current speed box
  const spdBoxY = asTop + asH / 2;
  ctx.fillStyle = `rgba(0,20,10,${0.8 * fadeIn})`;
  ctx.fillRect(asX - asW / 2, spdBoxY - 10, asW, 20);
  ctx.strokeStyle = `rgba(0,255,100,${0.7 * fadeIn})`;
  ctx.lineWidth = 1.5;
  ctx.strokeRect(asX - asW / 2, spdBoxY - 10, asW, 20);
  ctx.fillStyle = `rgba(0,255,100,${0.9 * fadeIn})`;
  ctx.font = "bold 10px monospace";
  ctx.textAlign = "center";
  ctx.textBaseline = "middle";
  ctx.fillText(String(Math.round(currentSpeed)), asX, spdBoxY);

  // ── 6. Altitude tape (right) ────────────────────────────────
  const altX = w * 0.88;
  const altW = 48;
  const altTop = h * 0.2;
  const altBot = h * 0.65;
  const altH = altBot - altTop;
  ctx.fillStyle = `rgba(0,10,5,${0.3 * fadeIn})`;
  ctx.fillRect(altX - altW / 2, altTop, altW, altH);
  ctx.strokeStyle = `rgba(0,255,100,${0.25 * fadeIn})`;
  ctx.lineWidth = 1;
  ctx.strokeRect(altX - altW / 2, altTop, altW, altH);

  const baseAlt = 250;
  const altDrift = Math.sin(time * 0.0006) * 1.5;
  const currentAlt = baseAlt + altDrift;
  ctx.save();
  ctx.beginPath();
  ctx.rect(altX - altW / 2, altTop, altW, altH);
  ctx.clip();
  ctx.font = "9px monospace";
  ctx.textAlign = "left";
  ctx.textBaseline = "middle";
  for (let alt = baseAlt - 4; alt <= baseAlt + 4; alt++) {
    const frac = (alt - currentAlt) / 8;
    const yy = altTop + altH / 2 + frac * altH;
    if (yy < altTop || yy > altBot) continue;
    const isMajor = alt % 2 === 0;
    if (isMajor) {
      ctx.fillStyle = `rgba(0,255,100,${0.5 * fadeIn})`;
      ctx.fillText(String(alt * 100), altX - altW / 2 + 14, yy);
    }
    ctx.strokeStyle = `rgba(0,255,100,${(isMajor ? 0.4 : 0.2) * fadeIn})`;
    ctx.beginPath();
    ctx.moveTo(altX - altW / 2, yy);
    ctx.lineTo(altX - altW / 2 + (isMajor ? 10 : 5), yy);
    ctx.stroke();
  }
  ctx.restore();

  // Current altitude box
  const altBoxY = altTop + altH / 2;
  ctx.fillStyle = `rgba(0,20,10,${0.8 * fadeIn})`;
  ctx.fillRect(altX - altW / 2, altBoxY - 10, altW, 20);
  ctx.strokeStyle = `rgba(0,255,100,${0.7 * fadeIn})`;
  ctx.lineWidth = 1.5;
  ctx.strokeRect(altX - altW / 2, altBoxY - 10, altW, 20);
  ctx.fillStyle = `rgba(0,255,100,${0.9 * fadeIn})`;
  ctx.font = "bold 10px monospace";
  ctx.textAlign = "center";
  ctx.textBaseline = "middle";
  ctx.fillText(String(Math.round(currentAlt * 100)), altX, altBoxY);

  // ── 7. Velocity vector (flight path marker) ─────────────────
  const rcx = w * 0.5, rcy = h * 0.42;
  const rs = Math.min(w, h) * 0.13;
  const vvR = 5;
  ctx.strokeStyle = `rgba(0,255,100,${0.7 * fadeIn})`;
  ctx.lineWidth = 1.5;
  ctx.beginPath();
  ctx.arc(rcx, rcy - rs * 0.02, vvR, 0, Math.PI * 2);
  ctx.stroke();
  // Wing lines
  ctx.beginPath();
  ctx.moveTo(rcx - vvR, rcy - rs * 0.02);
  ctx.lineTo(rcx - vvR - 12, rcy - rs * 0.02);
  ctx.moveTo(rcx + vvR, rcy - rs * 0.02);
  ctx.lineTo(rcx + vvR + 12, rcy - rs * 0.02);
  ctx.stroke();
  // Top tick
  ctx.beginPath();
  ctx.moveTo(rcx, rcy - rs * 0.02 - vvR);
  ctx.lineTo(rcx, rcy - rs * 0.02 - vvR - 8);
  ctx.stroke();

  // ── 8. G-force + AOA readouts ───────────────────────────────
  const gForce = (1.2 + Math.sin(time * 0.001) * 0.15).toFixed(1);
  const aoa = (2.1 + Math.sin(time * 0.0007) * 0.4).toFixed(1);
  hudText(ctx, w * 0.14, h * 0.72, `G ${gForce}`, 0.5 * fadeIn, 12);
  hudText(ctx, w * 0.14, h * 0.76, `AOA ${aoa}\u00B0`, 0.5 * fadeIn, 12);

  // ── 9. Enhanced reticle ─────────────────────────────────────
  // Solid outer circle
  ctx.strokeStyle = `rgba(0,255,100,${0.5 * fadeIn})`;
  ctx.lineWidth = 1.5;
  ctx.beginPath();
  ctx.arc(rcx, rcy, rs, 0, Math.PI * 2);
  ctx.stroke();

  // Dashed inner circle
  ctx.setLineDash([4, 4]);
  ctx.strokeStyle = `rgba(0,255,100,${0.35 * fadeIn})`;
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.arc(rcx, rcy, rs * 0.6, 0, Math.PI * 2);
  ctx.stroke();
  ctx.setLineDash([]);

  // Cardinal tick marks on outer circle
  const tickLen = 8;
  ctx.strokeStyle = `rgba(0,255,100,${0.5 * fadeIn})`;
  ctx.lineWidth = 1.5;
  for (let i = 0; i < 4; i++) {
    const angle = (i * Math.PI) / 2;
    ctx.beginPath();
    ctx.moveTo(rcx + Math.cos(angle) * rs, rcy + Math.sin(angle) * rs);
    ctx.lineTo(rcx + Math.cos(angle) * (rs + tickLen), rcy + Math.sin(angle) * (rs + tickLen));
    ctx.stroke();
  }

  // Crosshairs with gap
  const gap = rs * 0.18;
  ctx.strokeStyle = `rgba(0,255,100,${0.45 * fadeIn})`;
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(rcx - rs * 0.95, rcy); ctx.lineTo(rcx - gap, rcy);
  ctx.moveTo(rcx + gap, rcy); ctx.lineTo(rcx + rs * 0.95, rcy);
  ctx.moveTo(rcx, rcy - rs * 0.95); ctx.lineTo(rcx, rcy - gap);
  ctx.moveTo(rcx, rcy + gap); ctx.lineTo(rcx, rcy + rs * 0.95);
  ctx.stroke();

  // ── 10. Code fragments in reticle (clipped) ─────────────────
  const codeLines: string[] = [
    'const user = await db.findById(id);',
    'if (!user) throw new Error("null ref");',
    'const perms = user.roles.flatMap(r =>',
    '  r.permissions.filter(p => p.active)',
    ');',
    'return { ...user, perms };',
    'validateSession(req.headers.auth);',
    'const result = handler(req, res);',
  ];
  const addLines: { idx: number; text: string }[] = [
    { idx: 1, text: 'if (!user) return res.status(404);' },
    { idx: 2, text: 'const perms = getPermissions(user);' },
  ];
  const removeIndices = [1, 2]; // lines that get red markers

  const scrollOffset = time * 0.012;
  const lineH = 13;
  const codeStartY = rcy - (codeLines.length * lineH) / 2 + (scrollOffset % lineH);

  ctx.save();
  ctx.beginPath();
  ctx.arc(rcx, rcy, rs * 0.92, 0, Math.PI * 2);
  ctx.clip();

  ctx.font = "9px monospace";
  ctx.textBaseline = "top";

  for (let i = 0; i < codeLines.length; i++) {
    const ly = codeStartY + i * lineH - scrollOffset * 0.3;
    if (ly < rcy - rs || ly > rcy + rs) continue;

    const isRemove = removeIndices.includes(i) && p > 0.3;
    const isAdd = (i === 1 || i === 2) && p > 0.5;

    if (isRemove && !isAdd) {
      // Red removal marker
      const removeAlpha = Math.min(1, (p - 0.3) / 0.1) * 0.6 * fadeIn;
      ctx.fillStyle = `rgba(255,80,60,${removeAlpha})`;
      ctx.fillText("- " + codeLines[i], rcx - rs * 0.8, ly);
    } else if (isAdd) {
      // Show red strikethrough of original
      const removeAlpha = Math.min(1, (p - 0.3) / 0.1) * 0.35 * fadeIn;
      ctx.fillStyle = `rgba(255,80,60,${removeAlpha})`;
      ctx.fillText("- " + codeLines[i], rcx - rs * 0.8, ly - lineH * 0.6);

      // Green addition
      const addAlpha = Math.min(1, (p - 0.5) / 0.1) * 0.7 * fadeIn;
      ctx.fillStyle = `rgba(80,255,120,${addAlpha})`;
      ctx.fillText("+ " + addLines[i === 1 ? 0 : 1].text, rcx - rs * 0.8, ly);
    } else {
      // Normal code line
      ctx.fillStyle = `rgba(0,255,100,${0.35 * fadeIn})`;
      ctx.fillText("  " + codeLines[i], rcx - rs * 0.8, ly);
    }
  }

  ctx.restore();

  // ── 11. Target acquisition box (corner brackets) ────────────
  const locked = p > 0.7;
  const boxSizeMax = rs * 2.2;
  const boxSizeMin = rs * 0.55;
  const boxShrink = Math.min(1, p / 0.7);
  const boxSize = boxSizeMax - (boxSizeMax - boxSizeMin) * boxShrink;
  const bracketLen = boxSize * 0.25;
  const bx = rcx - boxSize / 2;
  const by = rcy - boxSize / 2;

  const boxColor = locked
    ? `rgba(255,60,40,${0.9 * fadeIn})`
    : `rgba(0,255,100,${(0.5 + boxShrink * 0.3) * fadeIn})`;
  ctx.strokeStyle = boxColor;
  ctx.lineWidth = 2;

  // Top-left corner
  ctx.beginPath();
  ctx.moveTo(bx, by + bracketLen); ctx.lineTo(bx, by); ctx.lineTo(bx + bracketLen, by);
  ctx.stroke();
  // Top-right corner
  ctx.beginPath();
  ctx.moveTo(bx + boxSize - bracketLen, by); ctx.lineTo(bx + boxSize, by); ctx.lineTo(bx + boxSize, by + bracketLen);
  ctx.stroke();
  // Bottom-left corner
  ctx.beginPath();
  ctx.moveTo(bx, by + boxSize - bracketLen); ctx.lineTo(bx, by + boxSize); ctx.lineTo(bx + bracketLen, by + boxSize);
  ctx.stroke();
  // Bottom-right corner
  ctx.beginPath();
  ctx.moveTo(bx + boxSize - bracketLen, by + boxSize); ctx.lineTo(bx + boxSize, by + boxSize); ctx.lineTo(bx + boxSize, by + boxSize - bracketLen);
  ctx.stroke();

  // ── 12. Range-to-target ─────────────────────────────────────
  const range = Math.round(850 * (1 - p));
  hudText(ctx, w * 0.86, h * 0.42, `RNG ${String(range).padStart(4, " ")}`, 0.6 * fadeIn, 11, "right");

  // ── 13. Lock-on flash (p > 0.7) ─────────────────────────────
  if (locked) {
    const la = Math.min(1, (p - 0.7) / 0.15);
    const blink = Math.sin(time * 0.01) > 0 ? 1 : 0.5;

    // "PATCH READY" flashing text
    ctx.fillStyle = `rgba(255,60,40,${la * blink * fadeIn})`;
    ctx.font = "bold 16px monospace";
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    ctx.fillText("PATCH READY", rcx, rcy + rs + 30);

    // Pulsing concentric lock rings
    const pulse = Math.sin(time * 0.008);
    for (let ring = 0; ring < 3; ring++) {
      const ringR = rs * (1.15 + ring * 0.12) + pulse * 3;
      const ringAlpha = la * (0.4 - ring * 0.12) * fadeIn;
      ctx.strokeStyle = `rgba(255,60,40,${ringAlpha})`;
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.arc(rcx, rcy, ringR, 0, Math.PI * 2);
      ctx.stroke();
    }
  }

  // ── 14. Animated counters ───────────────────────────────────
  const linesAdded = Math.round(2 + p * 18);
  const linesRemoved = Math.round(1 + p * 7);
  const filesCount = Math.round(3 + p * 4);
  hudText(ctx, w * 0.86, h * 0.30, `DIFF +${linesAdded} -${linesRemoved}`, 0.7 * fadeIn, 13, "right");
  hudText(ctx, w * 0.86, h * 0.34, `${filesCount} FILES`, 0.4 * fadeIn, 11, "right");

  // Sandbox active (pulsing)
  const sandboxPulse = 0.3 + Math.sin(time * 0.004) * 0.15;
  hudText(ctx, w * 0.14, h * 0.35, "SANDBOX ACTIVE", sandboxPulse * fadeIn, 11);

  // ── 15. Cockpit frame + post-processing ─────────────────────
  cockpitFrame(ctx, w, h);

  // Center status "GENERATING PATCH" with blinking cursor
  hudText(ctx, w * 0.5, h * 0.12, "GENERATING PATCH", 0.5 * fadeIn, 14, "center");
  if (Math.sin(time * 0.005) > 0) {
    ctx.font = "bold 14px monospace";
    const tw = ctx.measureText("GENERATING PATCH").width;
    hudText(ctx, w * 0.5 + tw / 2 + 6, h * 0.12, "\u2588", 0.4 * fadeIn, 14, "left");
  }

  drawCRTGrain(ctx, w, h, time, 0.02);
  drawVignette(ctx, w, h, 0.3);

  // ── 16. HUD text labels ─────────────────────────────────────
  hudText(ctx, 24, 22, "FIX GENERATED", 0.6 * fadeIn, 14);
  hudText(ctx, 24, 40, "BUILDING PATCH", 0.25 * fadeIn, 10);
  hudText(ctx, w - 24, 22, "STEP 4/6", 0.35 * fadeIn, 11, "right");
  hudText(ctx, w * 0.14, h * 0.30, "BUILDING FIX", 0.6 * fadeIn, 15);
}

// ── Scene 5: Neutralized ─────────────────────────────────────

function drawNeutralized(
  ctx: CanvasRenderingContext2D, w: number, h: number, p: number, time: number,
) {
  ctx.fillStyle = "#030610";
  ctx.fillRect(0, 0, w, h);

  // Impact flash — bigger, more dramatic
  if (p > 0.12 && p < 0.5) {
    const fp = (p - 0.12) / 0.38;
    const fa = fp < 0.2 ? fp / 0.2 : 1 - (fp - 0.2) / 0.8;
    const fr = w * 0.1 + fp * w * 0.45;
    const flash = ctx.createRadialGradient(w * 0.5, h * 0.4, 0, w * 0.5, h * 0.4, fr);
    flash.addColorStop(0, `rgba(255,255,250,${fa * 0.95})`);
    flash.addColorStop(0.15, `rgba(255,240,200,${fa * 0.6})`);
    flash.addColorStop(0.4, `rgba(255,200,100,${fa * 0.25})`);
    flash.addColorStop(1, "rgba(255,200,100,0)");
    ctx.fillStyle = flash;
    ctx.beginPath(); ctx.arc(w * 0.5, h * 0.4, fr, 0, Math.PI * 2); ctx.fill();

    // Particles
    for (let i = 0; i < 18; i++) {
      const pa = pseudoRandom(i * 7) * Math.PI * 2;
      const pd = fp * (40 + pseudoRandom(i * 7 + 1) * 80);
      const px = w * 0.5 + Math.cos(pa) * pd;
      const py = h * 0.4 + Math.sin(pa) * pd;
      const pAlpha = fa * (1 - fp) * 0.7;
      ctx.fillStyle = `rgba(255, 220, 100, ${pAlpha})`;
      ctx.beginPath(); ctx.arc(px, py, 1.5 + pseudoRandom(i * 7 + 2) * 2, 0, Math.PI * 2); ctx.fill();
    }
  }

  // Green screen wash when tests pass
  if (p > 0.35) {
    const washA = Math.min(0.06, (p - 0.35) * 0.15);
    ctx.fillStyle = `rgba(0, 255, 100, ${washA})`;
    ctx.fillRect(0, 0, w, h);
  }

  const rcx = w * 0.5, rcy = h * 0.42;
  const rs = Math.min(w, h) * 0.13;
  const isGreen = p > 0.4;
  const hc = isGreen ? "rgba(0,255,100,0.7)" : "rgba(255,60,40,0.7)";

  // Reticle
  ctx.strokeStyle = hc;
  ctx.lineWidth = 1.5;
  ctx.beginPath(); ctx.arc(rcx, rcy, rs, 0, Math.PI * 2); ctx.stroke();

  const rGap = rs * 0.15;
  ctx.beginPath();
  ctx.moveTo(rcx - rs, rcy); ctx.lineTo(rcx - rGap, rcy);
  ctx.moveTo(rcx + rGap, rcy); ctx.lineTo(rcx + rs, rcy);
  ctx.moveTo(rcx, rcy - rs); ctx.lineTo(rcx, rcy - rGap);
  ctx.moveTo(rcx, rcy + rGap); ctx.lineTo(rcx, rcy + rs);
  ctx.stroke();

  // Green pulse ring expanding
  if (p > 0.4 && p < 0.8) {
    const pulseP = (p - 0.4) / 0.4;
    const pulseR = rs + pulseP * rs * 1.5;
    const pulseA = (1 - pulseP) * 0.4;
    ctx.strokeStyle = `rgba(0, 255, 100, ${pulseA})`;
    ctx.lineWidth = 3;
    ctx.beginPath();
    ctx.arc(rcx, rcy, pulseR, 0, Math.PI * 2);
    ctx.stroke();
  }

  // Confirmation diamond
  if (p > 0.35) {
    const da = Math.min(1, (p - 0.35) / 0.15);
    ctx.strokeStyle = `rgba(0,255,100,${da})`;
    ctx.lineWidth = 2;
    const dds = 14;
    ctx.beginPath();
    ctx.moveTo(rcx, rcy - dds); ctx.lineTo(rcx + dds, rcy);
    ctx.lineTo(rcx, rcy + dds); ctx.lineTo(rcx - dds, rcy);
    ctx.closePath();
    ctx.stroke();

    // Checkmark inside diamond
    if (p > 0.5) {
      const ca = Math.min(1, (p - 0.5) / 0.1);
      ctx.strokeStyle = `rgba(0, 255, 100, ${ca})`;
      ctx.lineWidth = 2.5;
      ctx.beginPath();
      ctx.moveTo(rcx - 5, rcy);
      ctx.lineTo(rcx - 1, rcy + 4);
      ctx.lineTo(rcx + 6, rcy - 5);
      ctx.stroke();
    }
  }

  // "ALL TESTS PASSING" text — bigger, bolder
  if (p > 0.4) {
    const na = Math.min(1, (p - 0.4) / 0.2);
    ctx.fillStyle = `rgba(0,255,100,${na})`;
    ctx.font = "bold 22px monospace";
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    ctx.fillText("ALL TESTS PASSING", rcx, rcy + rs + 34);

    // Glow behind text
    const textGlow = ctx.createRadialGradient(rcx, rcy + rs + 34, 0, rcx, rcy + rs + 34, 80);
    textGlow.addColorStop(0, `rgba(0, 255, 100, ${na * 0.06})`);
    textGlow.addColorStop(1, "rgba(0, 255, 100, 0)");
    ctx.fillStyle = textGlow;
    ctx.beginPath();
    ctx.arc(rcx, rcy + rs + 34, 80, 0, Math.PI * 2);
    ctx.fill();
  }

  // Test result counters (animated counting)
  if (p > 0.55) {
    const cntA = Math.min(1, (p - 0.55) / 0.15);
    const passed = Math.round(47 * Math.min(1, (p - 0.55) / 0.3));
    hudText(ctx, w * 0.86, h * 0.30, `\u2713 ${passed}/47 PASSED`, cntA * 0.7, 14, "right");
    hudText(ctx, w * 0.86, h * 0.36, "0 FAILED  0 SKIPPED", cntA * 0.4, 11, "right");
    hudText(ctx, w * 0.14, h * 0.30, "DIFF +47 -14", cntA * 0.5, 14);
    hudText(ctx, w * 0.14, h * 0.36, "7 FILES CHANGED", cntA * 0.3, 11);
  }

  cockpitFrame(ctx, w, h);
  hudText(ctx, w * 0.5, h * 0.12, p > 0.3 ? "47/47 PASSED" : "RUNNING TESTS", p > 0.3 ? 0.6 : 0.4, 14, "center");

  // Blinking cursor
  if (Math.sin(time * 0.005) > 0) {
    const stxt = p > 0.3 ? "47/47 PASSED" : "RUNNING TESTS";
    ctx.font = "bold 14px monospace";
    const tw = ctx.measureText(stxt).width;
    hudText(ctx, w * 0.5 + tw / 2 + 6, h * 0.12, "\u2588", 0.4, 14, "left");
  }

  drawCRTGrain(ctx, w, h, time, 0.02);
  drawVignette(ctx, w, h, 0.3);

  hudText(ctx, 24, 22, "TESTS PASSING", 0.6, 14);
  hudText(ctx, 24, 40, "VALIDATING FIX", 0.25, 10);
  hudText(ctx, w - 24, 22, "STEP 5/6", 0.35, 11, "right");
}

// ── Scene 6: Return to Base ──────────────────────────────────

function drawReturnToBase(
  ctx: CanvasRenderingContext2D, w: number, h: number, p: number, time: number,
) {
  // Deep night sky
  const skyGrad = ctx.createLinearGradient(0, 0, 0, h);
  skyGrad.addColorStop(0, "#020408");
  skyGrad.addColorStop(0.5, "#060c14");
  skyGrad.addColorStop(1, "#0a1018");
  ctx.fillStyle = skyGrad;
  ctx.fillRect(0, 0, w, h);

  // Stars
  for (const star of STARS.slice(0, 70)) {
    const twinkle = 0.08 + 0.10 * Math.sin(time * 0.001 + star.x * 50);
    ctx.fillStyle = `rgba(255,255,255,${twinkle})`;
    ctx.beginPath();
    ctx.arc(star.x * w, star.y * h, star.s * 0.6, 0, Math.PI * 2);
    ctx.fill();
  }

  // ── Moon — centered in viewport ──
  const moonR = Math.min(w, h) * 0.22;
  const moonX = w * 0.5;
  const moonY = h * 0.5;

  // Moon glow (outer halo)
  const outerGlow = ctx.createRadialGradient(moonX, moonY, moonR * 0.8, moonX, moonY, moonR * 2.0);
  outerGlow.addColorStop(0, "rgba(180, 200, 230, 0.05)");
  outerGlow.addColorStop(0.4, "rgba(140, 170, 210, 0.025)");
  outerGlow.addColorStop(1, "rgba(100, 130, 180, 0)");
  ctx.fillStyle = outerGlow;
  ctx.beginPath();
  ctx.arc(moonX, moonY, moonR * 2.0, 0, Math.PI * 2);
  ctx.fill();

  // Moon body
  const moonGrad = ctx.createRadialGradient(
    moonX - moonR * 0.2, moonY - moonR * 0.15, moonR * 0.05,
    moonX + moonR * 0.1, moonY + moonR * 0.05, moonR,
  );
  moonGrad.addColorStop(0, "rgba(240, 238, 228, 0.95)");
  moonGrad.addColorStop(0.3, "rgba(220, 218, 208, 0.92)");
  moonGrad.addColorStop(0.6, "rgba(195, 195, 185, 0.88)");
  moonGrad.addColorStop(0.85, "rgba(165, 170, 160, 0.82)");
  moonGrad.addColorStop(1, "rgba(130, 140, 135, 0.70)");
  ctx.fillStyle = moonGrad;
  ctx.beginPath();
  ctx.arc(moonX, moonY, moonR, 0, Math.PI * 2);
  ctx.fill();

  // Moon craters (dark maria)
  const craters = [
    { dx: -0.25, dy: -0.20, r: 0.18, a: 0.08 },
    { dx: 0.10,  dy: -0.30, r: 0.12, a: 0.06 },
    { dx: -0.35, dy: 0.10,  r: 0.14, a: 0.07 },
    { dx: 0.20,  dy: 0.15,  r: 0.10, a: 0.05 },
    { dx: -0.05, dy: 0.30,  r: 0.08, a: 0.04 },
    { dx: 0.30,  dy: -0.05, r: 0.15, a: 0.06 },
    { dx: -0.15, dy: -0.40, r: 0.06, a: 0.04 },
    { dx: 0.35,  dy: 0.30,  r: 0.07, a: 0.03 },
    { dx: -0.40, dy: -0.05, r: 0.09, a: 0.05 },
    { dx: 0.05,  dy: 0.05,  r: 0.20, a: 0.05 },
  ];

  ctx.save();
  ctx.beginPath();
  ctx.arc(moonX, moonY, moonR, 0, Math.PI * 2);
  ctx.clip();

  for (const c of craters) {
    const cx = moonX + c.dx * moonR;
    const cy = moonY + c.dy * moonR;
    const cr = c.r * moonR;
    const crGrad = ctx.createRadialGradient(cx, cy, 0, cx, cy, cr);
    crGrad.addColorStop(0, `rgba(80, 85, 75, ${c.a})`);
    crGrad.addColorStop(0.6, `rgba(100, 105, 95, ${c.a * 0.5})`);
    crGrad.addColorStop(1, `rgba(120, 125, 115, 0)`);
    ctx.fillStyle = crGrad;
    ctx.beginPath();
    ctx.arc(cx, cy, cr, 0, Math.PI * 2);
    ctx.fill();
  }

  for (const c of craters.slice(0, 5)) {
    const cx = moonX + c.dx * moonR;
    const cy = moonY + c.dy * moonR;
    const cr = c.r * moonR;
    ctx.strokeStyle = `rgba(220, 220, 210, ${c.a * 0.4})`;
    ctx.lineWidth = 0.5;
    ctx.beginPath();
    ctx.arc(cx, cy, cr * 0.85, -Math.PI * 0.8, Math.PI * 0.2);
    ctx.stroke();
  }

  ctx.restore();

  // Limb darkening
  const limbGrad = ctx.createRadialGradient(moonX, moonY, moonR * 0.6, moonX, moonY, moonR);
  limbGrad.addColorStop(0, "rgba(0, 0, 0, 0)");
  limbGrad.addColorStop(0.85, "rgba(0, 0, 0, 0)");
  limbGrad.addColorStop(1, "rgba(0, 0, 0, 0.25)");
  ctx.fillStyle = limbGrad;
  ctx.beginPath();
  ctx.arc(moonX, moonY, moonR, 0, Math.PI * 2);
  ctx.fill();

  // ── Jet crossing the moon — bigger, the centrepiece ──
  const jetSize = Math.min(w, h) * 0.12;
  const pathStartX = moonX + moonR * 2.2;
  const pathEndX = moonX - moonR * 2.2;
  const jetX = pathStartX + (pathEndX - pathStartX) * p;
  const arcAmount = -moonR * 0.18;
  const jetY = moonY + arcAmount * Math.sin(p * Math.PI);
  drawP80Side(ctx, jetX, jetY, jetSize, Math.PI, 1, 0.02, { noShadow: true });

  // Engine contrail
  for (let i = 1; i <= 12; i++) {
    const tProgress = i / 12;
    const tAlpha = 0.05 * (1 - tProgress);
    const tx = jetX + i * jetSize * 0.15;
    const ty = jetY + tProgress * 1.5;
    const tSize = 2 + tProgress * 8;
    ctx.fillStyle = `rgba(200, 210, 230, ${tAlpha})`;
    ctx.beginPath();
    ctx.arc(tx, ty, tSize, 0, Math.PI * 2);
    ctx.fill();
  }

  drawVignette(ctx, w, h, 0.4);

  // ── HUD text — positioned well clear of moon ──
  hudText(ctx, 24, 22, "PR MERGED", 0.6, 14);
  hudText(ctx, 24, 40, "main \u2190 fix/null-ref-users", 0.25, 10);
  hudText(ctx, w - 24, 22, "STEP 6/6", 0.35, 11, "right");

  if (p > 0.35) {
    hudText(ctx, w * 0.5, h * 0.12, "PR MERGED", Math.min(1, (p - 0.35) / 0.2) * 0.8, 22, "center");
  }
  if (p > 0.6) {
    hudText(ctx, w * 0.5, h * 0.18, "ISSUE RESOLVED", Math.min(1, (p - 0.6) / 0.2) * 0.5, 12, "center");
  }
}

// ── Stable seeded data ───────────────────────────────────────

function pseudoRandom(seed: number): number {
  const x = Math.sin(seed * 127.1 + 311.7) * 43758.5453;
  return x - Math.floor(x);
}

const STARS = Array.from({ length: 80 }, (_, i) => ({
  x: pseudoRandom(i * 3 + 1),
  y: pseudoRandom(i * 3 + 2),
  s: 0.4 + pseudoRandom(i * 3 + 3) * 1.2,
}));

const CLOUDS = [
  { x: 0.15, y: 0.3, rx: 1.4, ry: 1.2, a: 0.18 },
  { x: 0.55, y: 0.5, rx: 1.0, ry: 1.0, a: 0.12 },
  { x: 0.85, y: 0.25, rx: 1.2, ry: 0.8, a: 0.14 },
  { x: 0.35, y: 0.6, rx: 0.8, ry: 1.1, a: 0.10 },
  { x: 1.05, y: 0.4, rx: 0.9, ry: 0.7, a: 0.11 },
];

const NOISE_BLIPS = Array.from({ length: 8 }, (_, i) => ({
  a: pseudoRandom(i * 5 + 100) * Math.PI * 2,
  d: 0.2 + pseudoRandom(i * 5 + 101) * 0.7,
}));
