"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { Sun, Moon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Slider } from "@/components/ui/slider";

// ── Types ──────────────────────────────────────────────────────────────────────

interface Plane {
  id: number;
  x: number;
  y: number;
  vx: number;
  vy: number;
  size: number;
  trail: Array<{ x: number; y: number }>;
  maxTrailLength: number;
  life: number;
  maxLife: number;
  opacity: number;
}

let nextPlaneId = 0;

interface BackgroundStar {
  x: number;
  y: number;
  size: number;
  baseOpacity: number;
  twinkleSpeed: number;
  twinklePhase: number;
  layer: number;
}

interface Cloud {
  x: number;
  y: number;
  vx: number;
  layer: number;
  blobs: Array<{ ox: number; oy: number; rx: number; ry: number; opacity: number }>;
}

interface Formation {
  active: boolean;
  startTime: number;
  duration: number;
  originX: number;
  originY: number;
  heading: number;
  speed: number;
  offsets: Array<{ dx: number; dy: number }>;
  slotMap: Map<number, number>;
}

// ── Theme colors ───────────────────────────────────────────────────────────────

const DARK = {
  bg: "#08080f",
  planeFill: (a: number) => `rgba(210, 218, 230, ${a})`,
  planeStroke: (a: number) => `rgba(255, 255, 255, ${a * 0.3})`,
  trail: (a: number) => `rgba(180, 190, 210, ${a})`,
  exhaust: (a: number) => `rgba(200, 215, 240, ${a})`,
  connection: (a: number) => `rgba(200, 200, 220, ${a})`,
  orbs: [
    { color: "rgba(30, 40, 80, 0.15)" },
    { color: "rgba(50, 30, 70, 0.1)" },
  ],
};

const LIGHT = {
  bg: "#d4e6f5",
  planeFill: (a: number) => `rgba(50, 55, 65, ${a})`,
  planeStroke: (a: number) => `rgba(30, 30, 40, ${a * 0.3})`,
  trail: (a: number) => `rgba(255, 255, 255, ${a})`,
  exhaust: (a: number) => `rgba(255, 255, 255, ${a})`,
  connection: (a: number) => `rgba(80, 80, 100, ${a})`,
  orbs: [
    { color: "rgba(255, 255, 255, 0.3)" },
    { color: "rgba(200, 220, 255, 0.2)" },
  ],
};

// ── Responsive helpers ─────────────────────────────────────────────────────────

function getResponsiveConfig(w: number) {
  if (w < 640) {
    return { maxPlanes: 5, starCount: 100, cloudCount: 6, planeSizeMin: 5, planeSizeRange: 4, spawnRate: 0.008 };
  }
  if (w < 1024) {
    return { maxPlanes: 8, starCount: 160, cloudCount: 9, planeSizeMin: 6, planeSizeRange: 5, spawnRate: 0.01 };
  }
  return { maxPlanes: 12, starCount: 250, cloudCount: 12, planeSizeMin: 8, planeSizeRange: 6, spawnRate: 0.012 };
}

// ── Constants ──────────────────────────────────────────────────────────────────

const MOUSE_GRAVITY_STRENGTH = 500;
const LOGO_REPEL_RADIUS = 180;
const LOGO_REPEL_STRENGTH = 0.12;
const FORMATION_DURATION = 5000;
const BG_COLORS_DARK = "#08080f";

// ── Helpers ────────────────────────────────────────────────────────────────────

function createBackgroundStar(w: number, h: number): BackgroundStar {
  const layer = Math.random() < 0.35 ? 0 : Math.random() < 0.55 ? 1 : 2;
  const sizeByLayer = [0.4, 0.9, 1.8][layer];
  return {
    x: Math.random() * w,
    y: Math.random() * h,
    size: Math.random() * sizeByLayer + 0.2,
    baseOpacity: [0.12, 0.3, 0.55][layer] + Math.random() * 0.1,
    twinkleSpeed: Math.random() * 0.02 + 0.003,
    twinklePhase: Math.random() * Math.PI * 2,
    layer,
  };
}

