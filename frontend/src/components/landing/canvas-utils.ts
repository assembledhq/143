// ── Shared canvas drawing utilities for landing page ─────────

export function pseudoRandom(seed: number): number {
  const x = Math.sin(seed * 127.1 + 311.7) * 43758.5453;
  return x - Math.floor(x);
}

export function hudText(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  text: string,
  alpha = 1,
  size = 14,
  align: CanvasTextAlign = "left",
) {
  ctx.fillStyle = `rgba(0, 255, 100, ${alpha})`;
  ctx.font = `bold ${size}px monospace`;
  ctx.textAlign = align;
  ctx.textBaseline = "middle";
  ctx.fillText(text, x, y);
}

export function drawVignette(
  ctx: CanvasRenderingContext2D,
  w: number,
  h: number,
  intensity = 0.4,
) {
  const cx = w * 0.5,
    cy = h * 0.5;
  const r = Math.max(w, h) * 0.7;
  const grad = ctx.createRadialGradient(cx, cy, r * 0.3, cx, cy, r);
  grad.addColorStop(0, "rgba(0,0,0,0)");
  grad.addColorStop(1, `rgba(0,0,0,${intensity})`);
  ctx.fillStyle = grad;
  ctx.fillRect(0, 0, w, h);
}

export function drawCRTGrain(
  ctx: CanvasRenderingContext2D,
  w: number,
  h: number,
  time: number,
  intensity = 0.03,
) {
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

export function drawHudPitchLadder(
  ctx: CanvasRenderingContext2D,
  w: number,
  h: number,
  alpha: number,
) {
  const cx = w * 0.5,
    cy = h * 0.42;
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

export function cockpitFrame(
  ctx: CanvasRenderingContext2D,
  w: number,
  h: number,
) {
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
    ctx.lineTo(
      sx + pseudoRandom(i * 17 + 2) * 30 - 15,
      sy + pseudoRandom(i * 17 + 3) * 6,
    );
    ctx.stroke();
  }

  // MFD screens (two green-tinted rectangles)
  const mfdW = w * 0.08,
    mfdH = h * 0.08;
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
    ctx.lineTo(
      w * gx + Math.cos(angle) * gaugeR * 0.8,
      gaugeY + Math.sin(angle) * gaugeR * 0.8,
    );
    ctx.stroke();
  }

  // Canopy struts
  ctx.strokeStyle = "rgba(20, 25, 35, 0.5)";
  ctx.lineWidth = 6;
  ctx.beginPath();
  ctx.moveTo(0, panelTop);
  ctx.lineTo(w * 0.1, 0);
  ctx.stroke();
  ctx.beginPath();
  ctx.moveTo(w, panelTop);
  ctx.lineTo(w * 0.9, 0);
  ctx.stroke();
  // Top bar
  ctx.lineWidth = 5;
  ctx.beginPath();
  ctx.moveTo(w * 0.1, 0);
  ctx.lineTo(w * 0.9, 0);
  ctx.stroke();

  // Center post (thin)
  ctx.strokeStyle = "rgba(20, 25, 35, 0.25)";
  ctx.lineWidth = 2;
  ctx.beginPath();
  ctx.moveTo(w * 0.5, 0);
  ctx.lineTo(w * 0.5, panelTop * 0.08);
  ctx.stroke();
}

// ── Stable seeded data ───────────────────────────────────────

export const STARS = Array.from({ length: 80 }, (_, i) => ({
  x: pseudoRandom(i * 3 + 1),
  y: pseudoRandom(i * 3 + 2),
  s: 0.4 + pseudoRandom(i * 3 + 3) * 1.2,
}));

export const CLOUDS = [
  { x: 0.15, y: 0.3, rx: 1.4, ry: 1.2, a: 0.18 },
  { x: 0.55, y: 0.5, rx: 1.0, ry: 1.0, a: 0.12 },
  { x: 0.85, y: 0.25, rx: 1.2, ry: 0.8, a: 0.14 },
  { x: 0.35, y: 0.6, rx: 0.8, ry: 1.1, a: 0.1 },
  { x: 1.05, y: 0.4, rx: 0.9, ry: 0.7, a: 0.11 },
];

export const NOISE_BLIPS = Array.from({ length: 8 }, (_, i) => ({
  a: pseudoRandom(i * 5 + 100) * Math.PI * 2,
  d: 0.2 + pseudoRandom(i * 5 + 101) * 0.7,
}));
