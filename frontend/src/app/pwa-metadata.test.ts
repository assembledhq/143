import { describe, expect, it, vi } from "vitest";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
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
      { media: "(prefers-color-scheme: light)", color: "#091f33" },
      { media: "(prefers-color-scheme: dark)", color: "#091f33" },
    ]);
    expect(appManifest).toMatchObject({
      name: "143",
      short_name: "143",
      theme_color: "#091f33",
      background_color: "#091f33",
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
    expect(readFileSync(join(publicDir, "favicon.ico")).subarray(0, 4)).toEqual(Buffer.from([0, 0, 1, 0]));
    expect(readFileSync(join(publicDir, "icon-32.png")).subarray(1, 4).toString("ascii")).toBe("PNG");
    expect(readFileSync(join(publicDir, "apple-icon.png")).subarray(1, 4).toString("ascii")).toBe("PNG");
  });
});
