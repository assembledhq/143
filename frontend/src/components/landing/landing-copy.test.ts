import { describe, expect, it } from "vitest";
import * as landingCopy from "./landing-copy";
import {
  agentChoiceHighlights,
  codeReviewApproval,
  codeReviewEscalation,
  codeReviewOutcomes,
  codeReviewSummary,
  reviewBottlenecks,
  codingAgents,
  integrations,
  platformLayers,
} from "./landing-copy";

describe("landing copy", () => {
  it("keeps hero copy focused instead of exposing summary cards", () => {
    expect("heroMetrics" in landingCopy).toBe(false);
  });

  it("numbers platform layers after the code-review and why-this-matters sections", () => {
    expect(platformLayers.map((layer) => `${layer.step} ${layer.title}`)).toEqual([
      "03 Team context",
      "04 Cloud execution",
      "05 Repair loops",
      "06 Cloud previews",
    ]);
  });

  it("opens the homepage story with code review", () => {
    expect(`${codeReviewSummary.step} ${codeReviewSummary.kicker}`).toBe(
      "01 Code review",
    );
    expect(codeReviewSummary.heading).toBe(
      "Code review that approves the pull requests it should.",
    );
  });

  it("names the review bottlenecks agent-written code creates", () => {
    expect(reviewBottlenecks).toEqual([
      "More pull requests than review capacity",
      "Larger, less cohesive agent-written changes",
      "More authors who don't review code for a living",
      "Stacks stuck in review for days while generation sprints ahead",
    ]);
  });

  it("answers each bottleneck with a code review outcome", () => {
    expect(codeReviewOutcomes).toEqual([
      "Reviews finish in minutes, not days",
      "Policy is enforced the same way on every pull request",
      "Hard feedback lands without the interpersonal cost",
      "Acceptable-risk changes merge on the spot",
    ]);
    expect(codeReviewOutcomes).toHaveLength(reviewBottlenecks.length);
  });

  it("shows both the auto-approval and the escalation path", () => {
    expect(codeReviewApproval.decision).toBe("Approved");
    expect(codeReviewEscalation.decision).toBe("Needs human review");
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
      "Arrive at review already clean.",
      "Preview every change in the cloud.",
    ]);
  });
});
