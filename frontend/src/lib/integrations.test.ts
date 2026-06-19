import { describe, expect, it } from "vitest";
import { INTEGRATIONS, getIntegrationByKey } from "./integrations";
import type { IntegrationKey } from "./integrations";

describe("INTEGRATIONS", () => {
  const expectedKeys = [
    "github",
    "sentry",
    "linear",
    "slack",
    "notion",
    "circleci",
    "mezmo",
  ] as const satisfies readonly IntegrationKey[];

  it("contains the expected integrations", () => {
    expect(INTEGRATIONS.map((integration) => integration.key)).toEqual(expectedKeys);
  });

  it("has unique keys", () => {
    const keys = INTEGRATIONS.map((i) => i.key);
    expect(new Set(keys).size).toBe(keys.length);
  });

  it("each integration has required fields", () => {
    for (const integration of INTEGRATIONS) {
      expect(integration.key).toBeTruthy();
      expect(integration.name).toBeTruthy();
      expect(integration.description).toBeTruthy();
      expect(integration.logoSrc).toMatch(/^\/integrations\//);
    }
  });
});

describe("getIntegrationByKey", () => {
  it.each([
    "github",
    "sentry",
    "linear",
    "slack",
    "notion",
    "circleci",
    "mezmo",
  ] as IntegrationKey[])(
    "returns the correct integration for %s",
    (key) => {
      const result = getIntegrationByKey(key);
      expect(result.key).toBe(key);
      expect(result.name).toBeTruthy();
    },
  );

  it("throws for an unknown key", () => {
    expect(() => getIntegrationByKey("unknown" as IntegrationKey)).toThrow(
      "missing integration definition for key: unknown",
    );
  });
});
