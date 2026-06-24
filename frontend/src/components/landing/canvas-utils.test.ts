import { describe, expect, it, vi } from "vitest";

import {
  CLOUDS,
  NOISE_BLIPS,
  STARS,
  cockpitFrame,
  drawCRTGrain,
  drawHudPitchLadder,
  drawVignette,
  hudText,
  pseudoRandom,
} from "./canvas-utils";

function makeCanvasContext() {
  const gradient = {
    addColorStop: vi.fn(),
  };
  const ctx = {
    fillStyle: "",
    strokeStyle: "",
    font: "",
    textAlign: "left" as CanvasTextAlign,
    textBaseline: "alphabetic" as CanvasTextBaseline,
    lineWidth: 0,
    beginPath: vi.fn(),
    moveTo: vi.fn(),
    lineTo: vi.fn(),
    quadraticCurveTo: vi.fn(),
    closePath: vi.fn(),
    fill: vi.fn(),
    stroke: vi.fn(),
    arc: vi.fn(),
    fillRect: vi.fn(),
    strokeRect: vi.fn(),
    fillText: vi.fn(),
    createRadialGradient: vi.fn(() => gradient),
  };
  return {
    ctx: ctx as unknown as CanvasRenderingContext2D,
    spies: ctx,
    gradient,
  };
}

describe("canvas landing utilities", () => {
  it("generates stable pseudo-random values in the expected range", () => {
    const first = pseudoRandom(42);

    expect(first).toBeGreaterThanOrEqual(0);
    expect(first).toBeLessThan(1);
    expect(pseudoRandom(42)).toBe(first);
  });

  it("draws HUD text with the requested alpha, size, and alignment", () => {
    const { ctx, spies } = makeCanvasContext();

    hudText(ctx, 10, 20, "ALT", 0.5, 18, "center");

    expect(spies.fillStyle).toBe("rgba(0, 255, 100, 0.5)");
    expect(spies.font).toBe("bold 18px monospace");
    expect(spies.textAlign).toBe("center");
    expect(spies.textBaseline).toBe("middle");
    expect(spies.fillText).toHaveBeenCalledWith("ALT", 10, 20);
  });

  it("draws a vignette gradient over the canvas", () => {
    const { ctx, spies, gradient } = makeCanvasContext();

    drawVignette(ctx, 200, 100, 0.25);

    expect(spies.createRadialGradient).toHaveBeenCalled();
    expect(gradient.addColorStop).toHaveBeenCalledWith(0, "rgba(0,0,0,0)");
    expect(gradient.addColorStop).toHaveBeenCalledWith(1, "rgba(0,0,0,0.25)");
    expect(spies.fillRect).toHaveBeenCalledWith(0, 0, 200, 100);
  });

  it("draws CRT scanlines, noise, and tint", () => {
    const { ctx, spies } = makeCanvasContext();

    drawCRTGrain(ctx, 90, 9, 100, 0.05);

    expect(spies.fillRect).toHaveBeenCalledWith(0, 0, 90, 1);
    expect(spies.fillRect).toHaveBeenCalledWith(0, 3, 90, 1);
    expect(spies.fillRect).toHaveBeenCalledWith(0, 6, 90, 1);
    expect(spies.fillRect).toHaveBeenCalledWith(0, 0, 90, 9);
    expect(spies.fillRect).toHaveBeenCalledTimes(44);
  });

  it("draws the pitch ladder without a center rung", () => {
    const { ctx, spies } = makeCanvasContext();

    drawHudPitchLadder(ctx, 300, 200, 0.8);

    expect(spies.fillText).toHaveBeenCalledTimes(6);
    expect(spies.fillText).toHaveBeenCalledWith("15", expect.any(Number), expect.any(Number));
    expect(spies.fillText).not.toHaveBeenCalledWith("0", expect.any(Number), expect.any(Number));
  });

  it("draws the cockpit frame with panels, instruments, and struts", () => {
    const { ctx, spies } = makeCanvasContext();

    cockpitFrame(ctx, 400, 300);

    expect(spies.quadraticCurveTo).toHaveBeenCalledWith(200, 226.5, 400, 234);
    expect(spies.strokeRect).toHaveBeenCalledTimes(2);
    expect(spies.arc).toHaveBeenCalled();
    expect(spies.lineTo).toHaveBeenCalledWith(40, 0);
    expect(spies.lineTo).toHaveBeenCalledWith(360, 0);
  });

  it("exports stable seeded decoration data", () => {
    expect(STARS).toHaveLength(80);
    expect(CLOUDS).toHaveLength(5);
    expect(NOISE_BLIPS).toHaveLength(8);
    expect(STARS[0]).toMatchObject({
      x: expect.any(Number),
      y: expect.any(Number),
      s: expect.any(Number),
    });
  });
});
