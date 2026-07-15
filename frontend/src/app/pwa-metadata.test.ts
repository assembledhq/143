import { describe, expect, it, vi } from "vitest";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { inflateSync } from "node:zlib";
import manifest from "./manifest";
import { metadata, viewport } from "./layout";

vi.mock("geist/font/sans", () => ({
  GeistSans: { variable: "font-sans" },
}));

vi.mock("geist/font/mono", () => ({
  GeistMono: { variable: "font-mono" },
}));

describe("PWA metadata", () => {
  it("exposes Chrome branding colors and icons", () => {
    const appManifest = manifest();
    const publicDir = join(process.cwd(), "public");

    expect(metadata.manifest).toBe("/manifest.webmanifest");
    expect(metadata.icons).toEqual({
      icon: [
        { url: "/favicon.ico", sizes: "any" },
        { url: "/icon-32.png", type: "image/png", sizes: "32x32" },
        { url: "/icon.svg", type: "image/svg+xml" },
      ],
      shortcut: [{ url: "/favicon.ico", sizes: "any" }],
      apple: [{ url: "/apple-icon.png", type: "image/png", sizes: "180x180" }],
    });
    expect(viewport.themeColor).toEqual([
      { media: "(prefers-color-scheme: light)", color: "#f6f5f0" },
      { media: "(prefers-color-scheme: dark)", color: "#151513" },
    ]);
    expect(appManifest).toMatchObject({
      name: "143",
      short_name: "143",
      theme_color: "#151513",
      background_color: "#f6f5f0",
      display: "standalone",
    });
    expect(appManifest.icons).toEqual([
      {
        src: "/icon-192.png",
        sizes: "192x192",
        type: "image/png",
        purpose: "any",
      },
      {
        src: "/icon-512.png",
        sizes: "512x512",
        type: "image/png",
        purpose: "any",
      },
      {
        src: "/icon.svg",
        sizes: "any",
        type: "image/svg+xml",
        purpose: "any",
      },
      {
        src: "/icon.svg",
        sizes: "any",
        type: "image/svg+xml",
        purpose: "maskable",
      },
    ]);
    expect(existsSync(join(publicDir, "favicon.ico"))).toBe(true);
    const favicon = readFileSync(join(publicDir, "favicon.ico"));
    expect(favicon.subarray(0, 4)).toEqual(Buffer.from([0, 0, 1, 0]));
    expect(favicon.readUInt16LE(4)).toBe(4);
    expect([0, 1, 2, 3].map((index) => [favicon[6 + index * 16] || 256, favicon[7 + index * 16] || 256])).toEqual([
      [16, 16],
      [32, 32],
      [48, 48],
      [256, 256],
    ]);
    expect(readFileSync(join(publicDir, "icon-32.png")).subarray(1, 4).toString("ascii")).toBe("PNG");
    expect(readFileSync(join(publicDir, "apple-icon.png")).subarray(1, 4).toString("ascii")).toBe("PNG");

    const iconSvg = readFileSync(join(process.cwd(), "src/app/icon.svg"), "utf8");
    expect(iconSvg).toContain('viewBox="150 60 700 780"');
    expect(iconSvg).toContain('id="p80-solid"');
    expect(iconSvg).toContain('id="body-gradient"');
    expect(iconSvg).toContain('id="glass-gradient"');
    expect(iconSvg).toContain("#abcbe6");
    expect(iconSvg).toContain("#699ec5");
    expect(iconSvg).not.toContain(">143<");
    expect(iconSvg).not.toContain("<circle");
    expect(iconSvg).not.toMatch(/<line[\s>]/);
    expect(iconSvg).not.toContain("<filter");
    expect(iconSvg).not.toContain("M 540 C 555");
    expect(iconSvg).not.toContain("M 460 C 445");
  });

  it("uses a transparent small-size optimized aircraft mark for the favicon", () => {
    const favicon = readFileSync(join(process.cwd(), "public/favicon.ico"));
    const largestIcon = largestIcoImage(favicon);
    const png = decodePng(largestIcon);

    let aircraftBluePixels = 0;
    let darkDetailPixels = 0;
    let opaquePixels = 0;
    let transparentPixels = 0;
    for (let offset = 0; offset < png.pixels.length; offset += 4) {
      const red = png.pixels[offset];
      const green = png.pixels[offset + 1];
      const blue = png.pixels[offset + 2];
      const alpha = png.pixels[offset + 3];

      if (alpha < 80) {
        transparentPixels += 1;
      }
      if (alpha >= 160) {
        opaquePixels += 1;
      }
      if (alpha >= 160 && red >= 80 && red <= 220 && green >= 120 && green <= 235 && blue >= 150 && blue <= 255 && blue > red + 15 && blue >= green + 5) {
        aircraftBluePixels += 1;
      }
      if (alpha >= 160 && red <= 35 && green <= 70 && blue <= 95 && blue > red) {
        darkDetailPixels += 1;
      }
    }

    expect(transparentPixels).toBeGreaterThan(45_000);
    expect(opaquePixels).toBeGreaterThan(9_000);
    expect(opaquePixels).toBeLessThan(18_000);
    expect(aircraftBluePixels).toBeGreaterThan(9_000);
    expect(darkDetailPixels).toBeGreaterThan(250);
    expect(darkDetailPixels).toBeLessThan(700);
  });

  it("keeps the embedded 16px favicon readable instead of sparse and pixelated", () => {
    const favicon = readFileSync(join(process.cwd(), "public/favicon.ico"));
    const png = decodePng(icoImageBySize(favicon, 16));

    let visibleAircraftPixels = 0;
    let strongAircraftPixels = 0;
    for (let offset = 0; offset < png.pixels.length; offset += 4) {
      const red = png.pixels[offset];
      const green = png.pixels[offset + 1];
      const blue = png.pixels[offset + 2];
      const alpha = png.pixels[offset + 3];

      if (alpha >= 96 && blue > red + 15 && blue >= green + 5) {
        visibleAircraftPixels += 1;
      }
      if (alpha >= 192 && blue > red + 15 && blue >= green + 5) {
        strongAircraftPixels += 1;
      }
    }

    expect(visibleAircraftPixels).toBeGreaterThanOrEqual(66);
    expect(strongAircraftPixels).toBeGreaterThanOrEqual(45);
  });
});

