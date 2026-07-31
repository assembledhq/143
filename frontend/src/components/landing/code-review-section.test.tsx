import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import CodeReviewSection from "./code-review-section";
import { landingTypography } from "./landing-typography";

describe("CodeReviewSection", () => {
  it("leads the homepage story with code review and its bottleneck", () => {
    render(<CodeReviewSection isDark={false} />);

    const heading = screen.getByRole("heading", {
      level: 2,
      name: "Code review that can auto-approve.",
    });

    expect(heading.className).toContain(landingTypography.sectionTitle);
    expect(screen.getByText("01 Code review")).toBeInTheDocument();
    expect(
      screen.getByText(/getting it reviewed takes days/),
    ).toBeInTheDocument();
  });

  it("shows the auto-approval verdict with the evidence behind it", () => {
    render(<CodeReviewSection isDark={false} />);

    expect(
      screen.getByText("143 Code Reviewer approved this PR"),
    ).toBeInTheDocument();
    expect(screen.getByText("Approved")).toBeInTheDocument();
    expect(screen.getByText("Codex clean, Claude Code clean")).toBeInTheDocument();
    expect(screen.getByText(/Policy v12/)).toBeInTheDocument();
  });

  it("shows only the approved verdict card", () => {
    render(<CodeReviewSection isDark={false} />);

    expect(
      screen.queryByText("143 Code Reviewer did not approve this PR"),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("Needs human review")).not.toBeInTheDocument();
  });

  it("focuses the copy column on policy tuning and reviewer model choice", () => {
    render(<CodeReviewSection isDark={false} />);

    expect(
      screen.getByRole("heading", {
        level: 3,
        name: "Tune the policy. Pick the reviewer models.",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/The reviewers are coding agents, not people/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Choose reviewer models: Codex, Claude Code, OpenCode/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Set reasoning depth per reviewer to control cost/),
    ).toBeInTheDocument();
    expect(screen.queryAllByRole("heading", { level: 3 })).toHaveLength(1);
  });

  it("links to the code review policy guide", () => {
    render(<CodeReviewSection isDark={false} />);

    expect(
      screen.getByRole("link", { name: /configure the review policy/i }),
    ).toHaveAttribute("href", "/docs/guides/code-review-policy");
  });
});
