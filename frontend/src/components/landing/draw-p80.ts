// ── Shared P-80 Shooting Star drawing ───────────────────────────────────────
// Used by both hero-canvas.tsx and airfield-canvas.tsx

export type PlaneTheme = {
  bg: string;
  planeFill: (a: number) => string;
  planeStroke: (a: number) => string;
  planeHighlight: (a: number) => string;
  planeShadow: (a: number) => string;
  canopy: (a: number) => string;
  canopyEdge: (a: number) => string;
  panelLine: (a: number) => string;
  trail: (a: number) => string;
  star: (a: number) => string;
  orbs: Array<{ color: string }>;
};

export const DARK: PlaneTheme = {
  bg: "#11110f",
  planeFill: (a: number) => `rgba(221, 219, 212, ${a})`,
  planeStroke: (a: number) => `rgba(244, 243, 238, ${a * 0.3})`,
  planeHighlight: (a: number) => `rgba(244, 243, 238, ${a * 0.25})`,
  planeShadow: (a: number) => `rgba(0, 0, 0, ${a * 0.3})`,
  canopy: (a: number) => `rgba(121, 146, 255, ${a * 0.58})`,
  canopyEdge: (a: number) => `rgba(200, 210, 255, ${a * 0.32})`,
  panelLine: (a: number) => `rgba(244, 243, 238, ${a * 0.08})`,
  trail: (a: number) => `rgba(200, 210, 255, ${a})`,
  star: (a: number) => `rgba(244, 243, 238, ${a})`,
  orbs: [
    { color: "rgba(80, 108, 255, 0.14)" },
    { color: "rgba(121, 146, 255, 0.08)" },
  ],
};

export const LIGHT: PlaneTheme = {
  bg: "#f6f5f0",
  planeFill: (a: number) => `rgba(52, 52, 48, ${a})`,
  planeStroke: (a: number) => `rgba(27, 27, 25, ${a * 0.25})`,
  planeHighlight: (a: number) => `rgba(255, 255, 255, ${a * 0.2})`,
  planeShadow: (a: number) => `rgba(27, 27, 25, ${a * 0.22})`,
  canopy: (a: number) => `rgba(49, 92, 232, ${a * 0.62})`,
  canopyEdge: (a: number) => `rgba(35, 59, 145, ${a * 0.35})`,
  panelLine: (a: number) => `rgba(0, 0, 0, ${a * 0.06})`,
  trail: (a: number) => `rgba(255, 255, 255, ${a * 0.95})`,
  star: () => "transparent",
  orbs: [
    { color: "rgba(49, 92, 232, 0.12)" },
    { color: "rgba(255, 255, 255, 0.24)" },
  ],
};

// ── Default airfield-style color functions (grays/blues) ────────────────────

const defaultFill = (a: number) => `rgba(58, 64, 80, ${a})`;
const defaultStroke = (a: number) => `rgba(255, 255, 255, ${a * 0.12})`;
const defaultHighlight = (a: number) => `rgba(180, 190, 210, ${a * 0.85})`;
const defaultShadow = (a: number) => `rgba(30, 35, 50, ${a * 0.7})`;
const defaultCanopy = (a: number) => `rgba(140, 180, 255, ${a * 0.5})`;
const defaultCanopyEdge = (a: number) => `rgba(180, 210, 255, ${a * 0.25})`;
const defaultPanelLine = (a: number) => `rgba(255, 255, 255, ${a * 0.06})`;

// ── Draw P-80 with 3/4 perspective ──────────────────────────────────────────
//
// Perspective technique: instead of complex geometry that turns muddy at small
// sizes, we rely on three cues that read well even at 20-40px:
//
//  1. Y-axis compression — foreshortens wingspan naturally
//  2. Wing colour split  — far wing darker, near wing lighter
//  3. Shadow drawn OUTSIDE globalAlpha so it never bleeds through semi-
//     transparent fills during hero fade-in
//

/**
 * Draw a P-80 Shooting Star with optional 3/4 perspective.
 *
 * @param ctx       Canvas context
 * @param x         Center X
 * @param y         Center Y
 * @param size      Overall scale
 * @param rotation  Heading in radians (nose direction)
 * @param viewAngle 0 = flat top-down, 1 = full side view. Default 0.4
 * @param alpha     Opacity (0–1). Default 1
 * @param theme     Optional PlaneTheme; when absent uses airfield-style grays
 */
