"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { Slider } from "@/components/ui/slider";

// ── Types ──────────────────────────────────────────────────────────────────────

interface BackgroundStar {
  x: number;
  y: number;
  size: number;
  baseOpacity: number;
  twinkleSpeed: number;
  twinklePhase: number;
}

interface ShootingStar {
  x: number;
  y: number;
  vx: number;
  vy: number;
  size: number;
  trail: Array<{ x: number; y: number }>;
  maxTrailLength: number;
  life: number;
  maxLife: number;
  hue: number;
  isEmoji: boolean;
}

// ── Constants ──────────────────────────────────────────────────────────────────

const BG_STAR_COUNT = 200;
const MAX_SHOOTING_STARS = 18;
const BASE_SPAWN_RATE = 0.02;
const MOUSE_GRAVITY_STRENGTH = 600;
const BURST_COUNT = 8;
const BG_COLOR = "rgb(8, 8, 16)";

// ── Helpers ────────────────────────────────────────────────────────────────────

function createBackgroundStar(w: number, h: number): BackgroundStar {
  return {
    x: Math.random() * w,
    y: Math.random() * h,
    size: Math.random() * 1.8 + 0.3,
    baseOpacity: Math.random() * 0.6 + 0.3,
    twinkleSpeed: Math.random() * 0.03 + 0.005,
    twinklePhase: Math.random() * Math.PI * 2,
  };
}

function spawnShootingStar(
  w: number,
  h: number,
  fromClick = false,
  cx = 0,
  cy = 0,
): ShootingStar {
  let x: number, y: number, vx: number, vy: number;

  if (fromClick) {
    x = cx;
    y = cy;
    const angle = Math.random() * Math.PI * 2;
    const spd = Math.random() * 2.5 + 1;
    vx = Math.cos(angle) * spd;
    vy = Math.sin(angle) * spd;
  } else {
    const edge = Math.floor(Math.random() * 4);
    switch (edge) {
      case 0:
        x = Math.random() * w;
        y = -30;
        break;
      case 1:
        x = w + 30;
        y = Math.random() * h;
        break;
      case 2:
        x = Math.random() * w;
        y = h + 30;
        break;
      default:
        x = -30;
        y = Math.random() * h;
        break;
    }
    const targetX = w / 2 + (Math.random() - 0.5) * w * 0.8;
    const targetY = h / 2 + (Math.random() - 0.5) * h * 0.8;
    const angle = Math.atan2(targetY - y, targetX - x);
    const spd = Math.random() * 1.5 + 0.7;
    vx = Math.cos(angle) * spd;
    vy = Math.sin(angle) * spd;
  }

  return {
    x,
    y,
    vx,
    vy,
    size: Math.random() * 3 + 1.5,
    trail: [],
    maxTrailLength: Math.floor(Math.random() * 40 + 20),
    life: 0,
    maxLife: Math.random() * 500 + 300,
    hue: Math.random() < 0.15 ? Math.random() * 20 + 30 : Math.random() * 40 + 210,
    isEmoji: Math.random() < 0.06,
  };
}

// ── Component ──────────────────────────────────────────────────────────────────

