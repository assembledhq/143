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
  // Night sky
  const skyGrad = ctx.createLinearGradient(0, 0, 0, h);
  skyGrad.addColorStop(0, "#030610");
  skyGrad.addColorStop(0.4, "#081018");
  skyGrad.addColorStop(1, "#0e141c");
  ctx.fillStyle = skyGrad;
  ctx.fillRect(0, 0, w, h);

  // Stars
  for (const star of STARS.slice(0, 50)) {
    if (star.y > 0.50) continue;
    const twinkle = 0.08 + 0.1 * Math.sin(time * 0.001 + star.x * 50);
    ctx.fillStyle = `rgba(255, 255, 255, ${twinkle})`;
    ctx.beginPath(); ctx.arc(star.x * w, star.y * h, star.s * 0.7, 0, Math.PI * 2); ctx.fill();
  }

  // Ground
  const groundY = h * 0.52;
  const groundGrad = ctx.createLinearGradient(0, groundY, 0, h);
  groundGrad.addColorStop(0, "#0c1410");
  groundGrad.addColorStop(1, "#081008");
  ctx.fillStyle = groundGrad;
  ctx.fillRect(0, groundY, w, h - groundY);

  // Ground texture — grass/dirt patches
  for (let i = 0; i < 30; i++) {
    const gx = pseudoRandom(i * 13 + 200) * w;
    const gy = groundY + pseudoRandom(i * 13 + 201) * (h - groundY) * 0.8;
    const gs = 8 + pseudoRandom(i * 13 + 202) * 20;
    ctx.fillStyle = `rgba(${pseudoRandom(i * 13 + 203) > 0.5 ? "8,18,10" : "12,10,6"}, ${0.15 + pseudoRandom(i * 13 + 204) * 0.1})`;
    ctx.beginPath();
    ctx.ellipse(gx, gy, gs, gs * 0.4, pseudoRandom(i * 13 + 205) * Math.PI, 0, Math.PI * 2);
    ctx.fill();
  }

  // Treeline silhouette along the horizon
  ctx.fillStyle = "rgba(4, 12, 6, 0.7)";
  ctx.beginPath();
  ctx.moveTo(0, groundY + 2);
  for (let tx = 0; tx <= w; tx += 6) {
    const treeH = 4 + pseudoRandom(tx * 0.1 + 500) * 10 + Math.sin(tx * 0.03) * 3;
    ctx.lineTo(tx, groundY - treeH);
  }
  ctx.lineTo(w, groundY + 2);
  ctx.closePath();
  ctx.fill();

  // Runway
  const rwyY = h * 0.64, rwyH = h * 0.045;
  const rwyX0 = w * 0.18, rwyX1 = w * 0.92;
  ctx.fillStyle = "#1c1e22";
  ctx.fillRect(rwyX0, rwyY, rwyX1 - rwyX0, rwyH);

  // Runway shoulder
  ctx.fillStyle = "#141618";
  ctx.fillRect(rwyX0, rwyY - 2, rwyX1 - rwyX0, 2);
  ctx.fillRect(rwyX0, rwyY + rwyH, rwyX1 - rwyX0, 2);

  // Centerline dashes
  ctx.setLineDash([14, 12]);
  ctx.strokeStyle = "rgba(255,255,255,0.35)";
  ctx.lineWidth = 1.5;
  ctx.beginPath();
  ctx.moveTo(rwyX0 + 30, rwyY + rwyH / 2);
  ctx.lineTo(rwyX1 - 30, rwyY + rwyH / 2);
  ctx.stroke();
  ctx.setLineDash([]);

  // Threshold bars
  for (const tx of [rwyX0 + 8, rwyX1 - 8]) {
    ctx.fillStyle = "rgba(255,255,255,0.4)";
    const barH = rwyH * 0.06;
    for (let i = 0; i < 6; i++) {
      const by = rwyY + (rwyH - 6 * barH - 5 * barH) / 2 + i * barH * 2;
      ctx.fillRect(tx - 8, by, 16, barH);
    }
  }

  // Runway numbers
  ctx.fillStyle = "rgba(255,255,255,0.3)";
  ctx.font = `bold ${Math.max(10, rwyH * 0.45)}px monospace`;
  ctx.textAlign = "center";
  ctx.textBaseline = "middle";
  ctx.fillText("09", rwyX0 + 40, rwyY + rwyH / 2);
  ctx.fillText("27", rwyX1 - 40, rwyY + rwyH / 2);

  // Runway edge lights
  for (let lx = rwyX0; lx <= rwyX1; lx += w * 0.02) {
    ctx.fillStyle = "rgba(255,230,180,0.5)";
    ctx.beginPath(); ctx.arc(lx, rwyY - 1, 2, 0, Math.PI * 2); ctx.fill();
    ctx.beginPath(); ctx.arc(lx, rwyY + rwyH + 1, 2, 0, Math.PI * 2); ctx.fill();
  }

  // Threshold lights
  for (let ty = rwyY + 4; ty < rwyY + rwyH - 2; ty += 6) {
    ctx.fillStyle = "rgba(0, 220, 80, 0.5)";
    ctx.beginPath(); ctx.arc(rwyX0 - 3, ty, 2, 0, Math.PI * 2); ctx.fill();
    ctx.fillStyle = "rgba(220, 50, 30, 0.5)";
    ctx.beginPath(); ctx.arc(rwyX1 + 3, ty, 2, 0, Math.PI * 2); ctx.fill();
  }

  // Taxiway from hangar area to runway
  const twyX = w * 0.16, twyW = w * 0.025;
  ctx.fillStyle = "#181a1e";
  ctx.fillRect(twyX, groundY + h * 0.02, twyW, rwyY - groundY - h * 0.02);
  ctx.setLineDash([6, 5]);
  ctx.strokeStyle = "rgba(220, 200, 50, 0.35)";
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(twyX + twyW / 2, groundY + h * 0.04);
  ctx.lineTo(twyX + twyW / 2, rwyY);
  ctx.stroke();
  ctx.setLineDash([]);
  // Blue taxiway edge lights
  for (let ty = groundY + h * 0.04; ty < rwyY; ty += 12) {
    ctx.fillStyle = "rgba(50, 100, 255, 0.4)";
    ctx.beginPath(); ctx.arc(twyX - 1, ty, 1.5, 0, Math.PI * 2); ctx.fill();
    ctx.beginPath(); ctx.arc(twyX + twyW + 1, ty, 1.5, 0, Math.PI * 2); ctx.fill();
  }

  // ── Buildings on LEFT side ──

  // Hangar (main, large)
  const hx = w * 0.03, hy = groundY + h * 0.01, hw = w * 0.10, hh = h * 0.08;

  // Building shadows (cast to the right)
  ctx.fillStyle = "rgba(0, 0, 0, 0.15)";
  ctx.beginPath();
  ctx.moveTo(hx + hw, hy);
  ctx.lineTo(hx + hw + hw * 0.3, hy + hh * 0.15);
  ctx.lineTo(hx + hw + hw * 0.3, hy + hh + hh * 0.15);
  ctx.lineTo(hx + hw, hy + hh);
  ctx.closePath();
  ctx.fill();

  ctx.fillStyle = "#14181e";
  ctx.fillRect(hx, hy, hw, hh);
  // Roof ridge
  ctx.strokeStyle = "rgba(80, 100, 120, 0.2)";
  ctx.lineWidth = 1;
  ctx.beginPath(); ctx.moveTo(hx, hy); ctx.lineTo(hx + hw / 2, hy - 4); ctx.lineTo(hx + hw, hy); ctx.stroke();

  // Doors opening
  const doorOpen = Math.min(1, Math.max(0, (p - 0.15) / 0.3));
  if (doorOpen < 1) {
    ctx.fillStyle = "#181c22";
    const dw = hw * 0.48;
    ctx.fillRect(hx, hy, dw * (1 - doorOpen), hh);
    ctx.fillRect(hx + hw - dw * (1 - doorOpen), hy, dw * (1 - doorOpen), hh);
  }
  if (doorOpen > 0) {
    ctx.fillStyle = `rgba(255,200,100,${doorOpen * 0.12})`;
    ctx.fillRect(hx, hy, hw, hh);
  }
  ctx.strokeStyle = "rgba(80, 100, 120, 0.15)";
  ctx.lineWidth = 1;
  ctx.strokeRect(hx, hy, hw, hh);

  // Ops building
  const obx = w * 0.04, oby = hy + hh + h * 0.015, obw = w * 0.05, obh = h * 0.04;
  ctx.fillStyle = "#12161c";
  ctx.fillRect(obx, oby, obw, obh);
  ctx.strokeStyle = "rgba(80, 100, 120, 0.12)";
  ctx.strokeRect(obx, oby, obw, obh);
  for (let wx = obx + 4; wx < obx + obw - 4; wx += 8) {
    ctx.fillStyle = `rgba(255, 220, 120, ${0.15 + 0.1 * Math.sin(time * 0.002 + wx)})`;
    ctx.fillRect(wx, oby + 3, 4, 3);
  }

  // Control tower (small)
  const twx = w * 0.11, twy = groundY - h * 0.02, tww = w * 0.012, twh = h * 0.06;
  ctx.fillStyle = "#141820";
  ctx.fillRect(twx, twy, tww, twh);
  ctx.fillRect(twx - tww * 0.5, twy - h * 0.015, tww * 2, h * 0.015);
  ctx.fillStyle = "rgba(0, 255, 100, 0.12)";
  ctx.fillRect(twx - tww * 0.3, twy - h * 0.012, tww * 1.6, h * 0.006);

  // Fuel depot
  ctx.fillStyle = "#101418";
  ctx.fillRect(w * 0.035, oby + obh + h * 0.01, w * 0.025, h * 0.025);
  ctx.strokeStyle = "rgba(80, 100, 120, 0.1)";
  ctx.strokeRect(w * 0.035, oby + obh + h * 0.01, w * 0.025, h * 0.025);

  // Floodlight cones
  for (const flx of [w * 0.08, w * 0.14]) {
    const fly = groundY - h * 0.01;
    const coneGrad = ctx.createRadialGradient(flx, fly, 0, flx, fly, h * 0.08);
    coneGrad.addColorStop(0, `rgba(255, 240, 200, ${p > 0.1 ? 0.06 : 0.02})`);
    coneGrad.addColorStop(1, "rgba(255, 240, 200, 0)");
    ctx.fillStyle = coneGrad;
    ctx.beginPath(); ctx.arc(flx, fly + h * 0.03, h * 0.08, 0, Math.PI * 2); ctx.fill();
  }

  // Jet rolling from hangar to runway — facing RIGHT
  if (p > 0.3) {
    const jp = Math.min(1, (p - 0.3) / 0.6);
    let jx: number, jy: number, jetAngle: number;
    if (jp < 0.2) {
      // Phase 1 (quick): taxi from hangar to taxiway
      const t = jp / 0.2;
      jx = hx + hw + t * (twyX + twyW / 2 - hx - hw);
      jy = hy + hh * 0.5;
      jetAngle = 0;
    } else if (jp < 0.5) {
      // Phase 2: down taxiway
      const t = (jp - 0.2) / 0.3;
      jx = twyX + twyW / 2;
      jy = hy + hh * 0.5 + t * (rwyY + rwyH / 2 - hy - hh * 0.5);
      jetAngle = Math.PI / 2;
    } else {
      // Phase 3: already turned onto runway, roll to takeoff position
      const t = (jp - 0.5) / 0.5;
      jx = twyX + twyW / 2 + t * (rwyX0 + w * 0.15 - twyX - twyW / 2);
      jy = rwyY + rwyH / 2;
      jetAngle = 0; // instant turn, now facing right down the runway
    }
    drawP80(ctx, jx, jy, Math.min(w, h) * 0.035, jetAngle, 0.25);

    // Engine glow trail
    if (jp > 0.2) {
      const glowGrad = ctx.createRadialGradient(jx, jy, 0, jx, jy, 20);
      glowGrad.addColorStop(0, "rgba(255, 140, 40, 0.08)");
      glowGrad.addColorStop(1, "rgba(255, 140, 40, 0)");
      ctx.fillStyle = glowGrad;
      ctx.beginPath(); ctx.arc(jx, jy, 20, 0, Math.PI * 2); ctx.fill();
    }
  }

  // Alarm flashers
  if (p > 0.05) {
    const fa = (Math.sin(time * 0.008) > 0 ? 0.7 : 0.15) * Math.min(1, p / 0.15);
    for (const ax of [w * 0.25, w * 0.5, w * 0.75]) {
      ctx.fillStyle = `rgba(255,40,20,${fa})`;
      ctx.beginPath(); ctx.arc(ax, groundY + h * 0.015, 4, 0, Math.PI * 2); ctx.fill();
      const alGlow = ctx.createRadialGradient(ax, groundY + h * 0.015, 0, ax, groundY + h * 0.015, 25);
      alGlow.addColorStop(0, `rgba(255,40,20,${fa * 0.2})`);
      alGlow.addColorStop(1, "rgba(255,40,20,0)");
      ctx.fillStyle = alGlow;
      ctx.beginPath(); ctx.arc(ax, groundY + h * 0.015, 25, 0, Math.PI * 2); ctx.fill();
    }
  }

  // Horizon haze and vignette
  drawHorizonHaze(ctx, w, h, groundY);
  drawVignette(ctx, w, h, 0.35);

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
  const skyGrad = ctx.createLinearGradient(0, 0, 0, h);
  skyGrad.addColorStop(0, "#020510");
  skyGrad.addColorStop(1, "#081020");
  ctx.fillStyle = skyGrad;
  ctx.fillRect(0, 0, w, h);

  // Drifting clouds
  for (const c of CLOUDS) {
    const drift = time * 0.00003;
    const ccx = ((c.x + drift) % 1.3 - 0.15) * w;
    ctx.fillStyle = `rgba(15,22,40,${c.a})`;
    ctx.beginPath();
    ctx.ellipse(ccx, c.y * h, c.rx * w * 0.12, c.ry * h * 0.06, 0, 0, Math.PI * 2);
    ctx.fill();
  }

  // Pitch ladder
  drawHudPitchLadder(ctx, w, h, 0.6);

  const rcx = w * 0.5, rcy = h * 0.42;
  const rs = Math.min(w, h) * 0.13;

  // Reticle circles
  ctx.strokeStyle = "rgba(0,255,100,0.5)";
  ctx.lineWidth = 1.5;
  ctx.beginPath(); ctx.arc(rcx, rcy, rs, 0, Math.PI * 2); ctx.stroke();
  ctx.beginPath(); ctx.arc(rcx, rcy, rs * 0.5, 0, Math.PI * 2); ctx.stroke();
  ctx.beginPath(); ctx.arc(rcx, rcy, rs * 0.25, 0, Math.PI * 2); ctx.stroke();

  // Crosshairs with gap
  const gap = rs * 0.15;
  ctx.beginPath();
  ctx.moveTo(rcx - rs * 1.2, rcy); ctx.lineTo(rcx - gap, rcy);
  ctx.moveTo(rcx + gap, rcy); ctx.lineTo(rcx + rs * 1.2, rcy);
  ctx.moveTo(rcx, rcy - rs * 1.2); ctx.lineTo(rcx, rcy - gap);
  ctx.moveTo(rcx, rcy + gap); ctx.lineTo(rcx, rcy + rs * 1.2);
  ctx.stroke();

  // Bogie diamond converging toward center
  const bogieX = rcx + (1 - p) * w * 0.18;
  const bogieY = rcy + (1 - p) * h * -0.07;
  const ds = 10 + (1 - p) * 8;
  const locked = p > 0.7;
  ctx.strokeStyle = locked ? "rgba(255,60,40,0.9)" : "rgba(0,255,100,0.7)";
  ctx.lineWidth = 2;
  ctx.beginPath();
  ctx.moveTo(bogieX, bogieY - ds);
  ctx.lineTo(bogieX + ds, bogieY);
  ctx.lineTo(bogieX, bogieY + ds);
  ctx.lineTo(bogieX - ds, bogieY);
  ctx.closePath();
  ctx.stroke();

  // Target info box
  if (p > 0.3) {
    const tia = Math.min(1, (p - 0.3) / 0.2);
    ctx.fillStyle = `rgba(0, 255, 100, ${tia * 0.5})`;
    ctx.font = "11px monospace";
    ctx.textAlign = "left";
    ctx.textBaseline = "middle";
    ctx.fillText("src/handlers/user.ts", bogieX + ds + 8, bogieY - 6);
    ctx.fillText(`line ${Math.round(142 - p * 5)}`, bogieX + ds + 8, bogieY + 8);
  }

  // Diff counter (bumped)
  const linesChanged = Math.round(12 + p * 35);
  hudText(ctx, w * 0.86, h * 0.30, `DIFF +${linesChanged} -${Math.round(linesChanged * 0.3)}`, 0.7, 15, "right");
  hudText(ctx, w * 0.86, h * 0.35, `${Math.round(3 + p * 4)} FILES`, 0.4, 11, "right");

  // Lock indicator
  if (locked) {
    const la = Math.min(1, (p - 0.7) / 0.15);
    const blink = Math.sin(time * 0.01) > 0 ? 1 : 0.5;
    ctx.fillStyle = `rgba(255,60,40,${la * blink})`;
    ctx.font = "bold 18px monospace";
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    ctx.fillText("PATCH READY", rcx, rcy + rs + 28);

    const bs = ds + 6;
    ctx.strokeStyle = `rgba(255,60,40,${la})`;
    ctx.lineWidth = 2;
    ctx.strokeRect(bogieX - bs, bogieY - bs, bs * 2, bs * 2);
  }

  cockpitFrame(ctx, w, h);
  hudText(ctx, w * 0.14, h * 0.30, "BUILDING FIX", 0.6, 15);
  hudText(ctx, w * 0.14, h * 0.35, "SANDBOX ACTIVE", 0.3, 11);
  hudText(ctx, w * 0.5, h * 0.12, "GENERATING PATCH", 0.5, 14, "center");

  // Blinking cursor
  if (Math.sin(time * 0.005) > 0) {
    ctx.font = "bold 14px monospace";
    const tw = ctx.measureText("GENERATING PATCH").width;
    hudText(ctx, w * 0.5 + tw / 2 + 6, h * 0.12, "\u2588", 0.4, 14, "left");
  }

  // Heading tape
  ctx.strokeStyle = "rgba(0, 255, 100, 0.2)";
  ctx.lineWidth = 1;
  ctx.beginPath(); ctx.moveTo(w * 0.3, h * 0.06); ctx.lineTo(w * 0.7, h * 0.06); ctx.stroke();
  hudText(ctx, w * 0.5, h * 0.06, "\u25BD", 0.4, 12, "center");

  drawCRTGrain(ctx, w, h, time, 0.02);
  drawVignette(ctx, w, h, 0.3);

  hudText(ctx, 24, 22, "FIX GENERATED", 0.6, 14);
  hudText(ctx, 24, 40, "BUILDING PATCH", 0.25, 10);
  hudText(ctx, w - 24, 22, "STEP 4/6", 0.35, 11, "right");
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
  const pathStartX = moonX + moonR * 1.4;
  const pathEndX = moonX - moonR * 1.4;
  const jetX = pathStartX + (pathEndX - pathStartX) * p;
  const arcAmount = -moonR * 0.12;
  const jetY = moonY + arcAmount * Math.sin(p * Math.PI);
  drawP80Side(ctx, jetX, jetY, jetSize, Math.PI, 1, 0.02);

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