function createCloud(w: number, h: number): Cloud {
  const layer = Math.random() < 0.3 ? 0 : Math.random() < 0.5 ? 1 : 2;
  const baseWidth = [120, 80, 50][layer];
  const width = baseWidth + Math.random() * baseWidth * 0.6;
  const blobCount = Math.floor(Math.random() * 4) + 4;
  const blobs: Cloud["blobs"] = [];

  for (let i = 0; i < blobCount; i++) {
    blobs.push({
      ox: (Math.random() - 0.5) * width * 0.8,
      oy: (Math.random() - 0.5) * width * 0.25,
      rx: width * (0.2 + Math.random() * 0.25),
      ry: width * (0.1 + Math.random() * 0.12),
      opacity: [0.25, 0.35, 0.5][layer] + Math.random() * 0.1,
    });
  }

  return {
    x: Math.random() * (w + 400) - 200,
    y: Math.random() * h * 0.85 + h * 0.05,
    vx: [0.08, 0.15, 0.25][layer] * (0.8 + Math.random() * 0.4),
    layer,
    blobs,
  };
}

function spawnPlane(w: number, h: number, sizeMin: number, sizeRange: number): Plane {
  const edge = Math.floor(Math.random() * 4);
  let x: number, y: number;
  switch (edge) {
    case 0: x = Math.random() * w; y = -40; break;
    case 1: x = w + 40; y = Math.random() * h; break;
    case 2: x = Math.random() * w; y = h + 40; break;
    default: x = -40; y = Math.random() * h; break;
  }
  const targetX = w / 2 + (Math.random() - 0.5) * w * 0.6;
  const targetY = h / 2 + (Math.random() - 0.5) * h * 0.6;
  const angle = Math.atan2(targetY - y, targetX - x);
  const spd = Math.random() * 1.2 + 0.5;

  return {
    id: nextPlaneId++,
    x, y,
    vx: Math.cos(angle) * spd,
    vy: Math.sin(angle) * spd,
    size: Math.random() * sizeRange + sizeMin,
    trail: [],
    maxTrailLength: Math.floor(Math.random() * 50 + 40),
    life: 0,
    maxLife: Math.random() * 600 + 400,
    opacity: 1,
  };
}

function getFormationOffsets(
  heading: number, count: number,
): Array<{ dx: number; dy: number }> {
  const spacing = 45;
  const offsets: Array<{ dx: number; dy: number }> = [];
  offsets.push({ dx: 0, dy: 0 });

  for (let i = 1; i < count; i++) {
    const row = Math.ceil(i / 2);
    const side = i % 2 === 0 ? 1 : -1;
    const back = row * spacing;
    const lateral = row * spacing * 0.6 * side;
    offsets.push({
      dx: -back * Math.cos(heading) - lateral * Math.sin(heading),
      dy: -back * Math.sin(heading) + lateral * Math.cos(heading),
    });
  }
  return offsets;
}

// ── Draw P-80 ──────────────────────────────────────────────────────────────────

function drawP80(
  ctx: CanvasRenderingContext2D,
  x: number, y: number, angle: number, size: number, alpha: number,
  fillFn: (a: number) => string, strokeFn: (a: number) => string,
) {
  ctx.save();
  ctx.translate(x, y);
  ctx.rotate(angle);
  ctx.globalAlpha = alpha;

  const s = size;
  ctx.beginPath();
  ctx.moveTo(s * 1.3, 0);
  ctx.lineTo(s * 0.4, -s * 0.1);
  ctx.lineTo(s * 0.15, -s * 0.12);
  ctx.lineTo(-s * 0.05, -s * 0.65);
  ctx.lineTo(-s * 0.15, -s * 0.7);
  ctx.lineTo(-s * 0.25, -s * 0.65);
  ctx.lineTo(-s * 0.15, -s * 0.13);
  ctx.lineTo(-s * 0.5, -s * 0.1);
  ctx.lineTo(-s * 0.75, -s * 0.32);
  ctx.lineTo(-s * 0.85, -s * 0.34);
  ctx.lineTo(-s * 0.9, -s * 0.28);
  ctx.lineTo(-s * 0.7, -s * 0.09);
  ctx.lineTo(-s * 1.0, -s * 0.06);
  ctx.lineTo(-s * 1.0, s * 0.06);
  ctx.lineTo(-s * 0.7, s * 0.09);
  ctx.lineTo(-s * 0.9, s * 0.28);
  ctx.lineTo(-s * 0.85, s * 0.34);
  ctx.lineTo(-s * 0.75, s * 0.32);
  ctx.lineTo(-s * 0.5, s * 0.1);
  ctx.lineTo(-s * 0.15, s * 0.13);
  ctx.lineTo(-s * 0.25, s * 0.65);
  ctx.lineTo(-s * 0.15, s * 0.7);
  ctx.lineTo(-s * 0.05, s * 0.65);
  ctx.lineTo(s * 0.15, s * 0.12);
  ctx.lineTo(s * 0.4, s * 0.1);
  ctx.closePath();

  ctx.fillStyle = fillFn(alpha);
  ctx.fill();
  ctx.strokeStyle = strokeFn(alpha);
  ctx.lineWidth = 0.5;
  ctx.stroke();

  ctx.globalAlpha = 1;
  ctx.restore();
}

