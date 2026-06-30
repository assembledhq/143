import { describe, expect, it } from "vitest";
import * as landingCopy from "./landing-copy";
import {
  agentChoiceHighlights,
  codingAgents,
  integrations,
  platformLayers,
} from "./landing-copy";

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

  it("positions model flexibility as a supporting coding-agent feature", () => {
    expect(agentChoiceHighlights.map((highlight) => highlight.title)).toEqual([
      "Use the best agent for the job",
      "Keep routine work economical",
      "Stack subscriptions before metered spend",
    ]);
    expect(agentChoiceHighlights.map((highlight) => highlight.body)).toEqual([
      "Run top-tier tools like Codex, Claude Code, and OpenCode when the task needs maximum capability.",
      "Route lighter jobs through OpenCode and open-source models when cost matters more than peak reasoning.",
      "Layer personal, team, and bundled coding-agent subscriptions so available seats are used before extra usage piles up.",
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
