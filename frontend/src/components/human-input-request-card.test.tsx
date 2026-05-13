import { describe, expect, it, vi } from "vitest";

import type { HumanInputRequest } from "@/lib/types";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";

import { HumanInputRequestCard } from "./human-input-request-card";

const baseRequest: HumanInputRequest = {
  id: "hir-1",
  org_id: "org-1",
  session_id: "session-1",
  turn_number: 2,
  agent_type: "claude_code",
  provider_request_id: "toolu-1",
  request_kind: "tool_approval",
  status: "pending",
  title: "Approve Bash?",
  body: "Claude needs approval before it can continue.",
  choices: [
    { id: "approve", label: "Approve", kind: "positive" },
    { id: "deny", label: "Deny", kind: "negative" },
  ],
  response_schema: {
    type: "object",
    required: ["decision"],
    properties: {
      decision: { type: "string", enum: ["approve", "deny"] },
    },
  },
  created_at: "2026-01-01T00:00:00Z",
};

describe("HumanInputRequestCard", () => {
  it("submits tool approval decisions as structured answer payloads", async () => {
    const onAnswer = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(
      <HumanInputRequestCard
        request={baseRequest}
        autoOpen
        onAnswer={onAnswer}
      />,
    );

    await user.click(screen.getByRole("radio", { name: "Deny" }));
    await user.click(screen.getByRole("button", { name: "Submit answer" }));

    expect(onAnswer).toHaveBeenCalledWith({
      answer_text: undefined,
      selected_choice_ids: ["deny"],
      answer_payload: { decision: "deny" },
    });
  });
});
