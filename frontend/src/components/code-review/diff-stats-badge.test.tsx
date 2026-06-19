import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { DiffStatsBadge } from "./diff-stats-badge";

describe("DiffStatsBadge", () => {
  it("renders additions with the semantic success token", () => {
    render(<DiffStatsBadge added={150} removed={49} />);

    expect(screen.getByText("+150"), "additions should align with the PR-ready success color").toHaveClass("text-success");
    expect(screen.getByText("-49"), "removals should keep the destructive red treatment").toHaveClass("text-red-600", "dark:text-red-400");
  });
});