export function drawP80(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  size: number,
  rotation: number,
  viewAngle = 0.4,
  alpha = 1,
  theme?: PlaneTheme,
) {
  const s = size;

  // Clamp effective viewAngle for small sizes to avoid sub-pixel muddiness
  const va = s < 20 ? Math.min(viewAngle, 0.15) : viewAngle;

  // Color functions — use theme if provided, else airfield defaults
  const fillFn = theme ? theme.planeFill : defaultFill;
  const strokeFn = theme ? theme.planeStroke : defaultStroke;
  const highlightFn = theme ? theme.planeHighlight : defaultHighlight;
  const shadowFn = theme ? theme.planeShadow : defaultShadow;
  const canopyFn = theme ? theme.canopy : defaultCanopy;
  const canopyEdgeFn = theme ? theme.canopyEdge : defaultCanopyEdge;
  const panelLineFn = theme ? theme.panelLine : defaultPanelLine;

  // ── Shadow — drawn BEFORE save so globalAlpha doesn't affect it ──
  // Offset proportional to size so it scales correctly, pushed toward near-wing
  const shadowOffX = s * 0.04;
  const shadowOffY = s * 0.06 * (1 + va);
  ctx.save();
  ctx.translate(x, y);
  ctx.rotate(rotation);
  ctx.globalAlpha = alpha * 0.18;
  ctx.fillStyle = "rgba(0,0,0,1)";
  ctx.beginPath();
  ctx.ellipse(shadowOffX, shadowOffY, s * 0.85, s * 0.12, 0, 0, Math.PI * 2);
  ctx.fill();
  ctx.restore();

  // ── Main plane ──
  ctx.save();
  ctx.translate(x, y);
  ctx.rotate(rotation);

  // Y-axis compression for natural foreshortening
  const ySquash = 1 - va * 0.1;
  ctx.scale(1, ySquash);

  ctx.globalAlpha = alpha;

  // Wing asymmetry — subtle span difference + colour is the real cue
  const farScale = 1 - va * 0.2;   // far wing slightly shorter
  const nearScale = 1 + va * 0.08; // near wing slightly longer

  // Tint multipliers for wing colour split (the primary depth cue)
  const farDim = 1 - va * 0.3;     // far wing darker
  const nearBright = 1 + va * 0.15; // near wing lighter

  // ── FAR WING (drawn first, behind fuselage) ──
  const farWingTip = -s * 0.73 * farScale;
  const farTipTankY = -s * 0.76 * farScale;

  ctx.fillStyle = fillFn(alpha * farDim);
  ctx.beginPath();
  ctx.moveTo(s * 0.15, -s * 0.11);
  ctx.lineTo(s * 0.05, -s * 0.7 * farScale);
  ctx.lineTo(-s * 0.05, farWingTip);
  ctx.lineTo(-s * 0.12, -s * 0.7 * farScale);
  ctx.lineTo(-s * 0.25, -s * 0.11);
  ctx.closePath();
  ctx.fill();
  ctx.strokeStyle = strokeFn(alpha * farDim);
  ctx.lineWidth = 0.4;
  ctx.stroke();

  // Far tip tank
  ctx.beginPath();
  ctx.ellipse(-s * 0.02, farTipTankY, s * 0.14 * farScale, s * 0.035, 0, 0, Math.PI * 2);
  ctx.fillStyle = fillFn(alpha * farDim);
  ctx.fill();
  ctx.strokeStyle = strokeFn(alpha * farDim);
  ctx.lineWidth = 0.4;
  ctx.stroke();

  // Far stabilizer
  ctx.fillStyle = fillFn(alpha * farDim);
  ctx.beginPath();
  ctx.moveTo(-s * 0.60, -s * 0.06);
  ctx.lineTo(-s * 0.68, -s * 0.28 * farScale);
  ctx.lineTo(-s * 0.78, -s * 0.29 * farScale);
  ctx.lineTo(-s * 0.88, -s * 0.22 * farScale);
  ctx.lineTo(-s * 0.85, -s * 0.06);
  ctx.closePath();
  ctx.fill();
  ctx.strokeStyle = strokeFn(alpha * farDim);
  ctx.lineWidth = 0.4;
  ctx.stroke();

  // ── Fuselage ──
  ctx.beginPath();
  ctx.moveTo(s * 0.95, 0);
  ctx.quadraticCurveTo(s * 0.88, -s * 0.07, s * 0.7, -s * 0.10);
  ctx.lineTo(-s * 0.55, -s * 0.09);
  ctx.lineTo(-s * 0.85, -s * 0.05);
  ctx.lineTo(-s * 0.92, 0);
  ctx.lineTo(-s * 0.85, s * 0.05);
  ctx.lineTo(-s * 0.55, s * 0.09);
  ctx.lineTo(s * 0.7, s * 0.10);
  ctx.quadraticCurveTo(s * 0.88, s * 0.07, s * 0.95, 0);
  ctx.closePath();

  // Gentle gradient: far side catches light, near side in subtle shadow
  const bodyGrad = ctx.createLinearGradient(0, -s * 0.14, 0, s * 0.14);
  bodyGrad.addColorStop(0, highlightFn(alpha * 0.3));
  bodyGrad.addColorStop(0.4, fillFn(alpha));
  bodyGrad.addColorStop(1, shadowFn(alpha * 0.15));
  ctx.fillStyle = bodyGrad;
  ctx.fill();
  ctx.strokeStyle = strokeFn(alpha);
  ctx.lineWidth = 0.5;
  ctx.stroke();

  // ── Vertical stabilizer ridge (subtle, like original planform) ──
  if (va > 0.1) {
    ctx.fillStyle = fillFn(alpha * (0.5 + va * 0.3));
    ctx.beginPath();
    ctx.moveTo(-s * 0.55, 0);
    ctx.lineTo(-s * 0.72, -s * 0.015 * (1 + va * 2));
    ctx.lineTo(-s * 0.88, -s * 0.012 * (1 + va * 2));
    ctx.lineTo(-s * 0.88, s * 0.012);
    ctx.lineTo(-s * 0.72, s * 0.015);
    ctx.closePath();
    ctx.fill();
  }

  // ── NEAR WING (drawn after fuselage, overlaps at root) ──
  const nearWingTip = s * 0.73 * nearScale;
  const nearTipTankY = s * 0.76 * nearScale;

  ctx.fillStyle = fillFn(Math.min(1, alpha * nearBright));
  ctx.beginPath();
  ctx.moveTo(s * 0.15, s * 0.11);
  ctx.lineTo(s * 0.05, s * 0.7 * nearScale);
  ctx.lineTo(-s * 0.05, nearWingTip);
  ctx.lineTo(-s * 0.12, s * 0.7 * nearScale);
  ctx.lineTo(-s * 0.25, s * 0.11);
  ctx.closePath();
  ctx.fill();
  ctx.strokeStyle = strokeFn(alpha);
  ctx.lineWidth = 0.4;
  ctx.stroke();

  // Near tip tank
  ctx.beginPath();
  ctx.ellipse(-s * 0.02, nearTipTankY, s * 0.14 * nearScale, s * 0.035, 0, 0, Math.PI * 2);
  ctx.fillStyle = fillFn(Math.min(1, alpha * nearBright));
  ctx.fill();
  ctx.strokeStyle = strokeFn(alpha);
  ctx.lineWidth = 0.4;
  ctx.stroke();

  // Near stabilizer
  ctx.fillStyle = fillFn(Math.min(1, alpha * nearBright));
  ctx.beginPath();
  ctx.moveTo(-s * 0.60, s * 0.06);
  ctx.lineTo(-s * 0.68, s * 0.28 * nearScale);
  ctx.lineTo(-s * 0.78, s * 0.29 * nearScale);
  ctx.lineTo(-s * 0.88, s * 0.22 * nearScale);
  ctx.lineTo(-s * 0.85, s * 0.06);
  ctx.closePath();
  ctx.fill();
  ctx.strokeStyle = strokeFn(alpha);
  ctx.lineWidth = 0.4;
  ctx.stroke();

  // ── Bubble canopy ──
  const canopyX = s * 0.42;
  const canopyRx = s * 0.18;
  const canopyRy = s * 0.06;

  ctx.beginPath();
  ctx.ellipse(canopyX, 0, canopyRx, canopyRy, 0, 0, Math.PI * 2);

  const canopyGrad = ctx.createLinearGradient(
    canopyX, -canopyRy * 1.2, canopyX, canopyRy * 1.2,
  );
  canopyGrad.addColorStop(0, canopyFn(alpha * 1.4));
  canopyGrad.addColorStop(0.4, canopyFn(alpha * 0.8));
  canopyGrad.addColorStop(1, canopyFn(alpha * 0.3));
  ctx.fillStyle = canopyGrad;
  ctx.fill();

  ctx.strokeStyle = canopyEdgeFn(alpha * 1.2);
  ctx.lineWidth = 0.5;
  ctx.stroke();

  // Canopy glint
  ctx.beginPath();
  ctx.ellipse(
    canopyX + canopyRx * 0.15, -canopyRy * 0.3,
    canopyRx * 0.3, canopyRy * 0.25, -0.15, 0, Math.PI * 2,
  );
  ctx.fillStyle = highlightFn(alpha * 1.2);
  ctx.fill();

  // ── Nose intake ──
  ctx.beginPath();
  ctx.ellipse(s * 0.90, 0, s * 0.02, s * 0.04, 0, 0, Math.PI * 2);
  ctx.fillStyle = shadowFn(alpha * 0.7);
  ctx.fill();

  // ── Panel line ──
  ctx.beginPath();
  ctx.moveTo(s * 0.75, 0);
  ctx.lineTo(-s * 0.80, 0);
  ctx.strokeStyle = panelLineFn(alpha);
  ctx.lineWidth = 0.4;
  ctx.stroke();

  // ── Exhaust ──
  ctx.beginPath();
  ctx.ellipse(-s * 0.91, 0, s * 0.015, s * 0.025, 0, 0, Math.PI * 2);
  ctx.fillStyle = shadowFn(alpha * 0.5);
  ctx.fill();

  // ── Engine glow ──
  ctx.fillStyle = `rgba(255, 140, 40, ${0.15 * alpha})`;
  ctx.beginPath();
  ctx.ellipse(-s * 0.95, 0, s * 0.04, s * 0.03, 0, 0, Math.PI * 2);
  ctx.fill();

  // ── Tip tank seams ──
  ctx.strokeStyle = panelLineFn(alpha);
  ctx.lineWidth = 0.3;
  ctx.beginPath();
  ctx.moveTo(-s * 0.15, farTipTankY);
  ctx.lineTo(s * 0.10, farTipTankY);
  ctx.stroke();

  ctx.beginPath();
  ctx.moveTo(-s * 0.15, nearTipTankY);
  ctx.lineTo(s * 0.10, nearTipTankY);
  ctx.stroke();

  ctx.globalAlpha = 1;
  ctx.restore();
}