// ── Draw exhaust shimmer ───────────────────────────────────────────────────────

function drawExhaustShimmer(
  ctx: CanvasRenderingContext2D,
  x: number, y: number, heading: number, size: number, alpha: number, time: number,
  colorFn: (a: number) => string,
) {
  const cosH = Math.cos(heading);
  const sinH = Math.sin(heading);
  const tailX = x - cosH * size * 1.05;
  const tailY = y - sinH * size * 1.05;

  for (let j = 0; j < 4; j++) {
    const wobbleX = Math.sin(time * 0.008 + j * 1.8) * 2.5;
    const wobbleY = Math.cos(time * 0.01 + j * 2.3) * 2.5;
    const dist = j * 5;
    const sx = tailX - cosH * dist + sinH * wobbleX + wobbleY * 0.5;
    const sy = tailY - sinH * dist - cosH * wobbleX + wobbleY * 0.5;
    const r = 2.5 - j * 0.4;
    const a = alpha * (0.18 - j * 0.04);

    if (a > 0) {
      const grad = ctx.createRadialGradient(sx, sy, 0, sx, sy, r * 2);
      grad.addColorStop(0, colorFn(a));
      grad.addColorStop(1, colorFn(0));
      ctx.beginPath();
      ctx.arc(sx, sy, r * 2, 0, Math.PI * 2);
      ctx.fillStyle = grad;
      ctx.fill();
    }
  }
}

// ── Draw cloud ─────────────────────────────────────────────────────────────────

function drawCloud(ctx: CanvasRenderingContext2D, cloud: Cloud, offsetX: number, offsetY: number) {
  for (const b of cloud.blobs) {
    const bx = cloud.x + b.ox + offsetX;
    const by = cloud.y + b.oy + offsetY;

    // Subtle shadow
    ctx.beginPath();
    ctx.ellipse(bx, by + b.ry * 0.15, b.rx, b.ry, 0, 0, Math.PI * 2);
    ctx.fillStyle = `rgba(160, 175, 200, ${b.opacity * 0.15})`;
    ctx.fill();

    // Cloud body
    ctx.beginPath();
    ctx.ellipse(bx, by, b.rx, b.ry, 0, 0, Math.PI * 2);
    ctx.fillStyle = `rgba(255, 255, 255, ${b.opacity})`;
    ctx.fill();
  }
}

// ── Draw light-mode sky gradient ───────────────────────────────────────────────

function drawSkyGradient(ctx: CanvasRenderingContext2D, w: number, h: number) {
  const grad = ctx.createLinearGradient(0, 0, 0, h);
  grad.addColorStop(0, "#87BBDF");
  grad.addColorStop(0.4, "#A8CEE4");
  grad.addColorStop(0.8, "#C9DFF0");
  grad.addColorStop(1, "#DAE8F2");
  ctx.fillStyle = grad;
  ctx.fillRect(0, 0, w, h);
}

// ── Component ──────────────────────────────────────────────────────────────────

