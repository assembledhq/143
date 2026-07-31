import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import CodeReviewSection from "./code-review-section";
import { landingTypography } from "./landing-typography";

describe("CodeReviewSection", () => {
  it("leads the homepage story with automated code review", () => {
    render(<CodeReviewSection isDark={false} />);

    const heading = screen.getByRole("heading", {
      level: 2,
      name: "Code review that approves the pull requests it should.",
    });

    expect(heading.className).toContain(landingTypography.sectionTitle);
    expect(screen.getByText("01 Code review")).toBeInTheDocument();
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

  it("keeps the escalation path visible alongside the approval", () => {
    render(<CodeReviewSection isDark={false} />);

    expect(
      screen.getByText("143 Code Reviewer did not approve this PR"),
    ).toBeInTheDocument();
    expect(screen.getByText("Needs human review")).toBeInTheDocument();
    expect(screen.getByText(/Auth-sensitive paths changed/)).toBeInTheDocument();
  });

  it("lists the outcomes as supporting bullets instead of extra cards", () => {
    render(<CodeReviewSection isDark={false} />);

    expect(
      screen.getByText(/Reviews finish in minutes, not days/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Policy is enforced the same way on every pull request/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Hard feedback lands without the interpersonal cost/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Acceptable-risk changes merge on the spot/),
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
