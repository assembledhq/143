import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ReviewLoopStartupNotice } from "./review-loop-startup-notice";

describe("ReviewLoopStartupNotice", () => {
  it.each([
    { state: "accepted", title: "Review startup accepted", toneClass: "text-success" },
    { state: "cancelled", title: "Review startup cancelled", toneClass: "text-warning" },
    { state: "attention", title: "Review startup needs attention", toneClass: "text-attention" },
    { state: "refused", title: "Review startup was refused", toneClass: "text-attention" },
    { state: "pending", title: "Review created; waiting to start", toneClass: "text-info" },
  ] as const)("uses the standard operational tone for $state startup", ({ state, title, toneClass }) => {
    render(<ReviewLoopStartupNotice state={state} />);

    expect(screen.getByText(title)).toHaveClass(toneClass);
  });

  it("renders guidance as body copy and announces status changes", () => {
    render(<ReviewLoopStartupNotice state="unknown" />);

    expect(screen.getByRole("status")).toHaveTextContent("Review created; confirming startup");
    expect(screen.getByText(/Starting another review could duplicate the request/)).toHaveClass("text-sm");
  });
});
