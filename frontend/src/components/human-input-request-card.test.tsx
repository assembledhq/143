import { describe, expect, it, vi } from "vitest";

import type { HumanInputRequest } from "@/lib/types";
import { fireEvent, renderWithProviders, screen, userEvent } from "@/test/test-utils";

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
  it("renders the collapsed approval request as a full-width checkpoint", () => {
    const onAnswer = vi.fn();
    const { container } = renderWithProviders(
      <HumanInputRequestCard request={baseRequest} onAnswer={onAnswer} />,
    );

    expect(container.firstElementChild).toHaveClass("w-full");
    expect(container.querySelector('[data-slot="card"]')).toHaveClass("w-full");
    expect(container.querySelector('[data-slot="card"]')).not.toHaveClass(
      "max-w-2xl",
    );
  });

  it("requires an explicit decision before submitting tool approval requests", async () => {
    const onAnswer = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(
      <HumanInputRequestCard
        request={baseRequest}
        autoOpen
        onAnswer={onAnswer}
      />,
    );

    await user.type(screen.getByPlaceholderText("Type your answer..."), "please be careful");

    expect(screen.getByRole("button", { name: "Submit answer" })).toBeDisabled();
    expect(onAnswer).not.toHaveBeenCalled();
  });

  it("keeps response controls disabled until the checkpoint is answerable", () => {
    const onAnswer = vi.fn();

    renderWithProviders(
      <HumanInputRequestCard
        request={baseRequest}
        autoOpen
        answerable={false}
        onAnswer={onAnswer}
      />,
    );

    expect(screen.getByRole("button", { name: "Respond" })).toBeDisabled();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

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

  it("presents approval choices as large selectable button tiles", async () => {
    const onAnswer = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(
      <HumanInputRequestCard request={baseRequest} onAnswer={onAnswer} />,
    );

    const denyTile = screen.getByTestId("human-input-choice-deny");
    expect(denyTile).toHaveClass("min-h-14");
    expect(denyTile).not.toHaveClass("border-primary");
    expect(screen.getByRole("button", { name: "Respond" })).toBeDisabled();

    await user.click(denyTile);

    expect(denyTile).toHaveClass("border-primary");
    expect(screen.getByRole("button", { name: "Respond" })).toBeEnabled();
  });

  it("opens the full response dialog when additional choices are hidden", async () => {
    const onAnswer = vi.fn();
    const user = userEvent.setup();
    const request: HumanInputRequest = {
      ...baseRequest,
      choices: [
        { id: "approve", label: "Approve", kind: "positive" },
        { id: "deny", label: "Deny", kind: "negative" },
        { id: "inspect", label: "Inspect first", kind: "neutral" },
        { id: "edit_command", label: "Edit command", kind: "edit" },
      ],
      response_schema: {
        type: "object",
        required: ["decision"],
        properties: {
          decision: {
            type: "string",
            enum: ["approve", "deny", "inspect", "edit_command"],
          },
        },
      },
    };

    renderWithProviders(
      <HumanInputRequestCard request={request} onAnswer={onAnswer} />,
    );

    expect(screen.queryByTestId("human-input-choice-edit_command")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Respond" })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "Respond" }));

    expect(screen.getByRole("radio", { name: "Edit command" })).toBeInTheDocument();
  });

  it("lets users provide structured edit payloads for edit-style choices", async () => {
    const onAnswer = vi.fn();
    const user = userEvent.setup();
    const request: HumanInputRequest = {
      ...baseRequest,
      choices: [
        {
          id: "edit_command",
          label: "Edit command",
          kind: "edit",
          preview: "npm test",
        },
        { id: "approve", label: "Run as-is", kind: "positive" },
        { id: "deny", label: "Deny", kind: "negative" },
      ],
      response_schema: {
        type: "object",
        required: ["decision", "edited_command"],
        properties: {
          decision: {
            type: "string",
            enum: ["edit_command", "approve", "deny"],
          },
          edited_command: { type: "string" },
        },
      },
    };

    renderWithProviders(
      <HumanInputRequestCard request={request} autoOpen onAnswer={onAnswer} />,
    );

    await user.click(screen.getByRole("radio", { name: "Edit command" }));
    fireEvent.change(screen.getByLabelText("Structured response payload"), {
      target: { value: '{"edited_command":"npm test -- --watch=false"}' },
    });
    await user.click(screen.getByRole("button", { name: "Submit answer" }));

    expect(onAnswer).toHaveBeenCalledWith({
      answer_text: undefined,
      selected_choice_ids: ["edit_command"],
      answer_payload: {
        decision: "edit_command",
        edited_command: "npm test -- --watch=false",
      },
    });
  });
});