function largestIcoImage(favicon: Buffer): Buffer {
  expect(favicon.subarray(0, 4)).toEqual(Buffer.from([0, 0, 1, 0]));
  const count = favicon.readUInt16LE(4);
  let largest = { pixels: 0, offset: 0, size: 0 };

  for (let index = 0; index < count; index += 1) {
    const entryOffset = 6 + index * 16;
    const width = favicon[entryOffset] || 256;
    const height = favicon[entryOffset + 1] || 256;
    const size = favicon.readUInt32LE(entryOffset + 8);
    const offset = favicon.readUInt32LE(entryOffset + 12);
    const pixels = width * height;
    if (pixels > largest.pixels) {
      largest = { pixels, offset, size };
    }
  }

  return favicon.subarray(largest.offset, largest.offset + largest.size);
}

function icoImageBySize(favicon: Buffer, expectedSize: number): Buffer {
  expect(favicon.subarray(0, 4)).toEqual(Buffer.from([0, 0, 1, 0]));
  const count = favicon.readUInt16LE(4);

  for (let index = 0; index < count; index += 1) {
    const entryOffset = 6 + index * 16;
    const width = favicon[entryOffset] || 256;
    const height = favicon[entryOffset + 1] || 256;
    const size = favicon.readUInt32LE(entryOffset + 8);
    const offset = favicon.readUInt32LE(entryOffset + 12);

    if (width === expectedSize && height === expectedSize) {
      return favicon.subarray(offset, offset + size);
    }
  }

  throw new Error(`favicon.ico missing ${expectedSize}x${expectedSize} image`);
}

function decodePng(png: Buffer): { width: number; height: number; pixels: Buffer } {
  expect(png.subarray(0, 8)).toEqual(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]));

  let width = 0;
  let height = 0;
  let colorType = 0;
  const idatChunks: Buffer[] = [];
  let offset = 8;

  while (offset < png.length) {
    const length = png.readUInt32BE(offset);
    const type = png.subarray(offset + 4, offset + 8).toString("ascii");
    const data = png.subarray(offset + 8, offset + 8 + length);
    offset += 12 + length;

    if (type === "IHDR") {
      width = data.readUInt32BE(0);
      height = data.readUInt32BE(4);
      expect(data[8]).toBe(8);
      colorType = data[9];
      expect(data[12]).toBe(0);
    } else if (type === "IDAT") {
      idatChunks.push(data);
    } else if (type === "IEND") {
      break;
    }
  }

  const channels = colorType === 6 ? 4 : colorType === 2 ? 3 : 0;
  expect(channels).toBeGreaterThan(0);

  const rowBytes = width * channels;
  const inflated = inflateSync(Buffer.concat(idatChunks));
  const rows = Buffer.alloc(rowBytes * height);
  let inputOffset = 0;

  for (let y = 0; y < height; y += 1) {
    const filter = inflated[inputOffset];
    inputOffset += 1;
    const rowOffset = y * rowBytes;
    const previousRowOffset = rowOffset - rowBytes;

    for (let x = 0; x < rowBytes; x += 1) {
      const raw = inflated[inputOffset + x];
      const left = x >= channels ? rows[rowOffset + x - channels] : 0;
      const up = y > 0 ? rows[previousRowOffset + x] : 0;
      const upLeft = y > 0 && x >= channels ? rows[previousRowOffset + x - channels] : 0;

      rows[rowOffset + x] =
        (raw +
          (filter === 1
            ? left
            : filter === 2
              ? up
              : filter === 3
                ? Math.floor((left + up) / 2)
                : filter === 4
                  ? paeth(left, up, upLeft)
                  : 0)) &
        0xff;
    }
    inputOffset += rowBytes;
  }

  if (channels === 4) {
    return { width, height, pixels: rows };
  }

  const pixels = Buffer.alloc(width * height * 4);
  for (let source = 0, target = 0; source < rows.length; source += 3, target += 4) {
    pixels[target] = rows[source];
    pixels[target + 1] = rows[source + 1];
    pixels[target + 2] = rows[source + 2];
    pixels[target + 3] = 255;
  }
  return { width, height, pixels };
}

function paeth(left: number, up: number, upLeft: number): number {
  const estimate = left + up - upLeft;
  const leftDistance = Math.abs(estimate - left);
  const upDistance = Math.abs(estimate - up);
  const upLeftDistance = Math.abs(estimate - upLeft);

  if (leftDistance <= upDistance && leftDistance <= upLeftDistance) {
    return left;
  }
  if (upDistance <= upLeftDistance) {
    return up;
  }
  return upLeft;
}
