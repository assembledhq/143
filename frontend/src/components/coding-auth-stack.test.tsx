import { describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import type { CodingAuth } from "@/lib/types";
import { CodingAuthStack } from "./coding-auth-stack";

const rows: CodingAuth[] = [
  {
    id: "auth-1",
    org_id: "org-1",
    priority: 1,
    agent: "codex",
    auth_type: "subscription",
    label: "Team seat A",
    scope: "organization",
    provider: "openai_chatgpt",
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
    scope: "organization",
    provider: "pi",
    status: "never_verified",
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

    expect(screen.getByText("Team seat A")).toBeInTheDocument();
    expect(screen.getByText("Default")).toBeInTheDocument();
    expect(screen.getByText("Never verified")).toBeInTheDocument();
    expect(screen.getByText("Pi")).toBeInTheDocument();
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

    await user.click(screen.getByRole("button", { name: "Move Pi backup up" }));
    expect(onMove).toHaveBeenCalledWith("auth-2", "up");

    await user.click(screen.getByRole("button", { name: "Edit Pi backup" }));
    expect(onSelect).toHaveBeenCalledWith("auth-2");
  });
});