// ── Draw P-80 side profile (hyper-realistic) ────────────────────────────────
//
// For scenes where the camera is external (watching the plane fly past).
// Nose points in the +X direction; use rotation to face any heading.
// Automatically corrects Y-flip when facing left (|rotation| > π/2).
//

/**
 * Draw a hyper-realistic P-80 Shooting Star from the side.
 *
 * @param ctx       Canvas context
 * @param x         Center X
 * @param y         Center Y
 * @param size      Overall scale (fuselage length ≈ 2×size)
 * @param rotation  Heading in radians (nose direction)
 * @param alpha     Opacity (0–1). Default 1
 * @param noseDown  Pitch angle in radians (positive = nose down). Default 0
 * @param opts      Optional: { noShadow, gearDown }
 */
export function drawP80Side(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  size: number,
  rotation: number,
  alpha = 1,
  noseDown = 0,
  opts: { noShadow?: boolean; gearDown?: boolean; perspective?: number } = {},
) {
  ctx.save();
  ctx.translate(x, y);

  const facingLeft = Math.cos(rotation) < 0;
  if (facingLeft) {
    ctx.rotate(rotation);
    ctx.scale(1, -1);
    ctx.rotate(-noseDown);
  } else {
    ctx.rotate(rotation + noseDown);
  }
  ctx.globalAlpha = alpha;

  const s = size;

  // ── Fuselage shadow (on ground / beneath) ──
  if (!opts.noShadow) {
    ctx.save();
    ctx.globalAlpha = alpha * 0.12;
    ctx.fillStyle = "rgba(0, 0, 0, 1)";
    ctx.beginPath();
    ctx.ellipse(0, s * 0.22, s * 0.7, s * 0.025, 0, 0, Math.PI * 2);
    ctx.fill();
    ctx.restore();
  }

  // ── Far-side perspective elements (rear-quarter elevated view) ──
  const persp = opts.perspective ?? 0;
  if (persp > 0) {
    const farDim = alpha * persp;
    const farOff = s * persp * 0.06;

    // Far horizontal stabilizer
    ctx.fillStyle = `rgba(45, 52, 68, ${farDim * 0.5})`;
    ctx.beginPath();
    ctx.moveTo(-s * 0.52, -s * 0.01 - farOff);
    ctx.quadraticCurveTo(-s * 0.65, -s * 0.06 - farOff, -s * 0.80, -s * 0.05 - farOff);
    ctx.lineTo(-s * 0.92, -s * 0.03 - farOff);
    ctx.lineTo(-s * 0.92, -s * 0.01 - farOff);
    ctx.quadraticCurveTo(-s * 0.80, -s * 0.005 - farOff, -s * 0.52, s * 0.01 - farOff);
    ctx.closePath();
    ctx.fill();

    // Far wing (planform visible above fuselage from elevated angle)
    const fwTopY = -s * 0.12 - s * persp * 0.055;
    const fwBotY = -s * 0.115;
    const fwGrad = ctx.createLinearGradient(0, fwTopY, 0, fwBotY);
    fwGrad.addColorStop(0, `rgba(38, 45, 60, ${farDim * 0.55})`);
    fwGrad.addColorStop(1, `rgba(55, 62, 78, ${farDim * 0.45})`);
    ctx.fillStyle = fwGrad;
    ctx.beginPath();
    ctx.moveTo(s * 0.14, fwBotY);
    ctx.lineTo(s * 0.06, fwTopY);
    ctx.lineTo(-s * 0.08, fwTopY);
    ctx.lineTo(-s * 0.18, fwTopY + (fwBotY - fwTopY) * 0.3);
    ctx.lineTo(-s * 0.26, fwBotY);
    ctx.closePath();
    ctx.fill();
    // Far wing leading edge highlight
    ctx.strokeStyle = `rgba(160, 170, 190, ${farDim * 0.2})`;
    ctx.lineWidth = 0.5;
    ctx.beginPath();
    ctx.moveTo(s * 0.14, fwBotY);
    ctx.lineTo(s * 0.06, fwTopY);
    ctx.stroke();

    // Far tip tank
    ctx.fillStyle = `rgba(48, 55, 70, ${farDim * 0.5})`;
    ctx.beginPath();
    ctx.ellipse(-s * 0.04, fwTopY - s * 0.008, s * 0.09, s * 0.015 * persp, 0, 0, Math.PI * 2);
    ctx.fill();
  }

  // ── Horizontal stabilizer (behind fuselage) ──
  const stabGrad = ctx.createLinearGradient(-s * 0.58, -s * 0.04, -s * 0.58, s * 0.02);
  stabGrad.addColorStop(0, "rgba(75, 82, 98, 0.85)");
  stabGrad.addColorStop(0.4, "rgba(55, 62, 78, 0.82)");
  stabGrad.addColorStop(1, "rgba(40, 48, 62, 0.75)");
  ctx.fillStyle = stabGrad;
  ctx.beginPath();
  ctx.moveTo(-s * 0.52, -s * 0.01);
  ctx.quadraticCurveTo(-s * 0.62, -s * 0.05, -s * 0.72, -s * 0.048);
  ctx.quadraticCurveTo(-s * 0.82, -s * 0.045, -s * 0.90, -s * 0.03);
  ctx.lineTo(-s * 0.92, -s * 0.02);
  ctx.lineTo(-s * 0.92, s * 0.005);
  ctx.quadraticCurveTo(-s * 0.82, s * 0.015, -s * 0.72, s * 0.015);
  ctx.quadraticCurveTo(-s * 0.62, s * 0.012, -s * 0.52, s * 0.015);
  ctx.closePath();
  ctx.fill();
  ctx.strokeStyle = "rgba(255, 255, 255, 0.06)";
  ctx.lineWidth = 0.4;
  ctx.stroke();

  // Elevator hinge line
  ctx.strokeStyle = "rgba(0, 0, 0, 0.08)";
  ctx.lineWidth = 0.5;
  ctx.beginPath();
  ctx.moveTo(-s * 0.80, -s * 0.038);
  ctx.quadraticCurveTo(-s * 0.85, -s * 0.028, -s * 0.90, -s * 0.025);
  ctx.stroke();

  // ── Vertical tail fin ──
  const finGrad = ctx.createLinearGradient(-s * 0.52, -s * 0.10, -s * 0.78, -s * 0.42);
  finGrad.addColorStop(0, "rgba(72, 80, 95, 0.94)");
  finGrad.addColorStop(0.25, "rgba(62, 70, 86, 0.92)");
  finGrad.addColorStop(0.5, "rgba(52, 60, 76, 0.90)");
  finGrad.addColorStop(0.8, "rgba(42, 50, 66, 0.86)");
  finGrad.addColorStop(1, "rgba(35, 43, 58, 0.82)");
  ctx.fillStyle = finGrad;
  ctx.beginPath();
  ctx.moveTo(-s * 0.46, -s * 0.095);
  ctx.bezierCurveTo(-s * 0.50, -s * 0.18, -s * 0.54, -s * 0.28, -s * 0.58, -s * 0.34);
  ctx.bezierCurveTo(-s * 0.61, -s * 0.39, -s * 0.66, -s * 0.42, -s * 0.72, -s * 0.42);
  ctx.lineTo(-s * 0.76, -s * 0.41);
  ctx.bezierCurveTo(-s * 0.82, -s * 0.38, -s * 0.86, -s * 0.32, -s * 0.88, -s * 0.24);
  ctx.bezierCurveTo(-s * 0.90, -s * 0.16, -s * 0.90, -s * 0.08, -s * 0.88, -s * 0.04);
  ctx.closePath();
  ctx.fill();

  // Fin outline
  ctx.strokeStyle = "rgba(255, 255, 255, 0.08)";
  ctx.lineWidth = 0.5;
  ctx.stroke();

  // Fin leading-edge highlight (sun catch)
  ctx.strokeStyle = "rgba(200, 210, 225, 0.22)";
  ctx.lineWidth = 0.8;
  ctx.beginPath();
  ctx.moveTo(-s * 0.46, -s * 0.095);
  ctx.bezierCurveTo(-s * 0.50, -s * 0.18, -s * 0.54, -s * 0.28, -s * 0.58, -s * 0.34);
  ctx.bezierCurveTo(-s * 0.61, -s * 0.39, -s * 0.66, -s * 0.42, -s * 0.72, -s * 0.42);
  ctx.stroke();

  // Rudder hinge line
  ctx.strokeStyle = "rgba(0, 0, 0, 0.10)";
  ctx.lineWidth = 0.6;
  ctx.beginPath();
  ctx.moveTo(-s * 0.76, -s * 0.40);
  ctx.bezierCurveTo(-s * 0.82, -s * 0.34, -s * 0.86, -s * 0.22, -s * 0.88, -s * 0.10);
  ctx.stroke();

  // Rudder trim tab
  ctx.strokeStyle = "rgba(0, 0, 0, 0.06)";
  ctx.lineWidth = 0.4;
  ctx.beginPath();
  ctx.moveTo(-s * 0.84, -s * 0.28);
  ctx.lineTo(-s * 0.87, -s * 0.20);
  ctx.stroke();

  // Fin tip fairing
  ctx.fillStyle = "rgba(55, 62, 78, 0.7)";
  ctx.beginPath();
  ctx.ellipse(-s * 0.72, -s * 0.42, s * 0.015, s * 0.005, -0.3, 0, Math.PI * 2);
  ctx.fill();

  // ── Main fuselage body ──
  const bodyGrad = ctx.createLinearGradient(0, -s * 0.18, 0, s * 0.18);
  bodyGrad.addColorStop(0, "rgba(195, 202, 218, 0.88)");
  bodyGrad.addColorStop(0.15, "rgba(155, 165, 182, 0.92)");
  bodyGrad.addColorStop(0.3, "rgba(105, 115, 132, 0.95)");
  bodyGrad.addColorStop(0.45, "rgba(78, 88, 105, 0.94)");
  bodyGrad.addColorStop(0.6, "rgba(60, 68, 82, 0.92)");
  bodyGrad.addColorStop(0.75, "rgba(50, 58, 72, 0.90)");
  bodyGrad.addColorStop(0.9, "rgba(40, 48, 62, 0.85)");
  bodyGrad.addColorStop(1, "rgba(30, 38, 50, 0.72)");
  ctx.fillStyle = bodyGrad;

  ctx.beginPath();
  // Nose — smooth ogive
  ctx.moveTo(s * 0.96, 0);
  ctx.bezierCurveTo(s * 0.94, -s * 0.025, s * 0.92, -s * 0.05, s * 0.88, -s * 0.068);
  ctx.bezierCurveTo(s * 0.82, -s * 0.085, s * 0.74, -s * 0.098, s * 0.64, -s * 0.105);
  // Upper fuselage contour
  ctx.bezierCurveTo(s * 0.50, -s * 0.112, s * 0.35, -s * 0.118, s * 0.20, -s * 0.12);
  ctx.bezierCurveTo(s * 0.05, -s * 0.122, -s * 0.10, -s * 0.12, -s * 0.22, -s * 0.115);
  ctx.bezierCurveTo(-s * 0.38, -s * 0.108, -s * 0.52, -s * 0.095, -s * 0.62, -s * 0.075);
  ctx.bezierCurveTo(-s * 0.72, -s * 0.058, -s * 0.80, -s * 0.045, -s * 0.86, -s * 0.035);
  // Tail
  ctx.lineTo(-s * 0.92, -s * 0.022);
  ctx.lineTo(-s * 0.93, -s * 0.012);
  ctx.lineTo(-s * 0.93, s * 0.032);
  ctx.lineTo(-s * 0.92, s * 0.042);
  // Lower fuselage contour
  ctx.bezierCurveTo(-s * 0.86, s * 0.058, -s * 0.72, s * 0.082, -s * 0.58, s * 0.095);
  ctx.bezierCurveTo(-s * 0.42, s * 0.105, -s * 0.25, s * 0.115, -s * 0.08, s * 0.12);
  ctx.bezierCurveTo(s * 0.10, s * 0.122, s * 0.30, s * 0.118, s * 0.48, s * 0.108);
  ctx.bezierCurveTo(s * 0.62, s * 0.098, s * 0.76, s * 0.080, s * 0.86, s * 0.058);
  ctx.bezierCurveTo(s * 0.92, s * 0.040, s * 0.94, s * 0.022, s * 0.96, 0);
  ctx.closePath();
  ctx.fill();

  // Fuselage outline
  ctx.strokeStyle = "rgba(255, 255, 255, 0.10)";
  ctx.lineWidth = 0.5;
  ctx.stroke();

  // ── Dorsal spine highlight (sun reflection along top) ──
  ctx.strokeStyle = "rgba(220, 228, 240, 0.18)";
  ctx.lineWidth = 1.2;
  ctx.beginPath();
  ctx.moveTo(s * 0.80, -s * 0.082);
  ctx.bezierCurveTo(s * 0.60, -s * 0.102, s * 0.30, -s * 0.115, 0, -s * 0.118);
  ctx.bezierCurveTo(-s * 0.25, -s * 0.114, -s * 0.45, -s * 0.098, -s * 0.60, -s * 0.078);
  ctx.stroke();

  // ── Top fuselage surface (elevated perspective) ──
  if (persp > 0) {
    const topA = alpha * persp * 0.25;
    const topGrad = ctx.createLinearGradient(s * 0.6, -s * 0.13, -s * 0.5, -s * 0.10);
    topGrad.addColorStop(0, `rgba(170, 180, 200, ${topA})`);
    topGrad.addColorStop(0.5, `rgba(130, 140, 160, ${topA * 0.6})`);
    topGrad.addColorStop(1, `rgba(90, 100, 120, ${topA * 0.3})`);
    ctx.fillStyle = topGrad;
    ctx.beginPath();
    ctx.moveTo(s * 0.78, -s * 0.088);
    ctx.bezierCurveTo(s * 0.55, -s * 0.108, s * 0.25, -s * 0.12, -s * 0.05, -s * 0.122);
    ctx.bezierCurveTo(-s * 0.30, -s * 0.116, -s * 0.48, -s * 0.098, -s * 0.60, -s * 0.080);
    ctx.bezierCurveTo(-s * 0.48, -s * 0.093, -s * 0.30, -s * 0.108, -s * 0.05, -s * 0.115);
    ctx.bezierCurveTo(s * 0.25, -s * 0.112, s * 0.55, -s * 0.100, s * 0.78, -s * 0.080);
    ctx.closePath();
    ctx.fill();
  }

  // ── Fuselage panel lines (many, for realism) ──
  ctx.strokeStyle = "rgba(0, 0, 0, 0.06)";
  ctx.lineWidth = 0.4;

  // Horizontal center-line seam
  ctx.beginPath();
  ctx.moveTo(s * 0.82, s * 0.005);
  ctx.lineTo(-s * 0.88, s * 0.005);
  ctx.stroke();

  // Upper fuselage panel line
  ctx.beginPath();
  ctx.moveTo(s * 0.70, -s * 0.065);
  ctx.bezierCurveTo(s * 0.40, -s * 0.088, s * 0.10, -s * 0.095, -s * 0.20, -s * 0.09);
  ctx.bezierCurveTo(-s * 0.42, -s * 0.082, -s * 0.58, -s * 0.068, -s * 0.72, -s * 0.048);
  ctx.stroke();

  // Lower fuselage panel line
  ctx.beginPath();
  ctx.moveTo(s * 0.70, s * 0.065);
  ctx.bezierCurveTo(s * 0.40, s * 0.088, s * 0.10, s * 0.095, -s * 0.20, s * 0.09);
  ctx.bezierCurveTo(-s * 0.42, s * 0.082, -s * 0.58, s * 0.068, -s * 0.72, s * 0.048);
  ctx.stroke();

  // Vertical panel seams (maintenance access panels)
  for (const px of [0.55, 0.38, 0.20, 0.02, -0.18, -0.35, -0.52, -0.68]) {
    ctx.beginPath();
    ctx.moveTo(s * px, -s * 0.10);
    ctx.lineTo(s * px, s * 0.10);
    ctx.stroke();
  }

  // Wing root fairing panel line
  ctx.strokeStyle = "rgba(0, 0, 0, 0.05)";
  ctx.lineWidth = 0.5;
  ctx.beginPath();
  ctx.moveTo(s * 0.16, s * 0.105);
  ctx.bezierCurveTo(s * 0.05, s * 0.11, -s * 0.10, s * 0.11, -s * 0.22, s * 0.105);
  ctx.stroke();

  // ── Access panels (rectangular hatches) ──
  ctx.strokeStyle = "rgba(0, 0, 0, 0.05)";
  ctx.lineWidth = 0.3;

  // Forward equipment bay
  ctx.beginPath();
  ctx.roundRect(s * 0.58, -s * 0.06, s * 0.08, s * 0.04, 1);
  ctx.stroke();
  // Ammo bay
  ctx.beginPath();
  ctx.roundRect(s * 0.62, s * 0.025, s * 0.06, s * 0.035, 1);
  ctx.stroke();
  // Mid-fuselage avionics bay
  ctx.beginPath();
  ctx.roundRect(s * 0.02, -s * 0.075, s * 0.10, s * 0.04, 1);
  ctx.stroke();
  // Engine access panel
  ctx.beginPath();
  ctx.roundRect(-s * 0.40, -s * 0.065, s * 0.14, s * 0.05, 1);
  ctx.stroke();

  // ── Rivet lines (subtle dot rows) ──
  ctx.fillStyle = "rgba(0, 0, 0, 0.04)";
  // Upper rivet row
  for (let rx = -s * 0.65; rx < s * 0.75; rx += s * 0.028) {
    ctx.beginPath();
    ctx.arc(rx, -s * 0.078, 0.4, 0, Math.PI * 2);
    ctx.fill();
  }
  // Lower rivet row
  for (let rx = -s * 0.65; rx < s * 0.75; rx += s * 0.028) {
    ctx.beginPath();
    ctx.arc(rx, s * 0.078, 0.4, 0, Math.PI * 2);
    ctx.fill();
  }
  // Center rivet row
  for (let rx = -s * 0.80; rx < s * 0.82; rx += s * 0.035) {
    ctx.beginPath();
    ctx.arc(rx, s * 0.008, 0.35, 0, Math.PI * 2);
    ctx.fill();
  }

  // ── Belly highlight (reflected light from ground/atmosphere) ──
  ctx.strokeStyle = "rgba(120, 130, 150, 0.08)";
  ctx.lineWidth = 2;
  ctx.beginPath();
  ctx.moveTo(s * 0.55, s * 0.105);
  ctx.bezierCurveTo(s * 0.25, s * 0.12, -s * 0.10, s * 0.12, -s * 0.40, s * 0.095);
  ctx.stroke();

  // ── Exhaust staining (dark streak behind nozzle) ──
  const stainGrad = ctx.createLinearGradient(-s * 0.93, 0, -s * 0.70, 0);
  stainGrad.addColorStop(0, "rgba(20, 22, 28, 0.12)");
  stainGrad.addColorStop(0.4, "rgba(25, 28, 35, 0.06)");
  stainGrad.addColorStop(1, "rgba(30, 32, 40, 0)");
  ctx.fillStyle = stainGrad;
  ctx.beginPath();
  ctx.moveTo(-s * 0.93, -s * 0.015);
  ctx.lineTo(-s * 0.70, -s * 0.025);
  ctx.lineTo(-s * 0.70, s * 0.04);
  ctx.lineTo(-s * 0.93, s * 0.035);
  ctx.closePath();
  ctx.fill();

  // ── Wing (airfoil cross-section, edge-on view) ──
  const wingGrad = ctx.createLinearGradient(0, s * 0.10, 0, s * 0.155);
  wingGrad.addColorStop(0, "rgba(70, 78, 94, 0.92)");
  wingGrad.addColorStop(0.3, "rgba(52, 60, 76, 0.90)");
  wingGrad.addColorStop(0.7, "rgba(42, 50, 66, 0.88)");
  wingGrad.addColorStop(1, "rgba(35, 42, 58, 0.82)");
  ctx.fillStyle = wingGrad;
  ctx.beginPath();
  ctx.moveTo(s * 0.16, s * 0.105);
  ctx.bezierCurveTo(s * 0.14, s * 0.12, s * 0.10, s * 0.14, s * 0.04, s * 0.148);
  ctx.bezierCurveTo(-s * 0.02, s * 0.152, -s * 0.10, s * 0.152, -s * 0.16, s * 0.148);
  ctx.bezierCurveTo(-s * 0.20, s * 0.14, -s * 0.23, s * 0.128, -s * 0.26, s * 0.105);
  ctx.closePath();
  ctx.fill();
  ctx.strokeStyle = "rgba(255, 255, 255, 0.07)";
  ctx.lineWidth = 0.4;
  ctx.stroke();

  // Wing leading-edge highlight
  ctx.strokeStyle = "rgba(200, 210, 225, 0.14)";
  ctx.lineWidth = 0.7;
  ctx.beginPath();
  ctx.moveTo(s * 0.16, s * 0.105);
  ctx.bezierCurveTo(s * 0.14, s * 0.12, s * 0.10, s * 0.14, s * 0.04, s * 0.148);
  ctx.stroke();

  // Wing trailing-edge (thin, dark)
  ctx.strokeStyle = "rgba(0, 0, 0, 0.08)";
  ctx.lineWidth = 0.3;
  ctx.beginPath();
  ctx.moveTo(-s * 0.16, s * 0.148);
  ctx.bezierCurveTo(-s * 0.20, s * 0.14, -s * 0.23, s * 0.128, -s * 0.26, s * 0.105);
  ctx.stroke();

  // Flap/aileron hinge line
  ctx.strokeStyle = "rgba(0, 0, 0, 0.06)";
  ctx.lineWidth = 0.4;
  ctx.beginPath();
  ctx.moveTo(-s * 0.12, s * 0.148);
  ctx.lineTo(-s * 0.24, s * 0.108);
  ctx.stroke();

  // Flap track fairing (small bump)
  ctx.fillStyle = "rgba(50, 58, 72, 0.6)";
  ctx.beginPath();
  ctx.ellipse(-s * 0.15, s * 0.135, s * 0.012, s * 0.005, 0.1, 0, Math.PI * 2);
  ctx.fill();

  // ── Tip tank ──
  const tankGrad = ctx.createLinearGradient(-s * 0.05, s * 0.155, -s * 0.05, s * 0.195);
  tankGrad.addColorStop(0, "rgba(72, 80, 96, 0.88)");
  tankGrad.addColorStop(0.4, "rgba(55, 62, 78, 0.85)");
  tankGrad.addColorStop(1, "rgba(40, 48, 62, 0.78)");
  ctx.fillStyle = tankGrad;
  ctx.beginPath();
  ctx.ellipse(-s * 0.04, s * 0.175, s * 0.13, s * 0.028, 0, 0, Math.PI * 2);
  ctx.fill();
  ctx.strokeStyle = "rgba(255, 255, 255, 0.06)";
  ctx.lineWidth = 0.3;
  ctx.stroke();

  // Tip tank center seam
  ctx.strokeStyle = "rgba(0, 0, 0, 0.05)";
  ctx.lineWidth = 0.3;
  ctx.beginPath();
  ctx.moveTo(-s * 0.17, s * 0.175);
  ctx.lineTo(s * 0.09, s * 0.175);
  ctx.stroke();

  // Tip tank nose highlight
  ctx.fillStyle = "rgba(180, 190, 210, 0.10)";
  ctx.beginPath();
  ctx.ellipse(s * 0.07, s * 0.172, s * 0.02, s * 0.012, 0, 0, Math.PI * 2);
  ctx.fill();

  // Tip tank pylon
  ctx.fillStyle = "rgba(50, 58, 72, 0.7)";
  ctx.fillRect(-s * 0.055, s * 0.148, s * 0.018, s * 0.028);

  // ── Bubble canopy (highly detailed) ──
  // Canopy base (darker tint)
  const canopyBaseGrad = ctx.createLinearGradient(s * 0.32, -s * 0.26, s * 0.32, -s * 0.095);
  canopyBaseGrad.addColorStop(0, "rgba(120, 165, 235, 0.5)");
  canopyBaseGrad.addColorStop(0.2, "rgba(100, 150, 225, 0.42)");
  canopyBaseGrad.addColorStop(0.5, "rgba(80, 130, 210, 0.30)");
  canopyBaseGrad.addColorStop(0.75, "rgba(65, 110, 195, 0.18)");
  canopyBaseGrad.addColorStop(1, "rgba(50, 90, 180, 0.08)");
  ctx.fillStyle = canopyBaseGrad;
  ctx.beginPath();
  // Windscreen front starts at fuselage
  ctx.moveTo(s * 0.56, -s * 0.098);
  ctx.bezierCurveTo(s * 0.52, -s * 0.14, s * 0.48, -s * 0.18, s * 0.44, -s * 0.21);
  // Top of canopy bubble
  ctx.bezierCurveTo(s * 0.40, -s * 0.235, s * 0.35, -s * 0.25, s * 0.30, -s * 0.255);
  ctx.bezierCurveTo(s * 0.25, -s * 0.252, s * 0.20, -s * 0.245, s * 0.16, -s * 0.23);
  // Rear of canopy (fairing back into fuselage)
  ctx.bezierCurveTo(s * 0.12, -s * 0.21, s * 0.09, -s * 0.17, s * 0.07, -s * 0.12);
  ctx.lineTo(s * 0.56, -s * 0.098);
  ctx.closePath();
  ctx.fill();

  // Canopy outline
  ctx.strokeStyle = "rgba(180, 210, 255, 0.32)";
  ctx.lineWidth = 0.6;
  ctx.stroke();

  // Windscreen (front section — flat armored glass)
  ctx.strokeStyle = "rgba(140, 175, 230, 0.20)";
  ctx.lineWidth = 0.7;
  ctx.beginPath();
  ctx.moveTo(s * 0.50, -s * 0.098);
  ctx.bezierCurveTo(s * 0.48, -s * 0.15, s * 0.45, -s * 0.19, s * 0.42, -s * 0.215);
  ctx.stroke();

  // Canopy frame lines (structural bow frames)
  ctx.strokeStyle = "rgba(100, 130, 180, 0.14)";
  ctx.lineWidth = 0.5;
  // Front frame (windscreen to bubble joint)
  ctx.beginPath();
  ctx.moveTo(s * 0.44, -s * 0.098);
  ctx.bezierCurveTo(s * 0.43, -s * 0.16, s * 0.41, -s * 0.20, s * 0.39, -s * 0.225);
  ctx.stroke();
  // Mid frame
  ctx.beginPath();
  ctx.moveTo(s * 0.32, -s * 0.098);
  ctx.bezierCurveTo(s * 0.31, -s * 0.17, s * 0.30, -s * 0.22, s * 0.28, -s * 0.248);
  ctx.stroke();
  // Rear frame
  ctx.beginPath();
  ctx.moveTo(s * 0.20, -s * 0.098);
  ctx.bezierCurveTo(s * 0.19, -s * 0.16, s * 0.17, -s * 0.21, s * 0.16, -s * 0.235);
  ctx.stroke();
  // Aft frame
  ctx.beginPath();
  ctx.moveTo(s * 0.12, -s * 0.098);
  ctx.bezierCurveTo(s * 0.11, -s * 0.14, s * 0.10, -s * 0.17, s * 0.09, -s * 0.19);
  ctx.stroke();

  // Canopy primary glint (broad specular)
  ctx.fillStyle = "rgba(255, 255, 255, 0.20)";
  ctx.beginPath();
  ctx.ellipse(s * 0.38, -s * 0.22, s * 0.055, s * 0.014, -0.2, 0, Math.PI * 2);
  ctx.fill();

  // Secondary glint (smaller, offset)
  ctx.fillStyle = "rgba(255, 255, 255, 0.10)";
  ctx.beginPath();
  ctx.ellipse(s * 0.24, -s * 0.24, s * 0.035, s * 0.008, -0.15, 0, Math.PI * 2);
  ctx.fill();

  // Sky reflection on lower canopy
  ctx.fillStyle = "rgba(80, 120, 200, 0.06)";
  ctx.beginPath();
  ctx.ellipse(s * 0.30, -s * 0.12, s * 0.10, s * 0.015, 0, 0, Math.PI * 2);
  ctx.fill();

  // ── Canopy rear fairing (turtledeck) ──
  const deckGrad = ctx.createLinearGradient(s * 0.07, -s * 0.14, s * 0.07, -s * 0.10);
  deckGrad.addColorStop(0, "rgba(85, 92, 110, 0.6)");
  deckGrad.addColorStop(1, "rgba(65, 72, 88, 0.4)");
  ctx.fillStyle = deckGrad;
  ctx.beginPath();
  ctx.moveTo(s * 0.09, -s * 0.12);
  ctx.bezierCurveTo(s * 0.04, -s * 0.13, -s * 0.02, -s * 0.12, -s * 0.06, -s * 0.115);
  ctx.lineTo(-s * 0.06, -s * 0.10);
  ctx.lineTo(s * 0.09, -s * 0.10);
  ctx.closePath();
  ctx.fill();

  // ── Nose intake (split-lip centrifugal type, detailed) ──
  // Intake barrel (dark interior)
  ctx.fillStyle = "rgba(6, 8, 14, 0.75)";
  ctx.beginPath();
  ctx.ellipse(s * 0.94, s * 0.002, s * 0.018, s * 0.042, 0, 0, Math.PI * 2);
  ctx.fill();

  // Intake lip (bright metal edge)
  ctx.strokeStyle = "rgba(170, 178, 195, 0.22)";
  ctx.lineWidth = 0.6;
  ctx.beginPath();
  ctx.arc(s * 0.94, s * 0.002, s * 0.042, -Math.PI * 0.55, Math.PI * 0.55);
  ctx.stroke();

  // Intake splitter plate (internal divider)
  ctx.fillStyle = "rgba(40, 45, 55, 0.5)";
  ctx.fillRect(s * 0.90, -s * 0.002, s * 0.04, s * 0.004);

  // Intake inner highlight (reflecting sky)
  ctx.fillStyle = "rgba(60, 80, 120, 0.08)";
  ctx.beginPath();
  ctx.ellipse(s * 0.92, -s * 0.01, s * 0.012, s * 0.02, 0, -Math.PI * 0.5, Math.PI * 0.2);
  ctx.fill();

  // ── Pitot tube (extending from nose) ──
  ctx.strokeStyle = "rgba(140, 148, 165, 0.35)";
  ctx.lineWidth = 0.6;
  ctx.beginPath();
  ctx.moveTo(s * 0.96, -s * 0.068);
  ctx.lineTo(s * 1.04, -s * 0.068);
  ctx.stroke();
  // Pitot tube tip
  ctx.fillStyle = "rgba(160, 168, 185, 0.3)";
  ctx.beginPath();
  ctx.arc(s * 1.04, -s * 0.068, s * 0.004, 0, Math.PI * 2);
  ctx.fill();

  // ── Gun ports (6x .50 cal, P-80 nose) ──
  ctx.fillStyle = "rgba(10, 12, 20, 0.25)";
  for (let i = 0; i < 3; i++) {
    const gy = s * 0.02 + i * s * 0.018;
    ctx.beginPath();
    ctx.arc(s * 0.90, gy, s * 0.004, 0, Math.PI * 2);
    ctx.fill();
  }
  for (let i = 0; i < 3; i++) {
    const gy = -s * 0.05 + i * s * 0.018;
    ctx.beginPath();
    ctx.arc(s * 0.88, gy, s * 0.004, 0, Math.PI * 2);
    ctx.fill();
  }

  // ── USAF star-and-bars marking (fuselage side) ──
  const markX = s * 0.05, markY = 0;
  const markR = s * 0.04;

  // White circle
  ctx.strokeStyle = "rgba(255, 255, 255, 0.12)";
  ctx.lineWidth = 0.6;
  ctx.beginPath();
  ctx.arc(markX, markY, markR, 0, Math.PI * 2);
  ctx.stroke();

  // Star (5-pointed)
  ctx.fillStyle = "rgba(255, 255, 255, 0.08)";
  ctx.beginPath();
  for (let i = 0; i < 5; i++) {
    const a = -Math.PI / 2 + (i * 2 * Math.PI) / 5;
    const px = markX + Math.cos(a) * markR * 0.65;
    const py = markY + Math.sin(a) * markR * 0.65;
    if (i === 0) ctx.moveTo(px, py);
    else ctx.lineTo(px, py);
    const ia = a + Math.PI / 5;
    const ipx = markX + Math.cos(ia) * markR * 0.25;
    const ipy = markY + Math.sin(ia) * markR * 0.25;
    ctx.lineTo(ipx, ipy);
  }
  ctx.closePath();
  ctx.fill();

  // Bars
  ctx.fillStyle = "rgba(255, 255, 255, 0.08)";
  ctx.fillRect(markX - markR * 2.0, markY - markR * 0.28, markR * 1.0, markR * 0.56);
  ctx.fillRect(markX + markR * 1.0, markY - markR * 0.28, markR * 1.0, markR * 0.56);

  // ── Serial number (tail) ──
  ctx.fillStyle = "rgba(255, 255, 255, 0.06)";
  ctx.font = `${Math.max(4, s * 0.028)}px monospace`;
  ctx.textAlign = "center";
  ctx.textBaseline = "middle";
  ctx.fillText("FT-675", -s * 0.60, s * 0.005);

  // ── Exhaust nozzle (detailed) ──
  // Outer ring
  ctx.strokeStyle = "rgba(80, 85, 100, 0.25)";
  ctx.lineWidth = 0.8;
  ctx.beginPath();
  ctx.ellipse(-s * 0.925, s * 0.012, s * 0.016, s * 0.028, 0, 0, Math.PI * 2);
  ctx.stroke();
  // Inner dark
  ctx.fillStyle = "rgba(5, 6, 12, 0.65)";
  ctx.beginPath();
  ctx.ellipse(-s * 0.925, s * 0.012, s * 0.012, s * 0.022, 0, 0, Math.PI * 2);
  ctx.fill();
  // Hot core glow
  ctx.fillStyle = "rgba(255, 120, 40, 0.15)";
  ctx.beginPath();
  ctx.ellipse(-s * 0.925, s * 0.012, s * 0.006, s * 0.010, 0, 0, Math.PI * 2);
  ctx.fill();

  // ── Engine exhaust glow + heat shimmer ──
  const glowGrad = ctx.createRadialGradient(
    -s * 0.98, s * 0.012, 0,
    -s * 0.98, s * 0.012, s * 0.08,
  );
  glowGrad.addColorStop(0, "rgba(255, 170, 70, 0.28)");
  glowGrad.addColorStop(0.25, "rgba(255, 120, 40, 0.15)");
  glowGrad.addColorStop(0.5, "rgba(255, 80, 20, 0.06)");
  glowGrad.addColorStop(1, "rgba(255, 60, 10, 0)");
  ctx.fillStyle = glowGrad;
  ctx.beginPath();
  ctx.arc(-s * 0.98, s * 0.012, s * 0.08, 0, Math.PI * 2);
  ctx.fill();

  // Exhaust plume (longer cone)
  const plumeGrad = ctx.createLinearGradient(-s * 0.93, 0, -s * 1.15, 0);
  plumeGrad.addColorStop(0, "rgba(255, 140, 50, 0.12)");
  plumeGrad.addColorStop(0.3, "rgba(255, 100, 30, 0.05)");
  plumeGrad.addColorStop(1, "rgba(255, 80, 20, 0)");
  ctx.fillStyle = plumeGrad;
  ctx.beginPath();
  ctx.moveTo(-s * 0.93, -s * 0.005);
  ctx.lineTo(-s * 1.15, s * 0.012);
  ctx.lineTo(-s * 0.93, s * 0.030);
  ctx.closePath();
  ctx.fill();

  // ── Navigation lights ──
  // Red (port/left wingtip)
  ctx.fillStyle = "rgba(255, 25, 15, 0.65)";
  ctx.beginPath();
  ctx.arc(-s * 0.20, s * 0.148, s * 0.008, 0, Math.PI * 2);
  ctx.fill();
  // Red glow
  const navRedGlow = ctx.createRadialGradient(
    -s * 0.20, s * 0.148, 0,
    -s * 0.20, s * 0.148, s * 0.03,
  );
  navRedGlow.addColorStop(0, "rgba(255, 25, 15, 0.18)");
  navRedGlow.addColorStop(1, "rgba(255, 25, 15, 0)");
  ctx.fillStyle = navRedGlow;
  ctx.beginPath();
  ctx.arc(-s * 0.20, s * 0.148, s * 0.03, 0, Math.PI * 2);
  ctx.fill();

  // Tail anti-collision (white strobe)
  ctx.fillStyle = "rgba(255, 255, 255, 0.3)";
  ctx.beginPath();
  ctx.arc(-s * 0.92, -s * 0.005, s * 0.005, 0, Math.PI * 2);
  ctx.fill();

  // ── Antenna mast (dorsal spine) ──
  ctx.strokeStyle = "rgba(120, 128, 145, 0.28)";
  ctx.lineWidth = 0.6;
  ctx.beginPath();
  ctx.moveTo(-s * 0.04, -s * 0.12);
  ctx.lineTo(-s * 0.05, -s * 0.16);
  ctx.stroke();
  // Antenna wire (thin, to tail)
  ctx.strokeStyle = "rgba(120, 128, 145, 0.10)";
  ctx.lineWidth = 0.3;
  ctx.beginPath();
  ctx.moveTo(-s * 0.05, -s * 0.16);
  ctx.lineTo(-s * 0.50, -s * 0.10);
  ctx.stroke();

  // ── Landing gear ──
  if (opts.gearDown) {
    // Nose gear strut
    const noseGearX = s * 0.64;
    const noseGearTopY = s * 0.08;
    const noseGearBottomY = s * 0.22;
    ctx.strokeStyle = "rgba(120, 130, 150, 0.55)";
    ctx.lineWidth = Math.max(1, s * 0.008);
    ctx.beginPath();
    ctx.moveTo(noseGearX, noseGearTopY);
    ctx.lineTo(noseGearX, noseGearBottomY);
    ctx.stroke();
    // Nose wheel
    ctx.fillStyle = "rgba(30, 32, 40, 0.8)";
    ctx.beginPath();
    ctx.ellipse(noseGearX, noseGearBottomY, s * 0.018, s * 0.012, 0, 0, Math.PI * 2);
    ctx.fill();
    ctx.strokeStyle = "rgba(80, 85, 100, 0.3)";
    ctx.lineWidth = 0.5;
    ctx.stroke();
    // Nose gear door (open, hanging)
    ctx.fillStyle = "rgba(55, 62, 78, 0.5)";
    ctx.beginPath();
    ctx.moveTo(s * 0.60, s * 0.08);
    ctx.lineTo(s * 0.60, s * 0.12);
    ctx.lineTo(s * 0.62, s * 0.12);
    ctx.lineTo(s * 0.62, s * 0.08);
    ctx.closePath();
    ctx.fill();

    // Main gear strut
    const mainGearX = -s * 0.02;
    const mainGearTopY = s * 0.115;
    const mainGearBottomY = s * 0.24;
    ctx.strokeStyle = "rgba(120, 130, 150, 0.55)";
    ctx.lineWidth = Math.max(1.2, s * 0.010);
    ctx.beginPath();
    ctx.moveTo(mainGearX, mainGearTopY);
    ctx.lineTo(mainGearX, mainGearBottomY);
    ctx.stroke();
    // Oleo strut detail (thicker lower portion)
    ctx.strokeStyle = "rgba(100, 110, 130, 0.4)";
    ctx.lineWidth = Math.max(1.8, s * 0.014);
    ctx.beginPath();
    ctx.moveTo(mainGearX, mainGearBottomY - s * 0.04);
    ctx.lineTo(mainGearX, mainGearBottomY);
    ctx.stroke();
    // Main wheel (larger)
    ctx.fillStyle = "rgba(30, 32, 40, 0.85)";
    ctx.beginPath();
    ctx.ellipse(mainGearX, mainGearBottomY, s * 0.025, s * 0.016, 0, 0, Math.PI * 2);
    ctx.fill();
    ctx.strokeStyle = "rgba(80, 85, 100, 0.3)";
    ctx.lineWidth = 0.5;
    ctx.stroke();
    // Tire highlight
    ctx.fillStyle = "rgba(60, 65, 80, 0.3)";
    ctx.beginPath();
    ctx.ellipse(mainGearX, mainGearBottomY - s * 0.004, s * 0.015, s * 0.006, 0, 0, Math.PI * 2);
    ctx.fill();
    // Main gear door (open, hanging)
    ctx.fillStyle = "rgba(55, 62, 78, 0.5)";
    ctx.beginPath();
    ctx.moveTo(s * 0.05, s * 0.115);
    ctx.lineTo(s * 0.05, s * 0.16);
    ctx.lineTo(s * 0.03, s * 0.16);
    ctx.lineTo(s * 0.03, s * 0.115);
    ctx.closePath();
    ctx.fill();
  } else {
    // Landing gear doors (closed, visible as panel lines)
    ctx.strokeStyle = "rgba(0, 0, 0, 0.06)";
    ctx.lineWidth = 0.4;
    // Main gear door
    ctx.beginPath();
    ctx.moveTo(s * 0.05, s * 0.115);
    ctx.lineTo(s * 0.05, s * 0.095);
    ctx.lineTo(-s * 0.10, s * 0.095);
    ctx.lineTo(-s * 0.10, s * 0.115);
    ctx.stroke();
    // Nose gear door
    ctx.beginPath();
    ctx.moveTo(s * 0.68, s * 0.05);
    ctx.lineTo(s * 0.68, s * 0.08);
    ctx.lineTo(s * 0.60, s * 0.08);
    ctx.lineTo(s * 0.60, s * 0.05);
    ctx.stroke();
  }

  ctx.globalAlpha = 1;
  ctx.restore();
}
