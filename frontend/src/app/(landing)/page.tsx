"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
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
  layer: number; // 0 = far, 1 = mid, 2 = near
}

interface Formation {
  active: boolean;
  startTime: number;
  duration: number;
  originX: number;
  originY: number;
  heading: number;
  speed: number;
  offsets: Array<{ dx: number; dy: number }>; // relative to leader
  slotMap: Map<number, number>; // planeId → slot index (locked at formation start)
}

// ── Responsive helpers ─────────────────────────────────────────────────────────

function getResponsiveConfig(w: number) {
  if (w < 640) {
    return { maxPlanes: 5, starCount: 100, planeSizeMin: 5, planeSizeRange: 4, spawnRate: 0.008 };
  }
  if (w < 1024) {
    return { maxPlanes: 8, starCount: 160, planeSizeMin: 6, planeSizeRange: 5, spawnRate: 0.01 };
  }
  return { maxPlanes: 12, starCount: 250, planeSizeMin: 8, planeSizeRange: 6, spawnRate: 0.012 };
}

// ── Constants ──────────────────────────────────────────────────────────────────

const MOUSE_GRAVITY_STRENGTH = 500;
const LOGO_REPEL_RADIUS = 180;
const LOGO_REPEL_STRENGTH = 0.12;
const PARALLAX_STRENGTH = 60;
const FORMATION_DURATION = 5000; // ms
const FORMATION_STEER = 0.04;
const BG_COLOR = "#08080f";

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
  offsets.push({ dx: 0, dy: 0 }); // leader

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

  ctx.fillStyle = `rgba(210, 218, 230, ${alpha})`;
  ctx.fill();
  ctx.strokeStyle = `rgba(255, 255, 255, ${alpha * 0.3})`;
  ctx.lineWidth = 0.5;
  ctx.stroke();

  ctx.globalAlpha = 1;
  ctx.restore();
}

// ── Draw exhaust shimmer ───────────────────────────────────────────────────────

function drawExhaustShimmer(
  ctx: CanvasRenderingContext2D,
  x: number, y: number, heading: number, size: number, alpha: number, time: number,
) {
  const cosH = Math.cos(heading);
  const sinH = Math.sin(heading);
  // Tail position
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
      grad.addColorStop(0, `rgba(200, 215, 240, ${a})`);
      grad.addColorStop(1, `rgba(200, 215, 240, 0)`);
      ctx.beginPath();
      ctx.arc(sx, sy, r * 2, 0, Math.PI * 2);
      ctx.fillStyle = grad;
      ctx.fill();
    }
  }
}

// ── Component ──────────────────────────────────────────────────────────────────

