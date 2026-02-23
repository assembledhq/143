"use client";

import { useEffect, useRef } from "react";
import { ZONES, getActiveZone } from "./zones";
import { AIRFIELD_DARK, AIRFIELD_LIGHT, AirfieldTheme } from "./airfield-theme";

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

    const theme = isDark ? AIRFIELD_DARK : AIRFIELD_LIGHT;

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
      const pulse = Math.sin(time * 0.003) * 0.5 + 0.5; // 0-1 sine wave
      const scale = w / 1920; // scale factor relative to reference width

      ctx!.clearRect(0, 0, w, h);

      // 1. Terrain fill
      drawTerrain(ctx!, w, h, theme);

      // 2. Tarmac areas
      drawTarmac(ctx!, w, h, theme);

      // 3. Runways
      drawRunways(ctx!, w, h, theme);

      // 4. Runway markings
      drawMarkings(ctx!, w, h, theme);

      // 5. Buildings at zone positions
      drawBuildings(ctx!, w, h, theme, activeZone, pulse, scale);

      // 6. Zone glow on active zone
      drawZoneGlow(ctx!, w, h, theme, activeZone, pulse);

      // 7. Runway lights (dark mode only)
      if (isDark) {
        drawRunwayLights(ctx!, w, h, theme, activeZone);
      }

      // 8. Atmosphere
      drawAtmosphere(ctx!, w, h, isDark, time);

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

// ── Drawing helpers ──────────────────────────────────────────

function drawTerrain(ctx: CanvasRenderingContext2D, w: number, h: number, theme: AirfieldTheme) {
  ctx.fillStyle = theme.terrain;
  ctx.fillRect(0, 0, w, h);
}

function drawTarmac(ctx: CanvasRenderingContext2D, w: number, h: number, theme: AirfieldTheme) {
  ctx.fillStyle = theme.tarmac;
  // Central large tarmac
  ctx.fillRect(w * 0.1, h * 0.35, w * 0.5, h * 0.3);
  // Upper-right apron
  ctx.fillRect(w * 0.6, h * 0.15, w * 0.3, h * 0.25);
  // Lower-right apron
  ctx.fillRect(w * 0.55, h * 0.6, w * 0.35, h * 0.25);
}

function drawRunways(ctx: CanvasRenderingContext2D, w: number, h: number, theme: AirfieldTheme) {
  ctx.fillStyle = theme.runway;

  // Main horizontal runway across center
  const rwyH = h * 0.12;
  const rwyY = h * 0.44;
  ctx.fillRect(0, rwyY, w, rwyH);

  // Diagonal taxiway connecting upper-left to lower-right zones
  ctx.save();
  ctx.translate(w * 0.3, h * 0.2);
  ctx.rotate(Math.atan2(h * 0.6, w * 0.5));
  ctx.fillRect(0, -h * 0.03, Math.hypot(w * 0.5, h * 0.6), h * 0.06);
  ctx.restore();
}

function drawMarkings(ctx: CanvasRenderingContext2D, w: number, h: number, theme: AirfieldTheme) {
  const rwyY = h * 0.44;
  const rwyH = h * 0.12;
  const centerY = rwyY + rwyH / 2;

  ctx.strokeStyle = theme.markings;
  ctx.lineWidth = 2;

  // Dashed center line on main runway
  ctx.setLineDash([20, 15]);
  ctx.beginPath();
  ctx.moveTo(w * 0.05, centerY);
  ctx.lineTo(w * 0.95, centerY);
  ctx.stroke();
  ctx.setLineDash([]);

  // Threshold chevrons at left end
  drawChevrons(ctx, w * 0.04, centerY, 1, theme);
  // Threshold chevrons at right end
  drawChevrons(ctx, w * 0.96, centerY, -1, theme);

  // Taxiway edge dashed lines
  ctx.setLineDash([8, 12]);
  ctx.lineWidth = 1;
  // Top edge of runway
  ctx.beginPath();
  ctx.moveTo(0, rwyY);
  ctx.lineTo(w, rwyY);
  ctx.stroke();
  // Bottom edge of runway
  ctx.beginPath();
  ctx.moveTo(0, rwyY + rwyH);
  ctx.lineTo(w, rwyY + rwyH);
  ctx.stroke();
  ctx.setLineDash([]);
}

function drawChevrons(
  ctx: CanvasRenderingContext2D,
  cx: number,
  cy: number,
  dir: number, // 1 = pointing right, -1 = pointing left
  theme: AirfieldTheme
) {
  ctx.strokeStyle = theme.markings;
  ctx.lineWidth = 2;
  const size = 12;
  for (let i = -2; i <= 2; i++) {
    const y = cy + i * 10;
    ctx.beginPath();
    ctx.moveTo(cx, y);
    ctx.lineTo(cx + dir * size, y - size * 0.6);
    ctx.moveTo(cx, y);
    ctx.lineTo(cx + dir * size, y + size * 0.6);
    ctx.stroke();
  }
}

