import { describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import type { CodingCredentialSummary } from "@/lib/types";
import { CodingAuthStack } from "./coding-auth-stack";

const rows: CodingCredentialSummary[] = [
  {
    id: "auth-1",
    org_id: "org-1",
    priority: 1,
    agent: "codex",
    auth_type: "subscription",
    label: "Team seat A",
    scope: "org",
    provider: "openai_subscription",
    status: "healthy",
    is_default: true,
    usage_note: "ChatGPT Plus",
    created_at: "2026-04-22T10:00:00Z",
    updated_at: "2026-04-22T10:00:00Z",
  },
  {
    id: "auth-2",
    org_id: "org-1",
    priority: 2,
    agent: "pi",
    auth_type: "api_key",
    label: "Pi backup",
    scope: "org",
    provider: "pi",
    status: "invalid",
    is_default: false,
    usage_note: "pi_12...cdef",
    created_at: "2026-04-22T10:00:00Z",
    updated_at: "2026-04-22T10:00:00Z",
  },
];

describe("CodingAuthStack", () => {
  it("renders the stack with a visible default badge", () => {
    renderWithProviders(
      <CodingAuthStack
        rows={rows}
        selectedId={null}
        onSelect={vi.fn()}
        onMove={vi.fn()}
        onReorder={vi.fn()}
      />,
    );

    expect(screen.getAllByText("Team seat A").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Default").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Invalid").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Pi").length).toBeGreaterThan(0);
  });

  it("supports keyboard-accessible move controls", async () => {
    const user = userEvent.setup();
    const onMove = vi.fn();
    const onSelect = vi.fn();

    renderWithProviders(
      <CodingAuthStack
        rows={rows}
        selectedId={null}
        onSelect={onSelect}
        onMove={onMove}
        onReorder={vi.fn()}
      />,
    );

    await user.click(screen.getAllByRole("button", { name: "Move Pi backup up" })[0]);
    expect(onMove).toHaveBeenCalledWith("auth-2", "up");

    await user.click(screen.getAllByRole("button", { name: "Edit Pi backup" })[0]);
    expect(onSelect).toHaveBeenCalledWith("auth-2");
  });

  it("renders compact mobile cards with inline metadata labels", () => {
    renderWithProviders(
      <CodingAuthStack
        rows={rows}
        selectedId={null}
        onSelect={vi.fn()}
        onMove={vi.fn()}
        onReorder={vi.fn()}
      />,
    );

    expect(screen.getAllByText("Priority").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Auth type").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Status").length).toBeGreaterThan(0);
    expect(screen.getAllByText("ChatGPT Plus").length).toBeGreaterThan(0);
  });
});
