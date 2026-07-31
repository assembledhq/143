import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import CodeReviewSection from "./code-review-section";
import { landingTypography } from "./landing-typography";

describe("CodeReviewSection", () => {
  it("leads with the review bottleneck, then automated code review", () => {
    render(<CodeReviewSection isDark={false} />);

    const problemHeading = screen.getByRole("heading", {
      level: 2,
      name: "Agents made code faster to write. Review is where it piles up.",
    });
    const answerHeading = screen.getByRole("heading", {
      level: 2,
      name: "Code review that approves the pull requests it should.",
    });

    expect(problemHeading.className).toContain(landingTypography.sectionTitle);
    expect(answerHeading.className).toContain(landingTypography.sectionTitle);
    expect(
      problemHeading.compareDocumentPosition(answerHeading) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(screen.getByText("01 Why this matters")).toBeInTheDocument();
    expect(screen.getByText("02 Code review")).toBeInTheDocument();
    expect(
      screen.getByText(/More pull requests than review capacity/),
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

  it("keeps the escalation path visible alongside the approval", () => {
    render(<CodeReviewSection isDark={false} />);

    expect(
      screen.getByText("143 Code Reviewer did not approve this PR"),
    ).toBeInTheDocument();
    expect(screen.getByText("Needs human review")).toBeInTheDocument();
    expect(screen.getByText(/Auth-sensitive paths changed/)).toBeInTheDocument();
  });

  it("focuses the copy column on policy tuning and reviewer choice", () => {
    render(<CodeReviewSection isDark={false} />);

    expect(
      screen.getByRole("heading", {
        level: 3,
        name: "Tune the policy. Pick the reviewers.",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/thresholds, sensitive paths, and required checks/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Run one reviewer agent or several in parallel/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/model and reasoning depth to balance cost and quality/),
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