function drawBuildings(
  ctx: CanvasRenderingContext2D,
  w: number,
  h: number,
  theme: AirfieldTheme,
  activeZone: number,
  pulse: number,
  scale: number
) {
  const baseSize = Math.max(20, 50 * scale);

  ZONES.forEach((zone, i) => {
    const x = zone.position.x * w;
    const y = zone.position.y * h;
    const isActive = i === activeZone;

    ctx.fillStyle = theme.buildingFill;
    ctx.strokeStyle = isActive ? theme.dotActive : theme.buildingStroke;
    ctx.lineWidth = isActive ? 2 : 1;

    switch (i) {
      case 0: // Comms Tower — circle + antenna lines + pulsing rings
        drawCommsTower(ctx, x, y, baseSize, theme, isActive, pulse);
        break;
      case 1: // Briefing Room — rectangle + screen dots
        drawBriefingRoom(ctx, x, y, baseSize, theme, isActive, pulse);
        break;
      case 2: // Hangar — rounded rect + arch + plane silhouette
        drawHangar(ctx, x, y, baseSize, theme, isActive);
        break;
      case 3: // Test Strip — rectangle + checkmarks
        drawTestStrip(ctx, x, y, baseSize, theme, isActive, pulse);
        break;
      case 4: // Launch Pad — chevron/arrow + small plane
        drawLaunchPad(ctx, x, y, baseSize, theme, isActive);
        break;
      case 5: // Control Tower — narrow tall rect + wider observation deck
        drawControlTower(ctx, x, y, baseSize, theme, isActive);
        break;
    }
  });
}

function drawCommsTower(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  size: number,
  theme: AirfieldTheme,
  isActive: boolean,
  pulse: number
) {
  const r = size * 0.35;

  // Pulsing concentric rings when active
  if (isActive) {
    for (let ring = 1; ring <= 3; ring++) {
      const ringR = r + ring * 12 * (0.7 + pulse * 0.3);
      const alpha = 0.3 * (1 - ring / 4) * (0.5 + pulse * 0.5);
      ctx.beginPath();
      ctx.arc(x, y, ringR, 0, Math.PI * 2);
      ctx.strokeStyle = theme.zoneGlow(alpha);
      ctx.lineWidth = 1.5;
      ctx.stroke();
    }
  }

  // Base circle
  ctx.beginPath();
  ctx.arc(x, y, r, 0, Math.PI * 2);
  ctx.fillStyle = theme.buildingFill;
  ctx.fill();
  ctx.strokeStyle = isActive ? theme.dotActive : theme.buildingStroke;
  ctx.lineWidth = isActive ? 2 : 1;
  ctx.stroke();

  // Antenna lines radiating outward
  ctx.strokeStyle = isActive ? theme.dotActive : theme.buildingStroke;
  ctx.lineWidth = 1.5;
  const antennaAngles = [-Math.PI / 4, -Math.PI / 2, -Math.PI * 0.75, 0];
  for (const angle of antennaAngles) {
    ctx.beginPath();
    ctx.moveTo(x + Math.cos(angle) * r, y + Math.sin(angle) * r);
    ctx.lineTo(x + Math.cos(angle) * (r + size * 0.35), y + Math.sin(angle) * (r + size * 0.35));
    ctx.stroke();
  }
}

function drawBriefingRoom(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  size: number,
  theme: AirfieldTheme,
  isActive: boolean,
  pulse: number
) {
  const bw = size * 1.1;
  const bh = size * 0.7;

  ctx.fillStyle = theme.buildingFill;
  ctx.strokeStyle = isActive ? theme.dotActive : theme.buildingStroke;
  ctx.lineWidth = isActive ? 2 : 1;
  ctx.fillRect(x - bw / 2, y - bh / 2, bw, bh);
  ctx.strokeRect(x - bw / 2, y - bh / 2, bw, bh);

  // Glowing screen dots inside
  const dotR = 3;
  const cols = 3;
  const rows = 2;
  for (let r = 0; r < rows; r++) {
    for (let c = 0; c < cols; c++) {
      const dx = x - bw * 0.3 + c * (bw * 0.3);
      const dy = y - bh * 0.15 + r * (bh * 0.35);
      const alpha = isActive ? 0.6 + pulse * 0.4 : 0.3;
      ctx.beginPath();
      ctx.arc(dx, dy, dotR, 0, Math.PI * 2);
      ctx.fillStyle = isActive ? theme.zoneGlow(alpha) : theme.buildingStroke;
      ctx.fill();
    }
  }
}

