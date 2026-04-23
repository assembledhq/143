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
    agent: "claude_code",
    auth_type: "api_key",
    label: "Claude backup",
    scope: "organization",
    provider: "anthropic",
    status: "never_verified",
    is_default: false,
    usage_note: "sk-ant...1234",
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
        onMoveToTop={vi.fn()}
      />,
    );

    expect(screen.getByText("Team seat A")).toBeInTheDocument();
    expect(screen.getByText("Default")).toBeInTheDocument();
    expect(screen.getByText("Never verified")).toBeInTheDocument();
  });

  it("supports keyboard-accessible move controls", async () => {
    const user = userEvent.setup();
    const onMove = vi.fn();
    const onMoveToTop = vi.fn();

    renderWithProviders(
      <CodingAuthStack
        rows={rows}
        selectedId={null}
        onSelect={vi.fn()}
        onMove={onMove}
        onMoveToTop={onMoveToTop}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Move Claude backup up" }));
    expect(onMove).toHaveBeenCalledWith("auth-2", "up");

    await user.click(screen.getByRole("button", { name: "Move Claude backup to top" }));
    expect(onMoveToTop).toHaveBeenCalledWith("auth-2");
  });
});
