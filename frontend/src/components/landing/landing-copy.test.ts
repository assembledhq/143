import { describe, expect, it } from "vitest";
import * as landingCopy from "./landing-copy";
import { integrations, platformLayers } from "./landing-copy";

describe("landing copy", () => {
  it("keeps hero copy focused instead of exposing summary cards", () => {
    expect("heroMetrics" in landingCopy).toBe(false);
  });

  it("numbers platform layers after the why-this-matters section", () => {
    expect(platformLayers.map((layer) => `${layer.step} ${layer.title}`)).toEqual([
      "02 Team context",
      "03 Cloud execution",
      "04 Review control",
      "05 Cloud previews",
    ]);
  });

  it("uses the available integration logo assets", () => {
    expect(integrations.map((integration) => integration.logo)).toEqual([
      "/integrations/github.svg",
      "/integrations/linear.svg",
      "/integrations/slack.svg",
      "/integrations/sentry.svg",
      "/integrations/notion.svg",
      "/integrations/circleci.svg",
    ]);
  });

  it("keeps section headers simple and focused", () => {
    expect(platformLayers.map((layer) => layer.heading)).toEqual([
      "Shared context for every run.",
      "Run agents from anywhere.",
      "Review loops before human review.",
      "Preview every change in the cloud.",
    ]);
  });
});
