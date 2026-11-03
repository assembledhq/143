import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { ReviewLoopAdmissionState } from "@/lib/types";

import { ReviewLoopStartupNotice } from "./review-loop-startup-notice";

describe("ReviewLoopStartupNotice", () => {
  it.each([
    { state: "pending", title: "Review created; waiting to start", tone: "info" },
    { state: "attention", title: "Review startup needs attention", tone: "attention" },
    { state: "refused", title: "Review startup was refused", tone: "attention" },
    { state: "cancelled", title: "Review startup cancelled", tone: "neutral" },
    { state: "accepted", title: "Review startup accepted", tone: "info" },
  ] satisfies Array<{ state: ReviewLoopAdmissionState; title: string; tone: string }>)(
    "uses the shared $tone treatment for $state startup",
    ({ state, title, tone }) => {
      render(<ReviewLoopStartupNotice state={state} />);

      const status = screen.getByRole("status");
      expect(status).toBeInTheDocument();
      expect(screen.getByText(title)).toBeInTheDocument();
      expect(status.querySelector(".font-medium")).toHaveClass(
        tone === "neutral" ? "text-muted-foreground" : `text-${tone}`,
      );
    },
  );
});
