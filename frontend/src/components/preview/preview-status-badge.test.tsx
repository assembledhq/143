import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { PreviewStatusBadge } from "./preview-status-badge";

describe("PreviewStatusBadge", () => {
  it("uses indeterminate progress only while a preview is starting", () => {
    render(<PreviewStatusBadge status="starting" />);

    expect(screen.getByText("Starting"), "starting should have explicit copy").toBeInTheDocument();
    expect(document.querySelector('[data-activity="indeterminate"]'), "starting should use short-operation progress").toBeInTheDocument();
  });

  it("keeps capacity blockers static and actionable", () => {
    render(<PreviewStatusBadge status="capacity_blocked" />);

    expect(screen.getByText("Capacity Blocked"), "capacity should have explicit blocker copy").toBeInTheDocument();
    expect(document.querySelector('[data-activity="none"]'), "blocked previews should not look like active work").toBeInTheDocument();
  });
});