function drawHangar(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  size: number,
  theme: AirfieldTheme,
  isActive: boolean
) {
  const bw = size * 1.4;
  const bh = size * 0.9;
  const cornerR = 6;

  // Rounded rectangle body
  ctx.fillStyle = theme.buildingFill;
  ctx.strokeStyle = isActive ? theme.dotActive : theme.buildingStroke;
  ctx.lineWidth = isActive ? 2 : 1;
  ctx.beginPath();
  ctx.roundRect(x - bw / 2, y - bh / 2, bw, bh, cornerR);
  ctx.fill();
  ctx.stroke();

  // Curved arch top
  ctx.beginPath();
  ctx.moveTo(x - bw / 2, y - bh / 2);
  ctx.quadraticCurveTo(x, y - bh / 2 - bh * 0.35, x + bw / 2, y - bh / 2);
  ctx.strokeStyle = isActive ? theme.dotActive : theme.buildingStroke;
  ctx.lineWidth = 2;
  ctx.stroke();

  // Small plane silhouette inside
  drawPlaneIcon(ctx, x, y + bh * 0.05, size * 0.35, isActive ? theme.dotActive : theme.buildingStroke);
}

function drawTestStrip(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  size: number,
  theme: AirfieldTheme,
  isActive: boolean,
  pulse: number
) {
  const bw = size * 1.1;
  const bh = size * 0.65;

  ctx.fillStyle = theme.buildingFill;
  ctx.strokeStyle = isActive ? theme.dotActive : theme.buildingStroke;
  ctx.lineWidth = isActive ? 2 : 1;
  ctx.fillRect(x - bw / 2, y - bh / 2, bw, bh);
  ctx.strokeRect(x - bw / 2, y - bh / 2, bw, bh);

  // Checkmarks inside
  ctx.strokeStyle = isActive ? theme.dotActive : theme.buildingStroke;
  ctx.lineWidth = 2;
  const checkPositions = [
    { cx: x - bw * 0.25, cy: y },
    { cx: x, cy: y },
    { cx: x + bw * 0.25, cy: y },
  ];
  for (let ci = 0; ci < checkPositions.length; ci++) {
    const cp = checkPositions[ci];
    const alpha = isActive ? 0.7 + pulse * 0.3 : 0.5;
    ctx.globalAlpha = alpha;
    const cs = size * 0.12;
    ctx.beginPath();
    ctx.moveTo(cp.cx - cs, cp.cy);
    ctx.lineTo(cp.cx - cs * 0.3, cp.cy + cs * 0.7);
    ctx.lineTo(cp.cx + cs, cp.cy - cs * 0.6);
    ctx.stroke();
  }
  ctx.globalAlpha = 1;
}

function drawLaunchPad(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  size: number,
  theme: AirfieldTheme,
  isActive: boolean
) {
  // Chevron / arrow shape pointing toward runway (rightward)
  const aw = size * 0.8;
  const ah = size * 0.9;

  ctx.fillStyle = theme.buildingFill;
  ctx.strokeStyle = isActive ? theme.dotActive : theme.buildingStroke;
  ctx.lineWidth = isActive ? 2 : 1;

  ctx.beginPath();
  ctx.moveTo(x - aw * 0.5, y - ah * 0.5);
  ctx.lineTo(x + aw * 0.5, y);
  ctx.lineTo(x - aw * 0.5, y + ah * 0.5);
  ctx.lineTo(x - aw * 0.15, y);
  ctx.closePath();
  ctx.fill();
  ctx.stroke();

  // Small plane in front of chevron
  drawPlaneIcon(ctx, x + aw * 0.6, y, size * 0.25, isActive ? theme.dotActive : theme.buildingStroke);
}

function drawControlTower(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  size: number,
  theme: AirfieldTheme,
  isActive: boolean
) {
  ctx.fillStyle = theme.buildingFill;
  ctx.strokeStyle = isActive ? theme.dotActive : theme.buildingStroke;
  ctx.lineWidth = isActive ? 2 : 1;

  // Narrow tall base
  const baseW = size * 0.35;
  const baseH = size * 1.0;
  ctx.fillRect(x - baseW / 2, y - baseH * 0.3, baseW, baseH);
  ctx.strokeRect(x - baseW / 2, y - baseH * 0.3, baseW, baseH);

  // Wider observation deck on top
  const deckW = size * 0.8;
  const deckH = size * 0.3;
  ctx.fillRect(x - deckW / 2, y - baseH * 0.3 - deckH, deckW, deckH);
  ctx.strokeRect(x - deckW / 2, y - baseH * 0.3 - deckH, deckW, deckH);

  // Window strip on observation deck
  if (isActive) {
    ctx.fillStyle = theme.zoneGlow(0.5);
  } else {
    ctx.fillStyle = theme.buildingStroke;
  }
  const winY = y - baseH * 0.3 - deckH * 0.6;
  ctx.fillRect(x - deckW * 0.35, winY, deckW * 0.7, deckH * 0.25);
}

