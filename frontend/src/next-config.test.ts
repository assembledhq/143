import type { NextConfig } from "next";
import { describe, expect, it, vi } from "vitest";

// Importing the real MDX plugin starts asynchronous .source generation, which
// test-worker teardown can interrupt after truncating the generated files.
vi.mock("fumadocs-mdx/next", () => ({
  createMDX: () => (config: NextConfig) => config,
}));

describe("next config", () => {
  it("exposes PREVIEW_ORIGIN_TEMPLATE to the client build", async () => {
    const { default: nextConfig } = await import("../next.config.mjs");

    expect(nextConfig.env?.NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE).toBeDefined();
    expect(nextConfig.env?.NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE).toContain("{id}");
  });
});
