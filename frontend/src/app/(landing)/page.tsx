"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { Button } from "@/components/ui/button";

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

interface CloudBlob {
  ox: number;
  oy: number;
  radius: number;
  opacity: number;
}

interface Cloud {
  x: number;
  y: number;
  vx: number;
  layer: number;
  blobs: CloudBlob[];
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
  planeHighlight: (a: number) => `rgba(255, 255, 255, ${a * 0.25})`,
  planeShadow: (a: number) => `rgba(0, 0, 0, ${a * 0.3})`,
  canopy: (a: number) => `rgba(140, 180, 255, ${a * 0.5})`,
  canopyEdge: (a: number) => `rgba(200, 220, 255, ${a * 0.3})`,
  panelLine: (a: number) => `rgba(255, 255, 255, ${a * 0.08})`,
  trail: (a: number) => `rgba(180, 195, 220, ${a})`,
  star: (a: number) => `rgba(255, 255, 255, ${a})`,
  orbs: [
    { color: "rgba(30, 40, 80, 0.15)" },
    { color: "rgba(50, 30, 70, 0.1)" },
  ],
};

const LIGHT = {
  bg: "#d4e6f5",
  planeFill: (a: number) => `rgba(45, 55, 70, ${a})`,
  planeStroke: (a: number) => `rgba(25, 30, 40, ${a * 0.25})`,
  planeHighlight: (a: number) => `rgba(255, 255, 255, ${a * 0.2})`,
  planeShadow: (a: number) => `rgba(15, 20, 35, ${a * 0.25})`,
  canopy: (a: number) => `rgba(120, 170, 230, ${a * 0.6})`,
  canopyEdge: (a: number) => `rgba(80, 130, 190, ${a * 0.35})`,
  panelLine: (a: number) => `rgba(0, 0, 0, ${a * 0.06})`,
  trail: (a: number) => `rgba(255, 255, 255, ${a})`,
  star: () => "transparent",
  orbs: [
    { color: "rgba(255, 255, 255, 0.3)" },
    { color: "rgba(200, 220, 255, 0.2)" },
  ],
};

// ── Responsive config ──────────────────────────────────────────────────────────