function drawPlaneIcon(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  size: number,
  color: string
) {
  ctx.strokeStyle = color;
  ctx.lineWidth = 1.5;

  // Fuselage
  ctx.beginPath();
  ctx.moveTo(x - size, y);
  ctx.lineTo(x + size, y);
  ctx.stroke();

  // Wings
  ctx.beginPath();
  ctx.moveTo(x - size * 0.1, y);
  ctx.lineTo(x - size * 0.4, y - size * 0.7);
  ctx.moveTo(x - size * 0.1, y);
  ctx.lineTo(x - size * 0.4, y + size * 0.7);
  ctx.stroke();

  // Tail
  ctx.beginPath();
  ctx.moveTo(x - size * 0.85, y);
  ctx.lineTo(x - size, y - size * 0.35);
  ctx.moveTo(x - size * 0.85, y);
  ctx.lineTo(x - size, y + size * 0.35);
  ctx.stroke();

  // Nose
  ctx.beginPath();
  ctx.arc(x + size, y, 1.5, 0, Math.PI * 2);
  ctx.fillStyle = color;
  ctx.fill();
}

function drawZoneGlow(
  ctx: CanvasRenderingContext2D,
  w: number,
  h: number,
  theme: AirfieldTheme,
  activeZone: number,
  pulse: number
) {
  if (activeZone < 0) return;
  const zone = ZONES[activeZone];
  const cx = zone.position.x * w;
  const cy = zone.position.y * h;
  const radius = 120 + pulse * 20;

  const grad = ctx.createRadialGradient(cx, cy, 0, cx, cy, radius);
  grad.addColorStop(0, theme.zoneGlow(0.3));
  grad.addColorStop(1, theme.zoneGlow(0));
  ctx.fillStyle = grad;
  ctx.beginPath();
  ctx.arc(cx, cy, radius, 0, Math.PI * 2);
  ctx.fill();
}

function drawRunwayLights(
  ctx: CanvasRenderingContext2D,
  w: number,
  h: number,
  theme: AirfieldTheme,
  activeZone: number
) {
  const rwyY = h * 0.44;
  const rwyH = h * 0.12;
  const spacing = 30;
  const dotR = 2.5;

  const activeX = activeZone >= 0 ? ZONES[activeZone].position.x * w : -9999;
  const brightRadius = 150;

  for (let lx = spacing; lx < w; lx += spacing) {
    const dist = Math.abs(lx - activeX);
    const bright = dist < brightRadius;
    ctx.fillStyle = bright ? theme.runwayLight : theme.runwayLightDim;

    // Top edge
    ctx.beginPath();
    ctx.arc(lx, rwyY, dotR, 0, Math.PI * 2);
    ctx.fill();

    // Bottom edge
    ctx.beginPath();
    ctx.arc(lx, rwyY + rwyH, dotR, 0, Math.PI * 2);
    ctx.fill();
  }
}

// Stable seeded positions for atmosphere elements
const STAR_POSITIONS = Array.from({ length: 70 }, (_, i) => ({
  x: pseudoRandom(i * 3 + 1),
  y: pseudoRandom(i * 3 + 2),
  size: 0.5 + pseudoRandom(i * 3 + 3) * 1.5,
}));

const CLOUD_SHADOWS = [
  { x: 0.2, y: 0.15, rx: 120, ry: 40 },
  { x: 0.7, y: 0.8, rx: 100, ry: 35 },
  { x: 0.5, y: 0.3, rx: 80, ry: 30 },
];

function pseudoRandom(seed: number): number {
  const x = Math.sin(seed * 127.1 + 311.7) * 43758.5453;
  return x - Math.floor(x);
}

function drawAtmosphere(
  ctx: CanvasRenderingContext2D,
  w: number,
  h: number,
  isDark: boolean,
  time: number
) {
  if (isDark) {
    // Stars
    for (const star of STAR_POSITIONS) {
      const twinkle = 0.2 + 0.15 * Math.sin(time * 0.001 + star.x * 50);
      ctx.fillStyle = `rgba(255, 255, 255, ${twinkle})`;
      ctx.beginPath();
      ctx.arc(star.x * w, star.y * h, star.size, 0, Math.PI * 2);
      ctx.fill();
    }
  } else {
    // Subtle cloud shadows
    for (const cloud of CLOUD_SHADOWS) {
      ctx.fillStyle = "rgba(0, 0, 0, 0.03)";
      ctx.beginPath();
      ctx.ellipse(cloud.x * w, cloud.y * h, cloud.rx, cloud.ry, 0, 0, Math.PI * 2);
      ctx.fill();
    }
  }
}
