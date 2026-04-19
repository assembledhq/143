import nextConfig from "../next.config";
import { describe, expect, it } from "vitest";

describe("next config", () => {
  it("exposes PREVIEW_ORIGIN_TEMPLATE to the client build", () => {
    expect(nextConfig.env?.NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE).toBeDefined();
    expect(nextConfig.env?.NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE).toContain("{id}");
  });
});
