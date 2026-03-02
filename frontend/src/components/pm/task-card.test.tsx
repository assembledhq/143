import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { TaskCard } from "./task-card";
import type { PMTask } from "@/lib/types";

describe("TaskCard", () => {
  it("renders task content", () => {
    const task: PMTask = {
      rank: 1,
      issue_ids: ["issue-1"],
      title: "Fix billing timeout",
      reasoning: "High impact",
      approach: "Check handlers/billing.go",
      risk: "Payment flow regression",
      complexity: "moderate",
      confidence: "medium",
      status: "delegated",
    };

    render(<TaskCard task={task} />);

    expect(screen.getByText("Fix billing timeout")).toBeInTheDocument();
    expect(screen.getByText("High impact")).toBeInTheDocument();
    expect(screen.getByText("Check handlers/billing.go")).toBeInTheDocument();
  });
});