export default function LandingPage() {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const bgStarsRef = useRef<BackgroundStar[]>([]);
  const planesRef = useRef<Plane[]>([]);
  const mouseRef = useRef({ x: -9999, y: -9999, active: false });
  const speedRef = useRef(1);
  const dimsRef = useRef({ w: 0, h: 0 });
  const configRef = useRef(getResponsiveConfig(typeof window !== "undefined" ? window.innerWidth : 1200));
  const formationRef = useRef<Formation>({ active: false, startTime: 0, duration: FORMATION_DURATION, originX: 0, originY: 0, heading: 0, speed: 1.5, offsets: [], slotMap: new Map() });
  const [speed, setSpeed] = useState(12);

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

      bgStarsRef.current = Array.from(
        { length: configRef.current.starCount },
        () => createBackgroundStar(w, h),
      );
    };

    resize();

    // Seed initial planes
    const { w, h } = dimsRef.current;
    const cfg = configRef.current;
    const seedCount = Math.min(4, cfg.maxPlanes);
    for (let i = 0; i < seedCount; i++) {
      planesRef.current.push(spawnPlane(w, h, cfg.planeSizeMin, cfg.planeSizeRange));
    }

    // ── Render loop ──────────────────────────────────────────────────────────

    const frame = (time: number) => {
      const { w, h } = dimsRef.current;
      const spd = speedRef.current;
      const mouse = mouseRef.current;
      const cfg = configRef.current;
      const formation = formationRef.current;

      // Logo center (slightly above visual center to match the h1 position)
      const logoCx = w / 2;
      const logoCy = h / 2 - h * 0.08;

      // Check formation expiry
      if (formation.active && time - formation.startTime > formation.duration) {
        formation.active = false;
      }

      // ── Background ─────────────────────────────────────────────────────────
      ctx.fillStyle = BG_COLOR;
      ctx.fillRect(0, 0, w, h);

      // Ambient gradient orbs
      const drawOrb = (cx: number, cy: number, r: number, color: string) => {
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
        "rgba(30, 40, 80, 0.15)",
      );
      drawOrb(
        w * 0.7 + Math.cos(t * 0.6) * w * 0.04,
        h * 0.6 + Math.sin(t * 0.8) * h * 0.04,
        w * 0.3,
        "rgba(50, 30, 70, 0.1)",
      );

      // ── Parallax background stars ──────────────────────────────────────────
      const parallaxMults = [0.0, 0.35, 1.0];
      const mx = mouse.active ? mouse.x : w / 2;
      const my = mouse.active ? mouse.y : h / 2;
      const pOffX = (mx - w / 2) / w;
      const pOffY = (my - h / 2) / h;

      for (const s of bgStarsRef.current) {
        const twinkle = Math.sin(time * s.twinkleSpeed * 0.001 + s.twinklePhase);
        const opacity = s.baseOpacity * (0.3 + 0.7 * twinkle);
        const pm = parallaxMults[s.layer];
        const drawX = s.x - pOffX * PARALLAX_STRENGTH * pm;
        const drawY = s.y - pOffY * PARALLAX_STRENGTH * pm;
        ctx.beginPath();
        ctx.arc(drawX, drawY, s.size, 0, Math.PI * 2);
        ctx.fillStyle = `rgba(255, 255, 255, ${Math.max(0, opacity)})`;
        ctx.fill();
      }

      // ── Spawn new planes ───────────────────────────────────────────────────
      if (planesRef.current.length < cfg.maxPlanes && Math.random() < cfg.spawnRate * spd) {
        planesRef.current.push(spawnPlane(w, h, cfg.planeSizeMin, cfg.planeSizeRange));
      }

      // ── Update and draw planes ─────────────────────────────────────────────
      const alive: Plane[] = [];

      for (let pi = 0; pi < planesRef.current.length; pi++) {
        const p = planesRef.current[pi];

        // Formation: smoothly lerp position + velocity toward slot (no oscillation)
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

          // Direct interpolation toward target: can never overshoot
          const blend = 0.025;
          p.x += (targetX - p.x) * blend;
          p.y += (targetY - p.y) * blend;
          p.vx += (cruiseVx - p.vx) * blend;
          p.vy += (cruiseVy - p.vy) * blend;
        } else {
          // Mouse gravity (only when not in formation)
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

        // Magnetic logo repulsion
        const ldx = p.x - logoCx;
        const ldy = p.y - logoCy;
        const logoDist = Math.sqrt(ldx * ldx + ldy * ldy);
        if (logoDist < LOGO_REPEL_RADIUS && logoDist > 5) {
          const repelForce = ((LOGO_REPEL_RADIUS - logoDist) / LOGO_REPEL_RADIUS) * LOGO_REPEL_STRENGTH;
          p.vx += (ldx / logoDist) * repelForce;
          p.vy += (ldy / logoDist) * repelForce;
        }

        // Drag
        p.vx *= 0.9985;
        p.vy *= 0.9985;

        // Integrate
        const inFormation = slot !== undefined;
        p.x += p.vx * spd;
        p.y += p.vy * spd;
        if (!inFormation) {
          p.life += spd;
        }

        // Fade in/out (planes in formation stay fully visible)
        const fadeIn = Math.min(p.life / 30, 1);
        const fadeOut = inFormation ? 1 : Math.max(0, 1 - p.life / p.maxLife);
        p.opacity = fadeIn * fadeOut;

        // Record trail
        p.trail.push({ x: p.x, y: p.y });
        while (p.trail.length > p.maxTrailLength) {
          p.trail.shift();
        }

        // Bounds + lifespan (planes in formation are never removed)
        const pad = 200;
        if (
          inFormation ||
          (p.life < p.maxLife &&
           p.x > -pad && p.x < w + pad &&
           p.y > -pad && p.y < h + pad)
        ) {
          alive.push(p);
        }

        // ── Draw contrail ────────────────────────────────────────────────────
        if (p.trail.length > 2) {
          for (let i = 2; i < p.trail.length; i++) {
            const prev = p.trail[i - 1];
            const cur = p.trail[i];
            const progress = i / p.trail.length;
            const trailAlpha = progress * p.opacity * 0.25;

            ctx.beginPath();
            ctx.moveTo(prev.x, prev.y);
            ctx.lineTo(cur.x, cur.y);
            ctx.strokeStyle = `rgba(180, 190, 210, ${trailAlpha})`;
            ctx.lineWidth = 1 + progress * 1.5;
            ctx.lineCap = "round";
            ctx.stroke();
          }
        }

        // ── Draw exhaust shimmer ─────────────────────────────────────────────
        const heading = Math.atan2(p.vy, p.vx);
        drawExhaustShimmer(ctx, p.x, p.y, heading, p.size, p.opacity, time);

        // ── Draw plane ───────────────────────────────────────────────────────
        drawP80(ctx, p.x, p.y, heading, p.size, p.opacity);
      }

      planesRef.current = alive;

      // ── Connection lines ───────────────────────────────────────────────────
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
            ctx.strokeStyle = `rgba(200, 200, 220, ${opacity})`;
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

    const onMouseMove = (e: MouseEvent) => {
      mouseRef.current = { x: e.clientX, y: e.clientY, active: true };
    };
    const onMouseLeave = () => { mouseRef.current.active = false; };

    const triggerFormation = (cx: number, cy: number) => {
      const planes = planesRef.current;
      if (planes.length === 0) return;

      const { w, h } = dimsRef.current;
      const heading = Math.atan2(cy - h / 2, cx - w / 2);
      const offsets = getFormationOffsets(heading, planes.length);

      // Lock each plane to a slot by its ID so slots never shuffle
      const slotMap = new Map<number, number>();
      planes.forEach((p, i) => { slotMap.set(p.id, i); });

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

    // Touch support for mobile
    const onTouchStart = (e: TouchEvent) => {
      if (e.touches.length > 0) {
        const touch = e.touches[0];
        mouseRef.current = { x: touch.clientX, y: touch.clientY, active: true };
        triggerFormation(touch.clientX, touch.clientY);
      }
    };
    const onTouchMove = (e: TouchEvent) => {
      if (e.touches.length > 0) {
        const touch = e.touches[0];
        mouseRef.current = { x: touch.clientX, y: touch.clientY, active: true };
      }
    };
    const onTouchEnd = () => {
      mouseRef.current.active = false;
    };

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
    <div className="relative min-h-screen overflow-hidden" style={{ background: BG_COLOR }}>
      <canvas
        ref={canvasRef}
        className="fixed inset-0 z-0"
        style={{ cursor: "crosshair" }}
      />

      {/* ── Hero ──────────────────────────────────────────────────────────── */}
      <div className="relative z-10 flex min-h-screen flex-col items-center justify-center px-6 text-center select-none pointer-events-none">
        <div className="max-w-2xl space-y-8">
          <h1 className="text-[5rem] sm:text-[7rem] md:text-[9rem] font-extrabold leading-none tracking-tighter">
            <span
              className="bg-clip-text text-transparent"
              style={{
                backgroundImage:
                  "linear-gradient(135deg, #f0f0f5 0%, #c8ccd8 40%, #8891a5 100%)",
              }}
            >
              143
            </span>
            <span className="text-white/50">.dev</span>
          </h1>

          <p className="text-base sm:text-lg md:text-xl font-light text-white/50 leading-relaxed">
            Detect issues, generate fixes,
            <br className="hidden sm:block" />
            and open pull requests while you sleep.
          </p>

          <p className="mx-auto max-w-lg text-xs sm:text-sm leading-relaxed text-white/25">
            The first US jet fighter, the P-80 Shooting Star, was built in
            just 143&nbsp;days. We bring that same speed to fixing your code.
            Connect GitHub, Sentry, or Linear and let 143 handle the rest.
          </p>

          <div className="flex items-center justify-center gap-3 sm:gap-4 pt-2 pointer-events-auto">
            <Button asChild className="rounded-lg bg-white px-5 sm:px-7 py-2.5 text-sm font-semibold text-[#08080f] shadow-lg shadow-white/5 transition-all hover:bg-white/90 hover:shadow-white/15">
              <Link href="/login?tab=signup">Get Started</Link>
            </Button>
            <Button asChild variant="outline" className="rounded-lg border border-white/15 bg-transparent px-5 sm:px-7 py-2.5 text-sm font-medium text-white/50 shadow-none transition-all hover:border-white/30 hover:bg-transparent hover:text-white/80">
              <Link href="/login">Sign In</Link>
            </Button>
          </div>
        </div>

        <p className="absolute bottom-20 text-[11px] text-white/10 animate-pulse hidden sm:block">
          move your mouse to attract planes &middot; click to call formation
        </p>
      </div>

      {/* ── Speed slider ──────────────────────────────────────────────────── */}
      <div className="fixed bottom-5 left-5 z-20 flex items-center gap-3 rounded-xl border border-white/[0.06] bg-white/[0.03] px-4 py-3 backdrop-blur-md">
        <span className="text-[11px] font-medium tracking-wide text-white/25 uppercase">
          Speed
        </span>
        <div className="w-28 [&_[data-slot=slider-track]]:bg-white/10 [&_[data-slot=slider-range]]:bg-white/40 [&_[data-slot=slider-thumb]]:border-white/40 [&_[data-slot=slider-thumb]]:bg-white/80">
          <Slider
            value={[speed]}
            onValueChange={(v) => setSpeed(v[0])}
            min={0}
            max={100}
            step={1}
          />
        </div>
        <span className="w-8 text-right font-mono text-[11px] text-white/25">
          {(0.1 + (speed / 100) * 4.9).toFixed(1)}x
        </span>
      </div>
    </div>
  );
}
