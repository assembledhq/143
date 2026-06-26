import { describe, expect, it } from 'vitest';

import { DARK, LIGHT, drawP80, drawP80Side } from './draw-p80';

type RecordedCall = {
  method: string;
  args: unknown[];
};

type GradientStop = {
  offset: number;
  color: string;
};

function createRecordingCanvasContext() {
  const calls: RecordedCall[] = [];
  const gradientStops: GradientStop[] = [];

  const record = (method: string) =>
    (...args: unknown[]) => {
      calls.push({ method, args });
    };

  const createGradient = (method: string) => (...args: unknown[]) => {
    calls.push({ method, args });
    return {
      addColorStop(offset: number, color: string) {
        gradientStops.push({ offset, color });
        calls.push({ method: 'addColorStop', args: [offset, color] });
      },
    };
  };

  const context = new Proxy(
    {},
    {
      get(_target, property) {
        if (property === 'createLinearGradient' || property === 'createRadialGradient') {
          return createGradient(String(property));
        }
        return record(String(property));
      },
      set(_target, property, value) {
        calls.push({ method: `set:${String(property)}`, args: [value] });
        return true;
      },
    },
  ) as CanvasRenderingContext2D;

  return { context, calls, gradientStops };
}

describe('drawP80', () => {
  it('draws top-down and side-profile P-80 variants across option branches', () => {
    const { context, calls, gradientStops } = createRecordingCanvasContext();

    drawP80(context, 120, 80, 48, Math.PI / 5, 0.6, 0.85, DARK);
    drawP80(context, 24, 32, 14, -Math.PI / 8, 0.9, 0.45, LIGHT);
    drawP80Side(context, 180, 110, 72, Math.PI, 0.9, 0.08, {
      gearDown: true,
      perspective: 0.7,
    });
    drawP80Side(context, 80, 55, 42, 0, 0.55, -0.04, {
      noShadow: true,
      perspective: 0,
    });

    const methodNames = calls.map((call) => call.method);

    expect(methodNames.filter((name) => name === 'save').length).toBeGreaterThanOrEqual(6);
    expect(methodNames).toContain('scale');
    expect(methodNames).toContain('roundRect');
    expect(methodNames).toContain('createLinearGradient');
    expect(methodNames).toContain('createRadialGradient');
    expect(methodNames).toContain('fillText');
    expect(
      calls.some((call) => call.method === 'set:fillStyle' && call.args[0] === 'rgba(30, 32, 40, 0.8)'),
    ).toBe(true);
    expect(gradientStops.length).toBeGreaterThan(20);
  });
});
