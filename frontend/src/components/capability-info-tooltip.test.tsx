import { describe, expect, it } from "vitest";

import { CapabilityInfoTooltip } from "@/components/capability-info-tooltip";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import type { AgentCapabilityDefinition } from "@/lib/types";

describe("CapabilityInfoTooltip", () => {
  it("shows risk and access without category or all-caps styling", async () => {
    const user = userEvent.setup();
    const definition: AgentCapabilityDefinition = {
      id: "repo_context",
      display_name: "Repository context",
      description: "Code, docs, and repository-local facts.",
      category: "context",
      max_access_level: "read",
      risk: "low",
      scope: "repository",
    };

    renderWithProviders(<CapabilityInfoTooltip definition={definition} />);

    await user.hover(screen.getByRole("button", { name: "About Repository context" }));

    const tooltip = await screen.findByRole("tooltip");
    expect(tooltip).toHaveTextContent("Low risk · Read-only");
    expect(tooltip).not.toHaveTextContent("context");
    for (const footer of screen.getAllByText("Low risk · Read-only")) {
      expect(footer).not.toHaveClass("uppercase");
    }
  });
});