export default function LandingPage() {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const bgStarsRef = useRef<BackgroundStar[]>([]);
  const starsRef = useRef<ShootingStar[]>([]);
  const mouseRef = useRef({ x: -9999, y: -9999, active: false });
  const speedRef = useRef(1);
  const dimsRef = useRef({ w: 0, h: 0 });
  const [speed, setSpeed] = useState(10);

  // Map slider 0-100 → multiplier 0.1x–5x
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
      canvas.width = w * dpr;
      canvas.height = h * dpr;
      canvas.style.width = `${w}px`;
      canvas.style.height = `${h}px`;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

      // Reinit background stars on resize
      bgStarsRef.current = Array.from({ length: BG_STAR_COUNT }, () =>
        createBackgroundStar(w, h),
      );

      // Full clear
      ctx.fillStyle = BG_COLOR;
      ctx.fillRect(0, 0, w, h);
    };

    resize();

    // Seed some initial shooting stars
    const { w, h } = dimsRef.current;
    for (let i = 0; i < 5; i++) {
      starsRef.current.push(spawnShootingStar(w, h));
    }

    // ── Render loop ──────────────────────────────────────────────────────────

    const frame = (time: number) => {
      const { w, h } = dimsRef.current;
      const spd = speedRef.current;
      const mouse = mouseRef.current;

      // Full clear each frame
      ctx.fillStyle = BG_COLOR;
      ctx.fillRect(0, 0, w, h);

      // ── Background stars ───────────────────────────────────────────────────
      for (const s of bgStarsRef.current) {
        const twinkle = Math.sin(time * s.twinkleSpeed * 0.001 + s.twinklePhase);
        const opacity = s.baseOpacity * (0.4 + 0.6 * twinkle);
        ctx.beginPath();
        ctx.arc(s.x, s.y, s.size, 0, Math.PI * 2);
        ctx.fillStyle = `rgba(255, 255, 255, ${Math.max(0, opacity)})`;
        ctx.fill();
      }

      // ── Spawn new shooting stars ───────────────────────────────────────────
      if (starsRef.current.length < MAX_SHOOTING_STARS && Math.random() < BASE_SPAWN_RATE * spd) {
        starsRef.current.push(spawnShootingStar(w, h));
      }

      // ── Update shooting stars ──────────────────────────────────────────────
      const alive: ShootingStar[] = [];

      for (const s of starsRef.current) {
        // Mouse gravity
        if (mouse.active) {
          const dx = mouse.x - s.x;
          const dy = mouse.y - s.y;
          const distSq = dx * dx + dy * dy;
          const dist = Math.sqrt(distSq);
          if (dist > 30 && dist < 500) {
            const force = MOUSE_GRAVITY_STRENGTH / distSq;
            s.vx += (dx / dist) * force;
            s.vy += (dy / dist) * force;
          }
        }

        // Apply a tiny drag to prevent infinite acceleration
        s.vx *= 0.999;
        s.vy *= 0.999;

        // Integrate
        s.x += s.vx * spd;
        s.y += s.vy * spd;
        s.life += spd;

        // Record trail
        s.trail.push({ x: s.x, y: s.y });
        while (s.trail.length > s.maxTrailLength) {
          s.trail.shift();
        }

        // Bounds + lifespan check
        const pad = 150;
        if (
          s.life < s.maxLife &&
          s.x > -pad &&
          s.x < w + pad &&
          s.y > -pad &&
          s.y < h + pad
        ) {
          alive.push(s);
        }

        // ── Draw trail ─────────────────────────────────────────────────────
        if (s.trail.length > 1) {
          const lifeFade = Math.max(0, 1 - s.life / s.maxLife);
          for (let i = 1; i < s.trail.length; i++) {
            const prev = s.trail[i - 1];
            const cur = s.trail[i];
            const progress = i / s.trail.length;

            // Subtle hue shift with velocity
            const vel = Math.sqrt(s.vx * s.vx + s.vy * s.vy);
            const hueShift = Math.min(vel * spd, 20);
            const hue = s.hue + hueShift;
            const sat = 8 + progress * 15;
            const light = 55 + progress * 35;
            const alpha = progress * lifeFade * 0.5;

            ctx.beginPath();
            ctx.moveTo(prev.x, prev.y);
            ctx.lineTo(cur.x, cur.y);
            ctx.strokeStyle = `hsla(${hue}, ${sat}%, ${light}%, ${alpha})`;
            ctx.lineWidth = s.size * progress;
            ctx.lineCap = "round";
            ctx.stroke();
          }
        }

        // ── Draw head ──────────────────────────────────────────────────────
        const headAlpha = Math.max(0, 1 - s.life / s.maxLife);

        if (s.isEmoji) {
          ctx.font = `${Math.round(s.size * 6)}px serif`;
          ctx.globalAlpha = headAlpha;
          ctx.fillText("\u{1F320}", s.x - s.size * 3, s.y + s.size * 2);
          ctx.globalAlpha = 1;
        } else {
          // Outer glow
          const grad = ctx.createRadialGradient(s.x, s.y, 0, s.x, s.y, s.size * 4);
          grad.addColorStop(0, `hsla(${s.hue}, 15%, 95%, ${headAlpha})`);
          grad.addColorStop(0.3, `hsla(${s.hue}, 10%, 80%, ${headAlpha * 0.35})`);
          grad.addColorStop(1, `hsla(${s.hue}, 8%, 60%, 0)`);
          ctx.beginPath();
          ctx.arc(s.x, s.y, s.size * 4, 0, Math.PI * 2);
          ctx.fillStyle = grad;
          ctx.fill();

          // Bright core
          ctx.beginPath();
          ctx.arc(s.x, s.y, s.size * 0.7, 0, Math.PI * 2);
          ctx.fillStyle = `rgba(255, 255, 255, ${headAlpha})`;
          ctx.fill();
        }
      }

      starsRef.current = alive;

      // ── Constellation lines ────────────────────────────────────────────────
      for (let i = 0; i < alive.length; i++) {
        for (let j = i + 1; j < alive.length; j++) {
          const a = alive[i];
          const b = alive[j];
          const dx = a.x - b.x;
          const dy = a.y - b.y;
          const dist = Math.sqrt(dx * dx + dy * dy);
          if (dist < 160) {
            const opacity = (1 - dist / 160) * 0.07;
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

    // Kick off
    ctx.fillStyle = BG_COLOR;
    ctx.fillRect(0, 0, w, h);
    animId = requestAnimationFrame(frame);

    // ── Event listeners ──────────────────────────────────────────────────────

    const onResize = () => resize();

    const onMouseMove = (e: MouseEvent) => {
      mouseRef.current = { x: e.clientX, y: e.clientY, active: true };
    };

    const onMouseLeave = () => {
      mouseRef.current.active = false;
    };

    const onClick = (e: MouseEvent) => {
      const { w, h } = dimsRef.current;
      for (let i = 0; i < BURST_COUNT; i++) {
        if (starsRef.current.length < MAX_SHOOTING_STARS + BURST_COUNT) {
          starsRef.current.push(spawnShootingStar(w, h, true, e.clientX, e.clientY));
        }
      }
    };

    window.addEventListener("resize", onResize);
    canvas.addEventListener("mousemove", onMouseMove);
    canvas.addEventListener("mouseleave", onMouseLeave);
    canvas.addEventListener("click", onClick);

    return () => {
      cancelAnimationFrame(animId);
      window.removeEventListener("resize", onResize);
      canvas.removeEventListener("mousemove", onMouseMove);
      canvas.removeEventListener("mouseleave", onMouseLeave);
      canvas.removeEventListener("click", onClick);
    };
  }, []);

  return (
    <div className="relative min-h-screen overflow-hidden" style={{ background: BG_COLOR }}>
      {/* Canvas layer */}
      <canvas
        ref={canvasRef}
        className="fixed inset-0 z-0"
        style={{ cursor: "crosshair" }}
      />

      {/* ── Hero content ──────────────────────────────────────────────────── */}
      <div className="relative z-10 flex min-h-screen flex-col items-center justify-center px-6 text-center select-none pointer-events-none">
        <div className="max-w-2xl space-y-8">
          {/* Logo */}
          <h1 className="text-[7rem] sm:text-[9rem] font-extrabold leading-none tracking-tighter">
            <span
              className="bg-clip-text text-transparent"
              style={{
                backgroundImage:
                  "linear-gradient(135deg, #e2e8f0 0%, #cbd5e1 35%, #94a3b8 70%, #64748b 100%)",
              }}
            >
              143
            </span>
            <span className="text-white/70">.dev</span>
          </h1>

          {/* Tagline */}
          <p className="text-lg sm:text-xl font-light text-white/55 leading-relaxed">
            AI agents that detect issues, generate fixes,
            <br className="hidden sm:block" />
            and open pull requests&nbsp;&mdash;&nbsp;while you sleep.
          </p>

          {/* Description */}
          <p className="mx-auto max-w-md text-sm leading-relaxed text-white/30">
            Connect GitHub, Sentry, or Linear. 143 monitors your repos for bugs,
            writes the code to fix them, and opens PRs for your review. Fully automated.
          </p>

          {/* CTAs */}
          <div className="flex items-center justify-center gap-4 pt-2 pointer-events-auto">
            <Link
              href="/login"
              className="rounded-lg bg-white px-7 py-2.5 text-sm font-semibold text-[rgb(3,3,20)] shadow-lg shadow-white/10 transition-all hover:bg-white/90 hover:shadow-white/25"
            >
              Get Started
            </Link>
            <Link
              href="/login"
              className="rounded-lg border border-white/20 px-7 py-2.5 text-sm font-medium text-white/60 transition-all hover:border-white/40 hover:text-white/90"
            >
              Sign In
            </Link>
          </div>
        </div>

        {/* Hint */}
        <p className="absolute bottom-20 text-[11px] text-white/15 animate-pulse">
          move your mouse to attract stars &middot; click anywhere to create bursts
        </p>
      </div>

      {/* ── Speed slider ──────────────────────────────────────────────────── */}
      <div className="fixed bottom-5 left-5 z-20 flex items-center gap-3 rounded-xl border border-white/10 bg-white/[0.04] px-4 py-3 backdrop-blur-md">
        <span className="text-[11px] font-medium tracking-wide text-white/35 uppercase">
          Speed
        </span>
        <div className="w-28 [&_[data-slot=slider-track]]:bg-white/15 [&_[data-slot=slider-range]]:bg-white/50 [&_[data-slot=slider-thumb]]:border-white/50 [&_[data-slot=slider-thumb]]:bg-white/90">
          <Slider
            value={[speed]}
            onValueChange={(v) => setSpeed(v[0])}
            min={0}
            max={100}
            step={1}
          />
        </div>
        <span className="w-8 text-right font-mono text-[11px] text-white/35">
          {(0.1 + (speed / 100) * 4.9).toFixed(1)}x
        </span>
      </div>
    </div>
  );
}
