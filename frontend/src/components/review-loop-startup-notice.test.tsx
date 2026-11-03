import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ReviewLoopStartupNotice } from "./review-loop-startup-notice";

describe("ReviewLoopStartupNotice", () => {
  it.each([
    { state: "accepted" as const, title: "Review startup accepted", tone: "text-success" },
    { state: "attention" as const, title: "Review startup needs attention", tone: "text-attention" },
    { state: "refused" as const, title: "Review startup was refused", tone: "text-attention" },
    { state: "cancelled" as const, title: "Review startup cancelled", tone: "text-muted-foreground" },
    { state: "unknown" as const, title: "Review created; confirming startup", tone: "text-info" },
    { state: null, title: "Review creation is awaiting confirmation", tone: "text-info" },
  ])("uses the semantic status tone for $state startup", ({ state, title, tone }) => {
    render(<ReviewLoopStartupNotice state={state} />);

    expect(screen.getByText(title)).toHaveClass(tone);
  });
});
