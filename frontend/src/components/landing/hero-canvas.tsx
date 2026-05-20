"use client";

import { useEffect, useRef } from "react";
import { drawP80, DARK, LIGHT, type PlaneTheme } from "./draw-p80";

export { DARK, LIGHT, type PlaneTheme };

// ── Types ──────────────────────────────────────────────────────────────────────

interface Plane {
  id: number;
  x: number;
  y: number;
  size: number;
  speed: number;
  baseHeading: number;
  amplitude: number;
  frequency: number;
  phase: number;
  baseY: number;
  opacity: number;
  trail: Array<{ x: number; y: number }>;
  maxTrailLength: number;
  layer: number;
  life: number;
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


// ── Responsive config ──────────────────────────────────────────────────────────

function getResponsiveConfig(w: number) {
  if (w < 640)
    return {
      maxPlanes: 3,
      starCount: 80,
      planeSizeMin: 14,
      planeSizeRange: 6,
    };
  if (w < 1024)
    return {
      maxPlanes: 4,
      starCount: 130,
      planeSizeMin: 16,
      planeSizeRange: 8,
    };
  return {
    maxPlanes: 6,
    starCount: 200,
    planeSizeMin: 18,
    planeSizeRange: 10,
  };
}

// ── Constants ──────────────────────────────────────────────────────────────────

const MOUSE_GRAVITY_STRENGTH = 200;
const FORMATION_DURATION = 12000;
const PARALLAX_STRENGTH = 40;

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

function spawnPlane(
  w: number,
  h: number,
  sizeMin: number,
  sizeRange: number,
): Plane {
  const goRight = Math.random() > 0.2;
  const x = goRight ? -80 : w + 80;
  const y = Math.random() * h * 0.7 + h * 0.1;
  const heading = goRight
    ? (Math.random() - 0.5) * 0.3
    : Math.PI + (Math.random() - 0.5) * 0.3;

  const layer = Math.random() < 0.3 ? 0 : Math.random() < 0.5 ? 1 : 2;
  const sizeMultiplier = [0.7, 1.0, 1.3][layer];
  const speedMultiplier = [0.4, 0.7, 1.1][layer];
  const size = (Math.random() * sizeRange + sizeMin) * sizeMultiplier;

  return {
    id: nextPlaneId++,
    x,
    y,
    size,
    speed: (Math.random() * 0.3 + 0.5) * speedMultiplier,
    baseHeading: heading,
    amplitude: Math.random() * 25 + 8,
    frequency: Math.random() * 0.0006 + 0.0002,
    phase: Math.random() * Math.PI * 2,
    baseY: y,
    opacity: 0,
    trail: [],
    maxTrailLength: Math.floor(Math.random() * 80 + 70),
    layer,
    life: 0,
  };
}

function getFormationOffsets(
  heading: number,
  count: number,
): Array<{ dx: number; dy: number }> {
  const spacing = 55;
  const offsets: Array<{ dx: number; dy: number }> = [{ dx: 0, dy: 0 }];
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

// ── Draw sky gradient ──────────────────────────────────────────────────────────

function drawSkyGradient(
  ctx: CanvasRenderingContext2D,
  w: number,
  h: number,
) {
  // Monochrome off-white wash with the faintest hint of brand-purple at the
  // top — color is carried by the contrails and the brand glow, not the sky.
  const grad = ctx.createLinearGradient(0, 0, 0, h);
  grad.addColorStop(0, "#F4F2F8");
  grad.addColorStop(0.4, "#F8F6FB");
  grad.addColorStop(0.8, "#FAFAFB");
  grad.addColorStop(1, "#FBFBFC");
  ctx.fillStyle = grad;
  ctx.fillRect(0, 0, w, h);
}

// ── Component ──────────────────────────────────────────────────────────────────

interface HeroCanvasProps {
  isDark: boolean;
}

export default function HeroCanvas({ isDark }: HeroCanvasProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const bgStarsRef = useRef<BackgroundStar[]>([]);
  const planesRef = useRef<Plane[]>([]);
  const mouseRef = useRef({ x: -9999, y: -9999, active: false });
  const dimsRef = useRef({ w: 0, h: 0 });
  const configRef = useRef(
    getResponsiveConfig(
      typeof window !== "undefined" ? window.innerWidth : 1200,
    ),
  );
  const formationRef = useRef<Formation>({
    active: false,
    startTime: 0,
    duration: FORMATION_DURATION,
    originX: 0,
    originY: 0,
    heading: 0,
    speed: 1.5,
    offsets: [],
    slotMap: new Map(),
  });
  const isDarkRef = useRef(isDark);

  useEffect(() => {
    isDarkRef.current = isDark;
  }, [isDark]);

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
      bgStarsRef.current = Array.from({ length: cfg.starCount }, () =>
        createBackgroundStar(w, h),
      );
    };

    resize();

    const { w, h } = dimsRef.current;
    const cfg = configRef.current;
    for (let i = 0; i < Math.min(3, cfg.maxPlanes); i++) {
      const p = spawnPlane(w, h, cfg.planeSizeMin, cfg.planeSizeRange);
      const side = Math.random() > 0.5;
      p.x = side ? w * 0.05 + Math.random() * w * 0.2 : w * 0.75 + Math.random() * w * 0.2;
      p.y = Math.random() * h * 0.3 + (Math.random() > 0.5 ? 0 : h * 0.65);
      p.baseY = p.y;
      p.opacity = 1;
      p.life = 120;
      planesRef.current.push(p);
    }

    const frame = (time: number) => {
      const { w, h } = dimsRef.current;
      const mouse = mouseRef.current;
      const cfg = configRef.current;
      const formation = formationRef.current;
      const dark = isDarkRef.current;
      const theme = dark ? DARK : LIGHT;

      if (
        formation.active &&
        time - formation.startTime > formation.duration
      ) {
        for (const p of planesRef.current) {
          if (formation.slotMap.has(p.id)) {
            p.baseHeading = formation.heading;
            p.baseY = p.y;
            p.speed = formation.speed;
          }
        }
        formation.active = false;
      }

      // Background
      if (dark) {
        ctx.fillStyle = DARK.bg;
        ctx.fillRect(0, 0, w, h);

        const drawOrb = (
          cx: number,
          cy: number,
          r: number,
          color: string,
        ) => {
          const grad = ctx.createRadialGradient(cx, cy, 0, cx, cy, r);
          grad.addColorStop(0, color);
          grad.addColorStop(1, "transparent");
          ctx.fillStyle = grad;
          ctx.fillRect(cx - r, cy - r, r * 2, r * 2);
        };
        const t = time * 0.0001;
        drawOrb(
          w * 0.3 + Math.sin(t * 0.7) * w * 0.05,
          h * 0.4 + Math.cos(t * 0.5) * h * 0.05,
          w * 0.35,
          theme.orbs[0].color,
        );
        drawOrb(
          w * 0.7 + Math.cos(t * 0.6) * w * 0.04,
          h * 0.6 + Math.sin(t * 0.8) * h * 0.04,
          w * 0.3,
          theme.orbs[1].color,
        );
      } else {
        drawSkyGradient(ctx, w, h);

        // Soft brand-purple glow at top-right ties the hero to the in-app
        // --gradient-primary; replaces the previous warm-yellow "sun" wash.
        const grad = ctx.createRadialGradient(
          w * 0.8,
          h * 0.05,
          0,
          w * 0.8,
          h * 0.05,
          w * 0.45,
        );
        grad.addColorStop(0, "rgba(125, 95, 220, 0.18)");
        grad.addColorStop(0.5, "rgba(125, 95, 220, 0.06)");
        grad.addColorStop(1, "transparent");
        ctx.fillStyle = grad;
        ctx.fillRect(0, 0, w, h);

        // Secondary glow at bottom-left for asymmetric depth.
        const grad2 = ctx.createRadialGradient(
          w * 0.15,
          h * 0.95,
          0,
          w * 0.15,
          h * 0.95,
          w * 0.35,
        );
        grad2.addColorStop(0, "rgba(95, 75, 200, 0.10)");
        grad2.addColorStop(0.6, "rgba(95, 75, 200, 0.03)");
        grad2.addColorStop(1, "transparent");
        ctx.fillStyle = grad2;
        ctx.fillRect(0, 0, w, h);
      }

      // Stars (dark only) — light mode is monochrome with brand glow.
      if (dark) {
        const mx = mouse.active ? mouse.x : w / 2;
        const my = mouse.active ? mouse.y : h / 2;
        const pOffX = (mx - w / 2) / w;
        const pOffY = (my - h / 2) / h;
        const parallaxMults = [0.0, 0.35, 1.0];

        for (const s of bgStarsRef.current) {
          const twinkle = Math.sin(
            time * s.twinkleSpeed * 0.001 + s.twinklePhase,
          );
          const opacity = s.baseOpacity * (0.3 + 0.7 * twinkle);
          const pm = parallaxMults[s.layer];
          const drawX = s.x - pOffX * PARALLAX_STRENGTH * pm;
          const drawY = s.y - pOffY * PARALLAX_STRENGTH * pm;
          ctx.beginPath();
          ctx.arc(drawX, drawY, s.size, 0, Math.PI * 2);
          ctx.fillStyle = DARK.star(Math.max(0, opacity));
          ctx.fill();
        }
      }
      // Light mode renders no clouds: white blobs would vanish on the
      // near-white background. Brand glow + contrails carry the depth.

      // Spawn new planes
      if (
        planesRef.current.length < cfg.maxPlanes &&
        Math.random() < 0.003
      ) {
        planesRef.current.push(
          spawnPlane(w, h, cfg.planeSizeMin, cfg.planeSizeRange),
        );
      }

      // Update and draw planes
      const alive: Plane[] = [];

      for (const p of planesRef.current) {
        const slot = formation.active
          ? formation.slotMap.get(p.id)
          : undefined;
        const inFormation = slot !== undefined;

        if (inFormation) {
          const elapsed = (time - formation.startTime) / 1000;
          const leaderX =
            formation.originX +
            Math.cos(formation.heading) *
              formation.speed *
              elapsed *
              60;
          const leaderY =
            formation.originY +
            Math.sin(formation.heading) *
              formation.speed *
              elapsed *
              60;
          const offset = formation.offsets[slot];
          const targetX = leaderX + offset.dx;
          const targetY = leaderY + offset.dy;

          const dx = targetX - p.x;
          const dy = targetY - p.y;
          const dist = Math.sqrt(dx * dx + dy * dy);
          const ease = Math.min(0.04, 0.008 + dist * 0.00004);
          p.x += dx * ease;
          p.y += dy * ease;
        } else {
          p.x += Math.cos(p.baseHeading) * p.speed;

          const oscillation =
            Math.sin(time * p.frequency + p.phase) * p.amplitude;
          const targetY = p.baseY + oscillation;
          p.y += (targetY - p.y) * 0.015;

          if (mouse.active) {
            const dx = mouse.x - p.x;
            const dy = mouse.y - p.y;
            const distSq = dx * dx + dy * dy;
            const dist = Math.sqrt(distSq);
            if (dist > 80 && dist < 400) {
              const force = MOUSE_GRAVITY_STRENGTH / distSq;
              p.x += (dx / dist) * force * 0.2;
              p.y += (dy / dist) * force * 0.2;
            }
          }
        }

        p.life += 1;

        const fadeIn = Math.min(p.life / 80, 1);
        const edgePad = 120;
        const edgeFade = inFormation
          ? 1
          : Math.min(
              Math.min(
                (p.x + edgePad) / edgePad,
                (w + edgePad - p.x) / edgePad,
              ),
              1,
            );
        p.opacity = fadeIn * Math.max(0, Math.min(1, edgeFade));

        p.trail.push({ x: p.x, y: p.y });
        while (p.trail.length > p.maxTrailLength) p.trail.shift();

        const removePad = 160;
        if (
          !inFormation &&
          (p.x < -removePad ||
            p.x > w + removePad ||
            p.y < -removePad ||
            p.y > h + removePad)
        ) {
          continue;
        }
        alive.push(p);

        // Contrail
        if (p.trail.length > 2) {
          for (let i = 2; i < p.trail.length; i++) {
            const prev = p.trail[i - 1];
            const cur = p.trail[i];
            const progress = i / p.trail.length;
            const trailAlpha = progress * p.opacity * 0.75;
            const trailWidth = 0.8 + progress * 2.5;

            ctx.beginPath();
            ctx.moveTo(prev.x, prev.y);
            ctx.lineTo(cur.x, cur.y);
            ctx.strokeStyle = theme.trail(trailAlpha);
            ctx.lineWidth = trailWidth;
            ctx.lineCap = "round";
            ctx.stroke();
          }
        }

        // Draw plane
        const heading = p.baseHeading;
        drawP80(ctx, p.x, p.y, p.size, heading, 0.72, p.opacity, theme);
      }

      planesRef.current = alive;

      animId = requestAnimationFrame(frame);
    };

    animId = requestAnimationFrame(frame);

    // Events
    const onResize = () => resize();
    const onMouseMove = (e: MouseEvent) => {
      mouseRef.current = { x: e.clientX, y: e.clientY, active: true };
    };
    const onMouseLeave = () => {
      mouseRef.current.active = false;
    };

    const triggerFormation = (cx: number, cy: number) => {
      const planes = planesRef.current;
      if (planes.length === 0) return;
      const { w, h } = dimsRef.current;
      const heading = Math.atan2(cy - h / 2, cx - w / 2);
      const offsets = getFormationOffsets(heading, planes.length);
      const slotMap = new Map<number, number>();
      planes.forEach((p, i) => {
        slotMap.set(p.id, i);
        p.baseHeading = heading;
      });
      formationRef.current = {
        active: true,
        startTime: performance.now(),
        duration: FORMATION_DURATION,
        originX: cx,
        originY: cy,
        heading,
        speed: 1.5,
        offsets,
        slotMap,
      };
    };

    const onClick = (e: MouseEvent) => {
      triggerFormation(e.clientX, e.clientY);
    };
    const onTouchStart = (e: TouchEvent) => {
      if (e.touches.length > 0) {
        const t = e.touches[0];
        mouseRef.current = { x: t.clientX, y: t.clientY, active: true };
        triggerFormation(t.clientX, t.clientY);
      }
    };
    const onTouchMove = (e: TouchEvent) => {
      if (e.touches.length > 0) {
        const t = e.touches[0];
        mouseRef.current = { x: t.clientX, y: t.clientY, active: true };
      }
    };
    const onTouchEnd = () => {
      mouseRef.current.active = false;
    };

    window.addEventListener("resize", onResize);
    canvas.addEventListener("mousemove", onMouseMove);
    canvas.addEventListener("mouseleave", onMouseLeave);
    window.addEventListener("click", onClick);
    window.addEventListener("touchstart", onTouchStart, { passive: true });
    canvas.addEventListener("touchmove", onTouchMove, { passive: true });
    canvas.addEventListener("touchend", onTouchEnd);

    return () => {
      cancelAnimationFrame(animId);
      window.removeEventListener("resize", onResize);
      canvas.removeEventListener("mousemove", onMouseMove);
      canvas.removeEventListener("mouseleave", onMouseLeave);
      window.removeEventListener("click", onClick);
      window.removeEventListener("touchstart", onTouchStart);
      canvas.removeEventListener("touchmove", onTouchMove);
      canvas.removeEventListener("touchend", onTouchEnd);
    };
  }, []);

  return <canvas ref={canvasRef} className="absolute inset-0 z-0" />;
}
