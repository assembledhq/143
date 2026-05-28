import { describe, expect, it, vi } from "vitest";
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

    expect(metadata.manifest).toBe("/manifest.webmanifest");
    expect(metadata.icons).toEqual({
      icon: [{ url: "/icon.svg", type: "image/svg+xml" }],
      shortcut: [{ url: "/icon.svg", type: "image/svg+xml" }],
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
  });
});
