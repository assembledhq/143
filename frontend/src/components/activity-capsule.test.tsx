import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ActivityCapsule } from "./activity-capsule";

describe("ActivityCapsule", () => {
  it("exposes an accessible disclosure and does not mount collapsed children", async () => {
    const onExpandedChange = vi.fn();
    const user = userEvent.setup();
    const { rerender } = render(
      <ActivityCapsule
        activity={{
          id: "phase-1", anchor_id: "aph_phase-1", phase_number: 1, status: "completed", trigger_kind: "initial",
          boundary_reason: "final_response", started_at: "2026-08-03T00:00:00Z",
          completed_at: "2026-08-03T00:00:42Z", tool_call_count: 1, turnNumber: 1,
          entries: [], inferredHistorical: false,
        }}
        expanded={false}
        onExpandedChange={onExpandedChange}
      >
        <div>original activity</div>
      </ActivityCapsule>,
    );

    const trigger = screen.getByRole("button", { name: "Worked for 42s · 1 tool call" });
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    const capsule = trigger.closest("[data-activity-capsule='true']");
    expect(capsule).toHaveClass("mx-2", "min-w-0");
    expect(capsule).not.toHaveClass("rounded-lg", "border", "bg-card");
    expect(screen.queryByText("original activity")).not.toBeInTheDocument();
    await user.click(trigger);
    expect(onExpandedChange).toHaveBeenCalledWith(true);

    rerender(
      <ActivityCapsule
        activity={{
          id: "phase-1", anchor_id: "aph_phase-1", phase_number: 1, status: "completed", trigger_kind: "initial",
          boundary_reason: "final_response", started_at: "2026-08-03T00:00:00Z",
          completed_at: "2026-08-03T00:00:42Z", tool_call_count: 1, turnNumber: 1,
          entries: [], inferredHistorical: false,
        }}
        expanded
        onExpandedChange={onExpandedChange}
      >
        <div>original activity</div>
      </ActivityCapsule>,
    );
    expect(screen.getByText("original activity")).toBeInTheDocument();
    const body = document.querySelector("[data-activity-phase-body='true']");
    expect(body).not.toHaveClass("border-t", "border-border");
  });

  it("reports a non-empty text selection inside expanded activity", () => {
    const onInspect = vi.fn();
    render(
      <ActivityCapsule
        activity={{
          id: "phase-1", anchor_id: "aph_phase-1", phase_number: 1, status: "running", trigger_kind: "initial",
          started_at: "2026-08-03T00:00:00Z", tool_call_count: 0, turnNumber: 1,
          entries: [], inferredHistorical: false,
        }}
        expanded
        onExpandedChange={vi.fn()}
        onInspect={onInspect}
      >
        <div>selectable activity detail</div>
      </ActivityCapsule>,
    );

    const content = screen.getByText("selectable activity detail");
    const range = document.createRange();
    range.selectNodeContents(content);
    const selection = window.getSelection();
    selection?.removeAllRanges();
    selection?.addRange(range);
    fireEvent.mouseUp(content);

    expect(onInspect).toHaveBeenCalledOnce();
    selection?.removeAllRanges();
  });

  it.each([
    { status: "failed", reason: "error", expected: /Failed after 42s.*2 tool calls/ },
    { status: "cancelled", reason: "cancelled", expected: /Cancelled after 42s.*2 tool calls/ },
    { status: "cancelled", reason: "stopped", expected: /Stopped after 42s.*2 tool calls/ },
    { status: "interrupted", reason: "runtime_lost", expected: /Interrupted after 42s.*2 tool calls/ },
  ] as const)("formats the $status/$reason terminal summary", ({ status, reason, expected }) => {
    render(
      <ActivityCapsule
        activity={{
          id: "phase-1", anchor_id: "aph_phase-1", phase_number: 1, status, trigger_kind: "initial",
          boundary_reason: reason, started_at: "2026-08-03T00:00:00Z",
          completed_at: "2026-08-03T00:00:42Z", tool_call_count: 2, turnNumber: 1,
          entries: [], inferredHistorical: false,
        }}
        expanded={false}
        onExpandedChange={vi.fn()}
      >
        <div>activity</div>
      </ActivityCapsule>,
    );

    expect(screen.getByRole("button", { name: expected })).toBeVisible();
  });

  it("shows the latest bounded activity label while running and omits a zero tool count", () => {
    render(
      <ActivityCapsule
        activity={{
          id: "phase-1", anchor_id: "aph_phase-1", phase_number: 1, status: "running", trigger_kind: "initial",
          started_at: new Date(Date.now() - 2_000).toISOString(), tool_call_count: 0, turnNumber: 1,
          entries: [], inferredHistorical: false, latestActivityLabel: "Running typecheck",
        }}
        expanded
        onExpandedChange={vi.fn()}
      >
        <div>activity</div>
      </ActivityCapsule>,
    );

    const trigger = screen.getByRole("button", { name: /Working for.*Running typecheck/ });
    expect(trigger).not.toHaveAccessibleName(/tool call/);
  });

  it("keeps inferred historical activity truthful by omitting duration", () => {
    render(
      <ActivityCapsule
        activity={{
          id: "historical-1", turnNumber: 1, entries: [], toolCallCount: 2, inferredHistorical: true,
        }}
        expanded={false}
        onExpandedChange={vi.fn()}
      >
        <div>activity</div>
      </ActivityCapsule>,
    );

    expect(screen.getByRole("button", { name: "Activity · 2 tool calls" })).toBeVisible();
    expect(screen.getByRole("button")).not.toHaveAccessibleName(/for|after/);
  });
});
