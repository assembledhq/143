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

export const LIGHT: PlaneTheme = {
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

// ── Draw P-80 side profile ──────────────────────────────────────────────────
//
// For scenes where the camera is external (watching the plane fly past).
// Nose points in the +X direction; use rotation to face any heading.
// Automatically corrects Y-flip when facing left (|rotation| > π/2).
//

/**
 * Draw a P-80 Shooting Star from the side.
 *
 * @param ctx       Canvas context
 * @param x         Center X
 * @param y         Center Y
 * @param size      Overall scale (fuselage length ≈ 2×size)
 * @param rotation  Heading in radians (nose direction)
 * @param alpha     Opacity (0–1). Default 1
 * @param noseDown  Pitch angle in radians (positive = nose down). Default 0
 */
export function drawP80Side(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  size: number,
  rotation: number,
  alpha = 1,
  noseDown = 0,
) {
  ctx.save();
  ctx.translate(x, y);

  // When the plane faces left (rotation ≈ π), ctx.rotate(π) flips the Y axis
  // so the tail fin / canopy end up pointing down. Fix: rotate then un-flip Y,
  // and negate noseDown since the Y sense reversed.
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

  // ── Fuselage ──
  // Torpedo shape: pointed nose, widest at mid-body, tapers at tail
  const bodyGrad = ctx.createLinearGradient(0, -s * 0.18, 0, s * 0.18);
  bodyGrad.addColorStop(0, "rgba(180, 190, 210, 0.85)");
  bodyGrad.addColorStop(0.3, "rgba(90, 100, 115, 0.95)");
  bodyGrad.addColorStop(0.55, "rgba(60, 68, 82, 0.92)");
  bodyGrad.addColorStop(0.8, "rgba(45, 52, 65, 0.88)");
  bodyGrad.addColorStop(1, "rgba(30, 38, 50, 0.7)");
  ctx.fillStyle = bodyGrad;

  ctx.beginPath();
  ctx.moveTo(s * 0.95, 0);
  ctx.quadraticCurveTo(s * 0.88, -s * 0.06, s * 0.72, -s * 0.095);
  ctx.quadraticCurveTo(s * 0.55, -s * 0.11, s * 0.3, -s * 0.115);
  ctx.lineTo(s * 0.1, -s * 0.12);
  ctx.lineTo(-s * 0.15, -s * 0.115);
  ctx.quadraticCurveTo(-s * 0.45, -s * 0.105, -s * 0.65, -s * 0.07);
  ctx.lineTo(-s * 0.85, -s * 0.04);
  ctx.lineTo(-s * 0.92, -s * 0.02);
  ctx.lineTo(-s * 0.92, s * 0.04);
  ctx.lineTo(-s * 0.85, s * 0.06);
  ctx.quadraticCurveTo(-s * 0.65, s * 0.09, -s * 0.4, s * 0.10);
  ctx.lineTo(-s * 0.1, s * 0.115);
  ctx.lineTo(s * 0.1, s * 0.12);
  ctx.quadraticCurveTo(s * 0.4, s * 0.115, s * 0.6, s * 0.10);
  ctx.quadraticCurveTo(s * 0.85, s * 0.07, s * 0.95, 0);
  ctx.closePath();
  ctx.fill();
  ctx.strokeStyle = "rgba(255, 255, 255, 0.12)";
  ctx.lineWidth = 0.5;
  ctx.stroke();

  // ── Fuselage panel lines ──
  ctx.strokeStyle = "rgba(255, 255, 255, 0.05)";
  ctx.lineWidth = 0.4;
  // Center panel line
  ctx.beginPath();
  ctx.moveTo(s * 0.80, s * 0.005);
  ctx.lineTo(-s * 0.85, s * 0.005);
  ctx.stroke();
  // Upper panel line
  ctx.beginPath();
  ctx.moveTo(s * 0.65, -s * 0.07);
  ctx.quadraticCurveTo(s * 0.2, -s * 0.09, -s * 0.4, -s * 0.08);
  ctx.lineTo(-s * 0.70, -s * 0.05);
  ctx.stroke();
  // Wing root fairing line
  ctx.beginPath();
  ctx.moveTo(s * 0.15, s * 0.10);
  ctx.lineTo(-s * 0.25, s * 0.10);
  ctx.stroke();

  // ── Fuselage belly highlight (reflected ground light) ──
  ctx.strokeStyle = "rgba(100, 110, 130, 0.08)";
  ctx.lineWidth = 1.5;
  ctx.beginPath();
  ctx.moveTo(s * 0.5, s * 0.11);
  ctx.quadraticCurveTo(s * 0.1, s * 0.125, -s * 0.3, s * 0.10);
  ctx.stroke();

  // ── Vertical tail fin (prominent in side view) ──
  const finGrad = ctx.createLinearGradient(-s * 0.50, -s * 0.10, -s * 0.80, -s * 0.38);
  finGrad.addColorStop(0, "rgba(65, 73, 88, 0.92)");
  finGrad.addColorStop(0.5, "rgba(55, 63, 78, 0.90)");
  finGrad.addColorStop(1, "rgba(45, 53, 68, 0.85)");
  ctx.fillStyle = finGrad;
  ctx.beginPath();
  ctx.moveTo(-s * 0.48, -s * 0.10);
  ctx.quadraticCurveTo(-s * 0.55, -s * 0.28, -s * 0.62, -s * 0.36);
  ctx.quadraticCurveTo(-s * 0.68, -s * 0.40, -s * 0.74, -s * 0.40);
  ctx.lineTo(-s * 0.86, -s * 0.32);
  ctx.quadraticCurveTo(-s * 0.90, -s * 0.22, -s * 0.88, -s * 0.04);
  ctx.closePath();
  ctx.fill();
  ctx.strokeStyle = "rgba(255, 255, 255, 0.08)";
  ctx.lineWidth = 0.4;
  ctx.stroke();

  // Fin leading edge highlight
  ctx.strokeStyle = "rgba(180, 190, 210, 0.18)";
  ctx.lineWidth = 0.7;
  ctx.beginPath();
  ctx.moveTo(-s * 0.48, -s * 0.10);
  ctx.quadraticCurveTo(-s * 0.55, -s * 0.28, -s * 0.62, -s * 0.36);
  ctx.stroke();

  // Fin panel line / hinge line
  ctx.strokeStyle = "rgba(255, 255, 255, 0.04)";
  ctx.lineWidth = 0.4;
  ctx.beginPath();
  ctx.moveTo(-s * 0.78, -s * 0.38);
  ctx.lineTo(-s * 0.88, -s * 0.10);
  ctx.stroke();

  // ── Horizontal stabilizer (seen edge-on, thin wedge) ──
  ctx.fillStyle = "rgba(50, 58, 72, 0.8)";
  ctx.beginPath();
  ctx.moveTo(-s * 0.58, -s * 0.02);
  ctx.lineTo(-s * 0.68, -s * 0.045);
  ctx.lineTo(-s * 0.88, -s * 0.025);
  ctx.lineTo(-s * 0.88, s * 0.01);
  ctx.lineTo(-s * 0.58, s * 0.02);
  ctx.closePath();
  ctx.fill();
  ctx.strokeStyle = "rgba(255, 255, 255, 0.05)";
  ctx.lineWidth = 0.3;
  ctx.stroke();

  // ── Wing (seen edge-on: airfoil cross-section visible) ──
  ctx.fillStyle = "rgba(46, 54, 68, 0.9)";
  ctx.beginPath();
  ctx.moveTo(s * 0.14, s * 0.10);
  ctx.quadraticCurveTo(s * 0.10, s * 0.13, s * 0.04, s * 0.14);
  ctx.lineTo(-s * 0.18, s * 0.14);
  ctx.quadraticCurveTo(-s * 0.22, s * 0.13, -s * 0.24, s * 0.10);
  ctx.closePath();
  ctx.fill();
  ctx.strokeStyle = "rgba(255, 255, 255, 0.06)";
  ctx.lineWidth = 0.3;
  ctx.stroke();

  // Wing leading-edge highlight
  ctx.strokeStyle = "rgba(180, 190, 210, 0.10)";
  ctx.lineWidth = 0.5;
  ctx.beginPath();
  ctx.moveTo(s * 0.14, s * 0.10);
  ctx.quadraticCurveTo(s * 0.10, s * 0.13, s * 0.04, s * 0.14);
  ctx.stroke();

  // ── Tip tank (visible hanging under wing) ──
  ctx.fillStyle = "rgba(50, 58, 72, 0.85)";
  ctx.beginPath();
  ctx.ellipse(-s * 0.05, s * 0.17, s * 0.12, s * 0.025, 0, 0, Math.PI * 2);
  ctx.fill();
  ctx.strokeStyle = "rgba(255, 255, 255, 0.06)";
  ctx.lineWidth = 0.3;
  ctx.stroke();
  // Tip tank pylon
  ctx.strokeStyle = "rgba(50, 58, 72, 0.6)";
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(-s * 0.05, s * 0.14);
  ctx.lineTo(-s * 0.05, s * 0.145);
  ctx.stroke();

  // ── Bubble canopy ──
  const canopyGrad = ctx.createLinearGradient(s * 0.30, -s * 0.24, s * 0.30, -s * 0.10);
  canopyGrad.addColorStop(0, "rgba(150, 195, 255, 0.55)");
  canopyGrad.addColorStop(0.35, "rgba(110, 160, 240, 0.4)");
  canopyGrad.addColorStop(0.7, "rgba(90, 140, 220, 0.25)");
  canopyGrad.addColorStop(1, "rgba(70, 110, 200, 0.10)");
  ctx.fillStyle = canopyGrad;
  ctx.beginPath();
  ctx.moveTo(s * 0.54, -s * 0.10);
  ctx.quadraticCurveTo(s * 0.48, -s * 0.18, s * 0.40, -s * 0.22);
  ctx.quadraticCurveTo(s * 0.32, -s * 0.24, s * 0.24, -s * 0.23);
  ctx.quadraticCurveTo(s * 0.16, -s * 0.215, s * 0.10, -s * 0.12);
  ctx.lineTo(s * 0.54, -s * 0.10);
  ctx.closePath();
  ctx.fill();
  ctx.strokeStyle = "rgba(180, 210, 255, 0.3)";
  ctx.lineWidth = 0.5;
  ctx.stroke();

  // Canopy frame lines
  ctx.strokeStyle = "rgba(120, 150, 200, 0.12)";
  ctx.lineWidth = 0.4;
  // Front frame
  ctx.beginPath();
  ctx.moveTo(s * 0.46, -s * 0.105);
  ctx.quadraticCurveTo(s * 0.44, -s * 0.17, s * 0.38, -s * 0.20);
  ctx.stroke();
  // Rear frame
  ctx.beginPath();
  ctx.moveTo(s * 0.22, -s * 0.115);
  ctx.quadraticCurveTo(s * 0.20, -s * 0.19, s * 0.19, -s * 0.215);
  ctx.stroke();

  // Canopy glint (specular highlight)
  ctx.fillStyle = "rgba(255, 255, 255, 0.22)";
  ctx.beginPath();
  ctx.ellipse(s * 0.40, -s * 0.20, s * 0.045, s * 0.012, -0.25, 0, Math.PI * 2);
  ctx.fill();
  // Secondary glint
  ctx.fillStyle = "rgba(255, 255, 255, 0.08)";
  ctx.beginPath();
  ctx.ellipse(s * 0.28, -s * 0.21, s * 0.03, s * 0.008, -0.15, 0, Math.PI * 2);
  ctx.fill();

  // ── Nose intake (split-lip style) ──
  ctx.fillStyle = "rgba(8, 10, 18, 0.7)";
  ctx.beginPath();
  ctx.ellipse(s * 0.93, 0, s * 0.015, s * 0.038, 0, 0, Math.PI * 2);
  ctx.fill();
  // Intake lip highlight
  ctx.strokeStyle = "rgba(150, 160, 180, 0.12)";
  ctx.lineWidth = 0.4;
  ctx.beginPath();
  ctx.arc(s * 0.93, 0, s * 0.038, -Math.PI * 0.5, Math.PI * 0.5);
  ctx.stroke();

  // ── Exhaust nozzle ──
  ctx.fillStyle = "rgba(8, 10, 18, 0.6)";
  ctx.beginPath();
  ctx.ellipse(-s * 0.91, s * 0.01, s * 0.014, s * 0.022, 0, 0, Math.PI * 2);
  ctx.fill();

  // ── Engine glow ──
  const glowGrad = ctx.createRadialGradient(
    -s * 0.96, s * 0.01, 0,
    -s * 0.96, s * 0.01, s * 0.06,
  );
  glowGrad.addColorStop(0, "rgba(255, 160, 60, 0.25)");
  glowGrad.addColorStop(0.4, "rgba(255, 100, 30, 0.10)");
  glowGrad.addColorStop(1, "rgba(255, 80, 20, 0)");
  ctx.fillStyle = glowGrad;
  ctx.beginPath();
  ctx.arc(-s * 0.96, s * 0.01, s * 0.06, 0, Math.PI * 2);
  ctx.fill();

  // ── Navigation light (red on port / left side when facing right) ──
  ctx.fillStyle = "rgba(255, 30, 20, 0.6)";
  ctx.beginPath();
  ctx.arc(-s * 0.18, s * 0.14, s * 0.008, 0, Math.PI * 2);
  ctx.fill();
  // Nav light glow
  const navGlow = ctx.createRadialGradient(
    -s * 0.18, s * 0.14, 0,
    -s * 0.18, s * 0.14, s * 0.025,
  );
  navGlow.addColorStop(0, "rgba(255, 30, 20, 0.15)");
  navGlow.addColorStop(1, "rgba(255, 30, 20, 0)");
  ctx.fillStyle = navGlow;
  ctx.beginPath();
  ctx.arc(-s * 0.18, s * 0.14, s * 0.025, 0, Math.PI * 2);
  ctx.fill();

  // ── Antenna mast (small, on fuselage spine) ──
  ctx.strokeStyle = "rgba(100, 110, 130, 0.25)";
  ctx.lineWidth = 0.5;
  ctx.beginPath();
  ctx.moveTo(-s * 0.05, -s * 0.115);
  ctx.lineTo(-s * 0.06, -s * 0.15);
  ctx.stroke();

  ctx.globalAlpha = 1;
  ctx.restore();
}
