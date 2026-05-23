import { describe, expect, it } from "vitest";

describe("next config", () => {
  it("exposes PREVIEW_ORIGIN_TEMPLATE to the client build", async () => {
    const { default: nextConfig } = await import("../next.config.mjs");

    expect(nextConfig.env?.NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE).toBeDefined();
    expect(nextConfig.env?.NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE).toContain("{id}");
  });
});