export default function LandingPage() {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const bgStarsRef = useRef<BackgroundStar[]>([]);
  const cloudsRef = useRef<Cloud[]>([]);
  const planesRef = useRef<Plane[]>([]);
  const mouseRef = useRef({ x: -9999, y: -9999, active: false });
  const speedRef = useRef(1);
  const dimsRef = useRef({ w: 0, h: 0 });
  const configRef = useRef(getResponsiveConfig(typeof window !== "undefined" ? window.innerWidth : 1200));
  const formationRef = useRef<Formation>({ active: false, startTime: 0, duration: FORMATION_DURATION, originX: 0, originY: 0, heading: 0, speed: 1.5, offsets: [], slotMap: new Map() });
  const isDarkRef = useRef(true);
  const [isDark, setIsDark] = useState(true);
  const manualThemeRef = useRef<boolean | null>(null);
  const [speed, setSpeed] = useState(12);

  // Detect system color scheme
  useEffect(() => {
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const update = () => {
      if (manualThemeRef.current !== null) return;
      const dark = mq.matches;
      isDarkRef.current = dark;
      setIsDark(dark);
    };
    update();
    mq.addEventListener("change", update);
    return () => mq.removeEventListener("change", update);
  }, []);

  const toggleTheme = () => {
    const next = !isDarkRef.current;
    manualThemeRef.current = next;
    isDarkRef.current = next;
    setIsDark(next);
  };

  useEffect(() => {
    speedRef.current = 0.1 + (speed / 100) * 4.9;
  }, [speed]);

  // ── Canvas animation ─────────────────────────────────────────────────────
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    let animId: number;

    const resize = () => {
      const dpr = window.devicePixelRatio || 1;
      const w = window.innerWidth;
      const h = window.innerHeight;
      dimsRef.current = { w, h };
      configRef.current = getResponsiveConfig(w);
      canvas.width = w * dpr;
      canvas.height = h * dpr;
      canvas.style.width = `${w}px`;
      canvas.style.height = `${h}px`;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

      const cfg = configRef.current;
      bgStarsRef.current = Array.from({ length: cfg.starCount }, () => createBackgroundStar(w, h));
      cloudsRef.current = Array.from({ length: cfg.cloudCount }, () => createCloud(w, h));
    };

    resize();

    const { w, h } = dimsRef.current;
    const cfg = configRef.current;
    for (let i = 0; i < Math.min(4, cfg.maxPlanes); i++) {
      planesRef.current.push(spawnPlane(w, h, cfg.planeSizeMin, cfg.planeSizeRange));
    }

    // ── Render loop ──────────────────────────────────────────────────────────

    const frame = (time: number) => {
      const { w, h } = dimsRef.current;
      const spd = speedRef.current;
      const mouse = mouseRef.current;
      const cfg = configRef.current;
      const formation = formationRef.current;
      const dark = isDarkRef.current;
      const theme = dark ? DARK : LIGHT;

      const logoCx = w / 2;
      const logoCy = h / 2 - h * 0.08;

      if (formation.active && time - formation.startTime > formation.duration) {
        formation.active = false;
      }

      // ── Background ─────────────────────────────────────────────────────────
      if (dark) {
        ctx.fillStyle = BG_COLORS_DARK;
        ctx.fillRect(0, 0, w, h);

        // Ambient orbs
        const drawOrb = (cx: number, cy: number, r: number, color: string) => {
          const grad = ctx.createRadialGradient(cx, cy, 0, cx, cy, r);
          grad.addColorStop(0, color);
          grad.addColorStop(1, "transparent");
          ctx.fillStyle = grad;
          ctx.fillRect(cx - r, cy - r, r * 2, r * 2);
        };
        const t = time * 0.0001;
        drawOrb(w * 0.3 + Math.sin(t * 0.7) * w * 0.05, h * 0.4 + Math.cos(t * 0.5) * h * 0.05, w * 0.35, theme.orbs[0].color);
        drawOrb(w * 0.7 + Math.cos(t * 0.6) * w * 0.04, h * 0.6 + Math.sin(t * 0.8) * h * 0.04, w * 0.3, theme.orbs[1].color);
      } else {
        drawSkyGradient(ctx, w, h);

        // Subtle sun glow top-right
        const grad = ctx.createRadialGradient(w * 0.8, h * 0.05, 0, w * 0.8, h * 0.05, w * 0.3);
        grad.addColorStop(0, "rgba(255, 248, 220, 0.4)");
        grad.addColorStop(0.5, "rgba(255, 248, 220, 0.1)");
        grad.addColorStop(1, "transparent");
        ctx.fillStyle = grad;
        ctx.fillRect(0, 0, w, h);
      }

      // ── Stars (dark mode) or Clouds (light mode) ──────────────────────────
      if (dark) {
        for (const s of bgStarsRef.current) {
          const twinkle = Math.sin(time * s.twinkleSpeed * 0.001 + s.twinklePhase);
          const opacity = s.baseOpacity * (0.3 + 0.7 * twinkle);
          ctx.beginPath();
          ctx.arc(s.x, s.y, s.size, 0, Math.PI * 2);
          ctx.fillStyle = `rgba(255, 255, 255, ${Math.max(0, opacity)})`;
          ctx.fill();
        }
      } else {
        for (const c of cloudsRef.current) {
          c.x += c.vx * spd;
          if (c.x - 150 > w) {
            c.x = -200;
            c.y = Math.random() * h * 0.85 + h * 0.05;
          }
          drawCloud(ctx, c, 0, 0);
        }
      }

      // ── Spawn new planes ───────────────────────────────────────────────────
      if (planesRef.current.length < cfg.maxPlanes && Math.random() < cfg.spawnRate * spd) {
        planesRef.current.push(spawnPlane(w, h, cfg.planeSizeMin, cfg.planeSizeRange));
      }

      // ── Update and draw planes ─────────────────────────────────────────────
      const alive: Plane[] = [];

      for (let pi = 0; pi < planesRef.current.length; pi++) {
        const p = planesRef.current[pi];

        const slot = formation.active ? formation.slotMap.get(p.id) : undefined;
        if (slot !== undefined) {
          const elapsed = (time - formation.startTime) / 1000;
          const leaderX = formation.originX + Math.cos(formation.heading) * formation.speed * elapsed * 60;
          const leaderY = formation.originY + Math.sin(formation.heading) * formation.speed * elapsed * 60;
          const offset = formation.offsets[slot];
          const targetX = leaderX + offset.dx;
          const targetY = leaderY + offset.dy;
          const cruiseVx = Math.cos(formation.heading) * formation.speed;
          const cruiseVy = Math.sin(formation.heading) * formation.speed;

          const blend = 0.025;
          p.x += (targetX - p.x) * blend;
          p.y += (targetY - p.y) * blend;
          p.vx += (cruiseVx - p.vx) * blend;
          p.vy += (cruiseVy - p.vy) * blend;
        } else {
          if (mouse.active) {
            const dx = mouse.x - p.x;
            const dy = mouse.y - p.y;
            const distSq = dx * dx + dy * dy;
            const dist = Math.sqrt(distSq);
            if (dist > 40 && dist < 450) {
              const force = MOUSE_GRAVITY_STRENGTH / distSq;
              p.vx += (dx / dist) * force;
              p.vy += (dy / dist) * force;
            }
          }
        }

        // Logo repulsion
        const ldx = p.x - logoCx;
        const ldy = p.y - logoCy;
        const logoDist = Math.sqrt(ldx * ldx + ldy * ldy);
        if (logoDist < LOGO_REPEL_RADIUS && logoDist > 5) {
          const repelForce = ((LOGO_REPEL_RADIUS - logoDist) / LOGO_REPEL_RADIUS) * LOGO_REPEL_STRENGTH;
          p.vx += (ldx / logoDist) * repelForce;
          p.vy += (ldy / logoDist) * repelForce;
        }

        p.vx *= 0.9985;
        p.vy *= 0.9985;

        const inFormation = slot !== undefined;
        p.x += p.vx * spd;
        p.y += p.vy * spd;
        if (!inFormation) p.life += spd;

        const fadeIn = Math.min(p.life / 30, 1);
        const fadeOut = inFormation ? 1 : Math.max(0, 1 - p.life / p.maxLife);
        p.opacity = fadeIn * fadeOut;

        p.trail.push({ x: p.x, y: p.y });
        while (p.trail.length > p.maxTrailLength) p.trail.shift();

        const pad = 200;
        if (inFormation || (p.life < p.maxLife && p.x > -pad && p.x < w + pad && p.y > -pad && p.y < h + pad)) {
          alive.push(p);
        }

        // Contrail
        if (p.trail.length > 2) {
          for (let i = 2; i < p.trail.length; i++) {
            const prev = p.trail[i - 1];
            const cur = p.trail[i];
            const progress = i / p.trail.length;
            const trailAlpha = progress * p.opacity * 0.25;
            ctx.beginPath();
            ctx.moveTo(prev.x, prev.y);
            ctx.lineTo(cur.x, cur.y);
            ctx.strokeStyle = theme.trail(trailAlpha);
            ctx.lineWidth = 1 + progress * 1.5;
            ctx.lineCap = "round";
            ctx.stroke();
          }
        }

        const heading = Math.atan2(p.vy, p.vx);
        drawExhaustShimmer(ctx, p.x, p.y, heading, p.size, p.opacity, time, theme.exhaust);
        drawP80(ctx, p.x, p.y, heading, p.size, p.opacity, theme.planeFill, theme.planeStroke);
      }

      planesRef.current = alive;

      // Connection lines
      for (let i = 0; i < alive.length; i++) {
        for (let j = i + 1; j < alive.length; j++) {
          const a = alive[i];
          const b = alive[j];
          const dx = a.x - b.x;
          const dy = a.y - b.y;
          const dist = Math.sqrt(dx * dx + dy * dy);
          if (dist < 200) {
            const opacity = (1 - dist / 200) * 0.06 * Math.min(a.opacity, b.opacity);
            ctx.beginPath();
            ctx.moveTo(a.x, a.y);
            ctx.lineTo(b.x, b.y);
            ctx.strokeStyle = theme.connection(opacity);
            ctx.lineWidth = 0.5;
            ctx.stroke();
          }
        }
      }

      animId = requestAnimationFrame(frame);
    };

    animId = requestAnimationFrame(frame);

    // ── Events ───────────────────────────────────────────────────────────────

    const onResize = () => resize();
    const onMouseMove = (e: MouseEvent) => { mouseRef.current = { x: e.clientX, y: e.clientY, active: true }; };
    const onMouseLeave = () => { mouseRef.current.active = false; };

    const triggerFormation = (cx: number, cy: number) => {
      const planes = planesRef.current;
      if (planes.length === 0) return;
      const { w, h } = dimsRef.current;
      const heading = Math.atan2(cy - h / 2, cx - w / 2);
      const offsets = getFormationOffsets(heading, planes.length);
      const slotMap = new Map<number, number>();
      planes.forEach((p, i) => { slotMap.set(p.id, i); });
      formationRef.current = { active: true, startTime: performance.now(), duration: FORMATION_DURATION, originX: cx, originY: cy, heading, speed: 1.5, offsets, slotMap };
    };

    const onClick = (e: MouseEvent) => { triggerFormation(e.clientX, e.clientY); };
    const onTouchStart = (e: TouchEvent) => { if (e.touches.length > 0) { const t = e.touches[0]; mouseRef.current = { x: t.clientX, y: t.clientY, active: true }; triggerFormation(t.clientX, t.clientY); } };
    const onTouchMove = (e: TouchEvent) => { if (e.touches.length > 0) { const t = e.touches[0]; mouseRef.current = { x: t.clientX, y: t.clientY, active: true }; } };
    const onTouchEnd = () => { mouseRef.current.active = false; };

    window.addEventListener("resize", onResize);
    canvas.addEventListener("mousemove", onMouseMove);
    canvas.addEventListener("mouseleave", onMouseLeave);
    canvas.addEventListener("click", onClick);
    canvas.addEventListener("touchstart", onTouchStart, { passive: true });
    canvas.addEventListener("touchmove", onTouchMove, { passive: true });
    canvas.addEventListener("touchend", onTouchEnd);

    return () => {
      cancelAnimationFrame(animId);
      window.removeEventListener("resize", onResize);
      canvas.removeEventListener("mousemove", onMouseMove);
      canvas.removeEventListener("mouseleave", onMouseLeave);
      canvas.removeEventListener("click", onClick);
      canvas.removeEventListener("touchstart", onTouchStart);
      canvas.removeEventListener("touchmove", onTouchMove);
      canvas.removeEventListener("touchend", onTouchEnd);
    };
  }, []);

  return (
    <div className="relative min-h-screen overflow-hidden" style={{ background: isDark ? BG_COLORS_DARK : "#87BBDF" }}>
      <canvas ref={canvasRef} className="fixed inset-0 z-0" style={{ cursor: "crosshair" }} />

      {/* ── Hero ──────────────────────────────────────────────────────────── */}
      <div className="relative z-10 flex min-h-screen flex-col items-center justify-center px-6 text-center select-none pointer-events-none">
        <div className="max-w-2xl space-y-8">
          <h1 className="text-[5rem] sm:text-[7rem] md:text-[9rem] font-extrabold leading-none tracking-tighter">
            <span
              className="bg-clip-text text-transparent"
              style={{
                backgroundImage: isDark
                  ? "linear-gradient(135deg, #ffffff 0%, #dde0e8 40%, #a8b0c0 100%)"
                  : "linear-gradient(135deg, #0f1f2e 0%, #1e3345 40%, #2d4a60 100%)",
              }}
            >
              143
            </span>
            <span className={isDark ? "text-white/70" : "text-slate-800/70"}>.dev</span>
          </h1>

          <p className={`text-base sm:text-lg md:text-xl font-light leading-relaxed ${isDark ? "text-white/70" : "text-slate-800"}`}>
            Open source bug fixing for production systems.
          </p>

          <p className={`mx-auto max-w-lg text-xs sm:text-sm leading-relaxed ${isDark ? "text-white/40" : "text-slate-700"}`}>
            The first US jet fighter, the P-80 Shooting Star, was built in just 143&nbsp;days.
            Connect GitHub, Sentry, or Linear and ship fixes while you sleep.
          </p>

          <div className="flex items-center justify-center gap-3 sm:gap-4 pt-2 pointer-events-auto">
            <Button
              asChild
              className={`rounded-lg px-5 sm:px-7 py-2.5 text-sm font-semibold shadow-lg transition-all ${
                isDark
                  ? "bg-white text-[#08080f] shadow-white/5 hover:bg-white/90 hover:shadow-white/15"
                  : "bg-slate-900 text-white shadow-slate-900/10 hover:bg-slate-800"
              }`}
            >
              <Link href="/login?tab=signup">Get Started</Link>
            </Button>
            <Button
              asChild
              variant="outline"
              className={`rounded-lg bg-transparent px-5 sm:px-7 py-2.5 text-sm font-medium shadow-none transition-all ${
                isDark
                  ? "border-white/25 text-white/70 hover:border-white/40 hover:bg-transparent hover:text-white/90"
                  : "border-slate-500 text-slate-700 hover:border-slate-600 hover:bg-transparent hover:text-slate-900"
              }`}
            >
              <Link href="/login">Sign In</Link>
            </Button>
          </div>
        </div>

        <p className={`absolute bottom-20 text-[11px] animate-pulse hidden sm:block ${isDark ? "text-white/20" : "text-slate-600/50"}`}>
          move your mouse to attract planes &middot; click to call formation
        </p>
      </div>

      {/* ── Controls panel ────────────────────────────────────────────────── */}
      <div className={`fixed bottom-5 left-5 z-20 flex items-center gap-3 rounded-xl border px-4 py-3 backdrop-blur-md ${
        isDark
          ? "border-white/10 bg-white/[0.05]"
          : "border-slate-400/30 bg-white/50"
      }`}>
        <span className={`text-[11px] font-medium tracking-wide uppercase ${isDark ? "text-white/40" : "text-slate-600"}`}>
          Speed
        </span>
        <div className={`w-28 ${
          isDark
            ? "[&_[data-slot=slider-track]]:bg-white/15 [&_[data-slot=slider-range]]:bg-white/50 [&_[data-slot=slider-thumb]]:border-white/50 [&_[data-slot=slider-thumb]]:bg-white/90"
            : "[&_[data-slot=slider-track]]:bg-slate-300/50 [&_[data-slot=slider-range]]:bg-slate-500/60 [&_[data-slot=slider-thumb]]:border-slate-400 [&_[data-slot=slider-thumb]]:bg-white"
        }`}>
          <Slider value={[speed]} onValueChange={(v) => setSpeed(v[0])} min={0} max={100} step={1} />
        </div>
        <span className={`w-8 text-right font-mono text-[11px] ${isDark ? "text-white/40" : "text-slate-600"}`}>
          {(0.1 + (speed / 100) * 4.9).toFixed(1)}x
        </span>
        <div className={`ml-1 h-4 w-px ${isDark ? "bg-white/10" : "bg-slate-300/50"}`} />
        <button
          onClick={toggleTheme}
          className={`flex items-center justify-center rounded-md p-1 transition-colors ${
            isDark
              ? "text-white/40 hover:text-white/70"
              : "text-slate-600 hover:text-slate-800"
          }`}
          aria-label={isDark ? "Switch to light mode" : "Switch to dark mode"}
        >
          {isDark ? <Sun className="h-3.5 w-3.5" /> : <Moon className="h-3.5 w-3.5" />}
        </button>
      </div>
    </div>
  );
}