function getResponsiveConfig(w: number) {
  if (w < 640)
    return {
      maxPlanes: 3,
      starCount: 80,
      cloudCount: 4,
      planeSizeMin: 14,
      planeSizeRange: 6,
    };
  if (w < 1024)
    return {
      maxPlanes: 4,
      starCount: 130,
      cloudCount: 5,
      planeSizeMin: 16,
      planeSizeRange: 8,
    };
  return {
    maxPlanes: 6,
    starCount: 200,
    cloudCount: 6,
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

function createCloud(w: number, h: number): Cloud {
  const layer = Math.random() < 0.3 ? 0 : Math.random() < 0.5 ? 1 : 2;

  // Dramatic layer differentiation
  const baseRadius = [120, 70, 40][layer];
  const blobCount = [6, 5, 4][layer];
  const baseOpacity = [0.08, 0.15, 0.28][layer];
  const speed = [0.03, 0.08, 0.15][layer];

  const blobs: CloudBlob[] = [];
  for (let i = 0; i < blobCount; i++) {
    const spread = baseRadius * 0.7;
    blobs.push({
      ox: (Math.random() - 0.5) * spread,
      oy: (Math.random() - 0.5) * spread * 0.3,
      radius: baseRadius * (0.5 + Math.random() * 0.5),
      opacity: baseOpacity * (0.7 + Math.random() * 0.3),
    });
  }

  // Avoid center zone where text is
  const centerY = h / 2;
  const exclusion = h * 0.22;
  let y: number;
  do {
    y = Math.random() * h;
  } while (Math.abs(y - centerY) < exclusion && Math.random() < 0.75);

  return {
    x: Math.random() * (w + 400) - 200,
    y,
    vx: speed * (0.8 + Math.random() * 0.4),
    layer,
    blobs,
  };
}

function spawnPlane(
  w: number,
  h: number,
  sizeMin: number,
  sizeRange: number,
): Plane {
  // Primarily left-to-right with occasional right-to-left
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

// ── Theme type for plane rendering ──────────────────────────────────────────────

type PlaneTheme = typeof DARK;

// ── Draw P-80 ──────────────────────────────────────────────────────────────────

function drawP80(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  angle: number,
  size: number,
  alpha: number,
  fillFn: (a: number) => string,
  strokeFn: (a: number) => string,
  theme?: PlaneTheme,
) {
  ctx.save();
  ctx.translate(x, y);
  ctx.rotate(angle);
  ctx.globalAlpha = alpha;

  const s = size;

  // ── P-80 Shooting Star top-down view ────────────────────────────────────
  // Compact cigar fuselage, nose intake, straight unswept mid-wings,
  // large torpedo-shaped wingtip fuel tanks, bubble canopy set forward,
  // conventional tail with horizontal stabilizers.

  // ── 1. Fuselage (shorter, fatter cigar shape) ──────────────────────────
  ctx.beginPath();
  // Nose — short rounded cone with intake
  ctx.moveTo(s * 0.95, 0);
  ctx.quadraticCurveTo(s * 0.88, -s * 0.07, s * 0.7, -s * 0.09);
  // Forward fuselage widens to cockpit area
  ctx.lineTo(s * 0.2, -s * 0.11);
  // Mid fuselage (widest at wing root)
  ctx.lineTo(-s * 0.1, -s * 0.11);
  // Aft fuselage tapers
  ctx.lineTo(-s * 0.55, -s * 0.08);
  ctx.lineTo(-s * 0.85, -s * 0.05);
  // Tail exhaust
  ctx.lineTo(-s * 0.95, -s * 0.03);
  ctx.lineTo(-s * 0.95, s * 0.03);
  // Mirror bottom
  ctx.lineTo(-s * 0.85, s * 0.05);
  ctx.lineTo(-s * 0.55, s * 0.08);
  ctx.lineTo(-s * 0.1, s * 0.11);
  ctx.lineTo(s * 0.2, s * 0.11);
  ctx.lineTo(s * 0.7, s * 0.09);
  ctx.quadraticCurveTo(s * 0.88, s * 0.07, s * 0.95, 0);
  ctx.closePath();

  // Very subtle body gradient
  const bodyGrad = ctx.createLinearGradient(0, -s * 0.12, 0, s * 0.12);
  if (theme) {
    bodyGrad.addColorStop(0, theme.planeHighlight(alpha * 0.3));
    bodyGrad.addColorStop(0.4, fillFn(alpha));
    bodyGrad.addColorStop(1, theme.planeShadow(alpha * 0.12));
  } else {
    bodyGrad.addColorStop(0, fillFn(alpha));
    bodyGrad.addColorStop(1, fillFn(alpha));
  }
  ctx.fillStyle = bodyGrad;
  ctx.fill();
  ctx.strokeStyle = strokeFn(alpha);
  ctx.lineWidth = 0.5;
  ctx.stroke();

  // ── 2. Main wings (straight, unswept, thick chord) ─────────────────────
  // Top wing
  ctx.beginPath();
  ctx.moveTo(s * 0.15, -s * 0.11);    // leading edge at root
  ctx.lineTo(s * 0.05, -s * 0.7);     // leading edge at tip
  ctx.lineTo(-s * 0.05, -s * 0.73);   // blunt wingtip
  ctx.lineTo(-s * 0.12, -s * 0.7);    // trailing edge at tip
  ctx.lineTo(-s * 0.25, -s * 0.11);   // trailing edge at root
  ctx.closePath();
  ctx.fillStyle = fillFn(alpha);
  ctx.fill();
  ctx.strokeStyle = strokeFn(alpha);
  ctx.lineWidth = 0.4;
  ctx.stroke();

  // Bottom wing
  ctx.beginPath();
  ctx.moveTo(s * 0.15, s * 0.11);
  ctx.lineTo(s * 0.05, s * 0.7);
  ctx.lineTo(-s * 0.05, s * 0.73);
  ctx.lineTo(-s * 0.12, s * 0.7);
  ctx.lineTo(-s * 0.25, s * 0.11);
  ctx.closePath();
  ctx.fillStyle = fillFn(alpha);
  ctx.fill();
  ctx.stroke();

  // ── 3. Wingtip fuel tanks (large torpedo pods) ─────────────────────────
  // These are a signature P-80 feature — big and visible
  // Top tip tank
  ctx.beginPath();
  ctx.ellipse(-s * 0.02, -s * 0.76, s * 0.16, s * 0.04, 0, 0, Math.PI * 2);
  ctx.fillStyle = fillFn(alpha);
  ctx.fill();
  ctx.strokeStyle = strokeFn(alpha);
  ctx.lineWidth = 0.4;
  ctx.stroke();

  // Bottom tip tank
  ctx.beginPath();
  ctx.ellipse(-s * 0.02, s * 0.76, s * 0.16, s * 0.04, 0, 0, Math.PI * 2);
  ctx.fillStyle = fillFn(alpha);
  ctx.fill();
  ctx.stroke();

  // ── 4. Horizontal tail stabilizers ─────────────────────────────────────
  // Top stabilizer
  ctx.beginPath();
  ctx.moveTo(-s * 0.6, -s * 0.06);
  ctx.lineTo(-s * 0.68, -s * 0.28);
  ctx.lineTo(-s * 0.76, -s * 0.3);
  ctx.lineTo(-s * 0.88, -s * 0.24);
  ctx.lineTo(-s * 0.9, -s * 0.06);
  ctx.closePath();
  ctx.fillStyle = fillFn(alpha);
  ctx.fill();
  ctx.strokeStyle = strokeFn(alpha);
  ctx.lineWidth = 0.4;
  ctx.stroke();

  // Bottom stabilizer
  ctx.beginPath();
  ctx.moveTo(-s * 0.6, s * 0.06);
  ctx.lineTo(-s * 0.68, s * 0.28);
  ctx.lineTo(-s * 0.76, s * 0.3);
  ctx.lineTo(-s * 0.88, s * 0.24);
  ctx.lineTo(-s * 0.9, s * 0.06);
  ctx.closePath();
  ctx.fillStyle = fillFn(alpha);
  ctx.fill();
  ctx.stroke();

  if (!theme) {
    ctx.globalAlpha = 1;
    ctx.restore();
    return;
  }

  // ── 5. Bubble canopy (large, prominent, set forward) ───────────────────
  const canopyX = s * 0.42;
  const canopyRx = s * 0.2;
  const canopyRy = s * 0.07;

  // Canopy outline — teardrop shape (wider at back, narrow at front)
  ctx.beginPath();
  ctx.ellipse(canopyX, 0, canopyRx, canopyRy, 0, 0, Math.PI * 2);

  // Bright canopy fill so it stands out
  const canopyGrad = ctx.createLinearGradient(
    canopyX, -canopyRy * 1.2, canopyX, canopyRy * 1.2,
  );
  canopyGrad.addColorStop(0, theme.canopy(alpha * 1.4));
  canopyGrad.addColorStop(0.4, theme.canopy(alpha * 0.8));
  canopyGrad.addColorStop(1, theme.canopy(alpha * 0.3));
  ctx.fillStyle = canopyGrad;
  ctx.fill();

  // Canopy frame
  ctx.strokeStyle = theme.canopyEdge(alpha * 1.2);
  ctx.lineWidth = 0.5;
  ctx.stroke();

  // Bright glint on canopy glass
  ctx.beginPath();
  ctx.ellipse(
    canopyX + canopyRx * 0.15, -canopyRy * 0.3,
    canopyRx * 0.3, canopyRy * 0.25, -0.15, 0, Math.PI * 2,
  );
  ctx.fillStyle = theme.planeHighlight(alpha * 1.2);
  ctx.fill();

  // ── 6. Nose intake (visible dark oval) ─────────────────────────────────
  ctx.beginPath();
  ctx.ellipse(s * 0.9, 0, s * 0.02, s * 0.04, 0, 0, Math.PI * 2);
  ctx.fillStyle = theme.planeShadow(alpha * 0.7);
  ctx.fill();

  // ── 7. Fuselage panel line ─────────────────────────────────────────────
  ctx.beginPath();
  ctx.moveTo(s * 0.75, 0);
  ctx.lineTo(-s * 0.85, 0);
  ctx.strokeStyle = theme.panelLine(alpha);
  ctx.lineWidth = 0.4;
  ctx.stroke();

  // ── 8. Exhaust nozzle ──────────────────────────────────────────────────
  ctx.beginPath();
  ctx.ellipse(-s * 0.93, 0, s * 0.015, s * 0.025, 0, 0, Math.PI * 2);
  ctx.fillStyle = theme.planeShadow(alpha * 0.5);
  ctx.fill();

  // ── 9. Tip tank detail lines ───────────────────────────────────────────
  // Subtle seam on each tank
  ctx.beginPath();
  ctx.moveTo(-s * 0.16, -s * 0.76);
  ctx.lineTo(s * 0.12, -s * 0.76);
  ctx.strokeStyle = theme.panelLine(alpha);
  ctx.lineWidth = 0.3;
  ctx.stroke();

  ctx.beginPath();
  ctx.moveTo(-s * 0.16, s * 0.76);
  ctx.lineTo(s * 0.12, s * 0.76);
  ctx.stroke();

  ctx.globalAlpha = 1;
  ctx.restore();
}

// ── Draw cloud (soft radial gradients, no hard edges) ──────────────────────────

function drawCloudSoft(ctx: CanvasRenderingContext2D, cloud: Cloud) {
  for (const b of cloud.blobs) {
    const bx = cloud.x + b.ox;
    const by = cloud.y + b.oy;
    const r = b.radius;

    const grad = ctx.createRadialGradient(bx, by, 0, bx, by, r);
    grad.addColorStop(0, `rgba(255, 255, 255, ${b.opacity})`);
    grad.addColorStop(0.3, `rgba(255, 255, 255, ${b.opacity * 0.7})`);
    grad.addColorStop(0.6, `rgba(255, 255, 255, ${b.opacity * 0.3})`);
    grad.addColorStop(1, `rgba(255, 255, 255, 0)`);
    ctx.fillStyle = grad;
    ctx.fillRect(bx - r, by - r, r * 2, r * 2);
  }
}

// ── Draw sky gradient ──────────────────────────────────────────────────────────

function drawSkyGradient(
  ctx: CanvasRenderingContext2D,
  w: number,
  h: number,
) {
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
  const isDarkRef = useRef(true);
  const [isDark, setIsDark] = useState(true);

  // Detect system color scheme
  useEffect(() => {
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const update = () => {
      const dark = mq.matches;
      isDarkRef.current = dark;
      setIsDark(dark);
    };
    update();
    mq.addEventListener("change", update);
    return () => mq.removeEventListener("change", update);
  }, []);

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
      bgStarsRef.current = Array.from({ length: cfg.starCount }, () =>
        createBackgroundStar(w, h),
      );
      cloudsRef.current = Array.from({ length: cfg.cloudCount }, () =>
        createCloud(w, h),
      );
    };

    resize();

    // Seed a few initial planes in the periphery (avoiding center text)
    const { w, h } = dimsRef.current;
    const cfg = configRef.current;
    for (let i = 0; i < Math.min(3, cfg.maxPlanes); i++) {
      const p = spawnPlane(w, h, cfg.planeSizeMin, cfg.planeSizeRange);
      // Place in left or right third, outside the content zone
      const side = Math.random() > 0.5;
      p.x = side ? w * 0.05 + Math.random() * w * 0.2 : w * 0.75 + Math.random() * w * 0.2;
      p.y = Math.random() * h * 0.3 + (Math.random() > 0.5 ? 0 : h * 0.65);
      p.baseY = p.y;
      p.opacity = 1;
      p.life = 120;
      planesRef.current.push(p);
    }

    // ── Render loop ──────────────────────────────────────────────────────────

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
        // Adopt formation heading so planes continue smoothly instead of snapping back
        for (const p of planesRef.current) {
          if (formation.slotMap.has(p.id)) {
            p.baseHeading = formation.heading;
            p.baseY = p.y;
            p.speed = formation.speed;
          }
        }
        formation.active = false;
      }

      // ── Background ─────────────────────────────────────────────────────────
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

        // Subtle sun glow
        const grad = ctx.createRadialGradient(
          w * 0.8,
          h * 0.05,
          0,
          w * 0.8,
          h * 0.05,
          w * 0.3,
        );
        grad.addColorStop(0, "rgba(255, 248, 220, 0.4)");
        grad.addColorStop(0.5, "rgba(255, 248, 220, 0.1)");
        grad.addColorStop(1, "transparent");
        ctx.fillStyle = grad;
        ctx.fillRect(0, 0, w, h);
      }

      // ── Stars (dark) or Clouds (light) ──────────────────────────────────
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
      } else {
        for (const c of cloudsRef.current) {
          c.x += c.vx;
          if (c.x - 250 > w) {
            c.x = -300;
            const centerY = h / 2;
            const exclusion = h * 0.22;
            let newY: number;
            do {
              newY = Math.random() * h;
            } while (
              Math.abs(newY - centerY) < exclusion &&
              Math.random() < 0.75
            );
            c.y = newY;
          }
          drawCloudSoft(ctx, c);
        }
      }

      // ── Spawn new planes ───────────────────────────────────────────────────
      if (
        planesRef.current.length < cfg.maxPlanes &&
        Math.random() < 0.003
      ) {
        planesRef.current.push(
          spawnPlane(w, h, cfg.planeSizeMin, cfg.planeSizeRange),
        );
      }

      // ── Update and draw planes ─────────────────────────────────────────────
      const alive: Plane[] = [];

      for (const p of planesRef.current) {
        const slot = formation.active
          ? formation.slotMap.get(p.id)
          : undefined;
        const inFormation = slot !== undefined;

        if (inFormation) {
          // Formation: spring-like easing toward slot
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
          // Directional flight with gentle sine oscillation
          p.x += Math.cos(p.baseHeading) * p.speed;

          const oscillation =
            Math.sin(time * p.frequency + p.phase) * p.amplitude;
          const targetY = p.baseY + oscillation;
          p.y += (targetY - p.y) * 0.015;

          // Subtle mouse influence
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

        // Smooth fade in; fade at screen edges
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

        // Record trail
        p.trail.push({ x: p.x, y: p.y });
        while (p.trail.length > p.maxTrailLength) p.trail.shift();

        // Remove if off screen
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

        // ── Draw contrail ────────────────────────────────────────────────────
        if (p.trail.length > 2) {
          for (let i = 2; i < p.trail.length; i++) {
            const prev = p.trail[i - 1];
            const cur = p.trail[i];
            const progress = i / p.trail.length;
            // Contrail: thin at start, thicker near plane, fades out at tail
            const trailAlpha = progress * p.opacity * 0.6;
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

        // ── Draw plane ───────────────────────────────────────────────────────
        // Use baseHeading for stable visual orientation (no jitter)
        const heading = p.baseHeading;
        drawP80(
          ctx,
          p.x,
          p.y,
          heading,
          p.size,
          p.opacity,
          theme.planeFill,
          theme.planeStroke,
          theme,
        );
      }

      planesRef.current = alive;

      animId = requestAnimationFrame(frame);
    };

    animId = requestAnimationFrame(frame);

    // ── Events ───────────────────────────────────────────────────────────────

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
        p.baseHeading = heading; // visually align with formation direction
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

  return (
    <div
      className="relative min-h-screen overflow-hidden"
      style={{ background: isDark ? DARK.bg : "#87BBDF" }}
    >
      <canvas ref={canvasRef} className="fixed inset-0 z-0" />

      {/* ── Top nav ───────────────────────────────────────────────────────── */}
      <div className="relative z-10 flex items-center justify-end px-6 sm:px-10 pt-6 sm:pt-8 pointer-events-auto">
        <Button
          asChild
          variant="outline"
          className={`rounded-full px-5 py-2 text-sm font-medium transition-all ${
            isDark
              ? "border-white/20 text-white/60 hover:text-white hover:border-white/40 bg-transparent"
              : "border-slate-400/40 text-slate-600 hover:text-slate-900 hover:border-slate-500 bg-transparent"
          }`}
        >
          <Link href="/login">Sign In</Link>
        </Button>
      </div>

      {/* ── Bottom-left hero ───────────────────────────────────────────────── */}
      <div className="relative z-10 flex min-h-[calc(100vh-80px)] flex-col justify-end px-6 sm:px-10 pb-12 sm:pb-16 select-none">
        <div className="max-w-xl space-y-5">
          <h1
            className={`text-[2.75rem] sm:text-[3.5rem] md:text-6xl font-light leading-[1.1] tracking-tight ${
              isDark ? "text-white" : "text-slate-900"
            }`}
          >
            Open source
            <br />
            bug fixing for
            <br />
            production systems
          </h1>

          <p
            className={`max-w-md text-sm sm:text-base leading-relaxed ${isDark ? "text-white/40" : "text-slate-600"}`}
          >
            Connect your issue tracker and error monitoring. 143 spins
            up an agent, validates the fix, and opens a PR, all before
            you even wake up.
          </p>

          <div className="pt-2 pointer-events-auto">
            <Button
              asChild
              className={`rounded-full px-6 py-2.5 text-sm font-medium transition-all ${
                isDark
                  ? "bg-white text-[#08080f] hover:bg-white/90"
                  : "bg-slate-900 text-white hover:bg-slate-800"
              }`}
            >
              <Link href="/login?tab=signup">
                Get Started
                <span className="ml-2">&rsaquo;</span>
              </Link>
            </Button>
          </div>
        </div>
      </div>

      {/* ── Bottom-right: 143 origin story ─────────────────────────────────── */}
      <div className={`absolute bottom-12 right-6 sm:right-10 z-10 hidden md:block max-w-[280px] text-right ${isDark ? "text-white/40" : "text-slate-600"}`}>
        <p className="text-[11px] leading-relaxed tracking-wide">
          The first US jet fighter, the P-80 Shooting Star,
          was designed and built by Lockheed&apos;s Skunk Works
          in just 143&nbsp;days. We named this project after
          that same spirit of speed.
        </p>
      </div>
    </div>
  );
}
