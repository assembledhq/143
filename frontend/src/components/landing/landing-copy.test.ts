import { describe, expect, it } from "vitest";
import * as landingCopy from "./landing-copy";
import {
  codeReviewApproval,
  codeReviewControls,
  codeReviewSummary,
  codingAgents,
  integrations,
  platformLayers,
} from "./landing-copy";

describe("landing copy", () => {
  it("keeps hero copy focused instead of exposing summary cards", () => {
    expect("heroMetrics" in landingCopy).toBe(false);
  });

  it("builds the page around code review, agents, and previews", () => {
    expect(platformLayers.map((layer) => `${layer.step} ${layer.title}`)).toEqual([
      "02 Cloud agents",
      "03 Cloud previews",
    ]);
  });

  it("opens the homepage story with code review and its bottleneck", () => {
    expect(`${codeReviewSummary.step} ${codeReviewSummary.kicker}`).toBe(
      "01 Code review",
    );
    expect(codeReviewSummary.heading).toBe(
      "Code review that can auto-approve.",
    );
    expect(codeReviewSummary.body).toContain("Reviewing it is the bottleneck.");
  });

  it("focuses the review controls on policy tuning and reviewer model choice", () => {
    expect(codeReviewControls).toEqual([
      "Tune thresholds, sensitive paths, and required checks",
      "Raise the auto-approval rate by tightening policy",
      "Choose reviewer models: Codex, Claude Code, OpenCode",
      "Set reasoning depth per reviewer to control cost",
    ]);
  });

  it("keeps the landing copy free of em-dashes", () => {
    const flatten = (value: unknown): string =>
      typeof value === "string"
        ? value
        : value && typeof value === "object"
          ? Object.values(value).map(flatten).join(" ")
          : "";

    expect(flatten({ ...landingCopy })).not.toContain("—");
  });

  it("keeps the auto-approval evidence concrete", () => {
    expect(codeReviewApproval.decision).toBe("Approved");
    expect(codeReviewApproval.evidence.map((row) => row.label)).toEqual([
      "Risk",
      "Description",
      "Review agents",
      "Required checks",
      "Changed",
      "Sensitive paths",
    ]);
  });

  it("uses the available integration logo assets", () => {
    expect(integrations.map((integration) => integration.logo)).toEqual([
      "/integrations/github.svg",
      "/integrations/linear.svg",
      "/integrations/slack.svg",
      "/integrations/sentry.svg",
      "/integrations/pagerduty.svg",
      "/integrations/notion.svg",
      "/integrations/circleci.svg",
      "/integrations/mezmo.svg",
    ]);
  });

  it("lists the supported coding agents with their brand logos", () => {
    expect(codingAgents.map((agent) => `${agent.name}:${agent.logo}`)).toEqual([
      "Codex:/agents/codex.svg",
      "Claude Code:/agents/claude_code.svg",
      "OpenCode:/agents/opencode.svg",
      "Amp:/agents/amp.svg",
      "Pi:/agents/pi.svg",
    ]);
  });

  it("keeps section headers simple and focused", () => {
    expect(platformLayers.map((layer) => layer.heading)).toEqual([
      "Run any coding agent.",
      "Preview every change in the cloud.",
    ]);
  });
});
