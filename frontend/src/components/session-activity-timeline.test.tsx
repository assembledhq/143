import { useRef } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { SessionActivityTimeline } from "./session-activity-timeline";
import type { TimelineEntry } from "@/lib/timeline";
import type { SessionActivityDetail, SessionLog, SessionMessage, SessionTranscriptTurn } from "@/lib/types";

const phaseID = "10000000-0000-0000-0000-000000000001";
const toolUse: SessionLog = {
  id: 1, session_id: "session-1", thread_id: "thread-1", level: "tool_use",
  message: "Run tests", metadata: { type: "tool_use", tool: "shell", input: { command: "npm test" } },
  turn_number: 1, created_at: "2026-08-03T00:00:01Z", message_bytes: 9,
  message_chars: 9, message_truncated: false, activity_phase_id: phaseID,
};
const finalMessage: SessionMessage = {
  id: 2, session_id: "session-1", org_id: "org-1", thread_id: "thread-1",
  turn_number: 1, role: "assistant", content: "Finished safely.",
  created_at: "2026-08-03T00:00:06Z", activity_phase_id: phaseID,
};
const entries: TimelineEntry[] = [
  { kind: "tool_group", toolUse, transcriptEntryId: "tuse_1" },
  { kind: "message", data: finalMessage, transcriptEntryId: "msg_2" },
];

function Harness({ status, detail = "compact", atLiveEdge = true, anchorEntryId, boundaryRendered = true, userScrollEpoch = 0 }: { status: "running" | "completed"; detail?: SessionActivityDetail; atLiveEdge?: boolean; anchorEntryId?: string; boundaryRendered?: boolean; userScrollEpoch?: number }) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const turns: SessionTranscriptTurn[] = [{
    turn_number: 1,
    started_at: "2026-08-03T00:00:00Z",
    entries: [],
    phases: [{
      id: phaseID, anchor_id: `aph_${phaseID}`, phase_number: 1, status, trigger_kind: "initial",
      boundary_reason: status === "completed" ? "final_response" : undefined,
      started_at: "2026-08-03T00:00:00Z",
      completed_at: status === "completed" ? "2026-08-03T00:00:06Z" : undefined,
      tool_call_count: 1,
    }],
  }];
  return (
    <div ref={scrollRef}>
      <SessionActivityTimeline
        entries={boundaryRendered ? entries : entries.slice(0, 1)}
        isRunning={status === "running"}
        turns={turns}
        detailPreference={detail}
        anchorEntryId={anchorEntryId}
        threadID="thread-1"
        scrollContainerRef={scrollRef}
        userScrollEpoch={userScrollEpoch}
        atLiveEdge={atLiveEdge}
      />
    </div>
  );
}

describe("SessionActivityTimeline", () => {
  it("collapses an untouched running phase only after its terminal boundary is rendered", async () => {
    const rendered = render(<Harness status="running" />);
    expect(screen.getByRole("button", { name: /Working for.*1 tool call/ })).toHaveAttribute("aria-expanded", "true");

    rendered.rerender(<Harness status="completed" />);

    await waitFor(() => expect(screen.getByRole("button", { name: /Worked for 6s.*1 tool call/ })).toHaveAttribute("aria-expanded", "false"));
    expect(screen.getByText("Finished safely.")).toBeVisible();
  });

  it("waits for a separately reconciled boundary before collapsing a terminal phase", async () => {
    const rendered = render(<Harness status="running" boundaryRendered={false} />);
    expect(screen.getByRole("button", { name: /Working for.*1 tool call/ })).toHaveAttribute("aria-expanded", "true");

    rendered.rerender(<Harness status="completed" boundaryRendered={false} />);
    await waitFor(() => expect(screen.getByRole("button", { name: /Worked for 6s.*1 tool call/ })).toHaveAttribute("aria-expanded", "true"));
    expect(screen.queryByText("Finished safely.")).not.toBeInTheDocument();

    rendered.rerender(<Harness status="completed" boundaryRendered />);
    await waitFor(() => expect(screen.getByRole("button", { name: /Worked for 6s.*1 tool call/ })).toHaveAttribute("aria-expanded", "false"));
    expect(screen.getByText("Finished safely.")).toBeVisible();
  });

  it("protects a phase after a manual disclosure action", async () => {
    const user = userEvent.setup();
    const rendered = render(<Harness status="running" />);
    const active = screen.getByRole("button", { name: /Working for.*1 tool call/ });
    await user.click(active);
    await user.click(active);

    rendered.rerender(<Harness status="completed" />);

    await waitFor(() => expect(screen.getByRole("button", { name: /Worked for 6s.*1 tool call/ })).toHaveAttribute("aria-expanded", "true"));
  });

  it("protects a phase after opening an individual tool disclosure", async () => {
    const user = userEvent.setup();
    const rendered = render(<Harness status="running" />);
    await user.click(screen.getByRole("button", { name: /Ran `npm test`/ }));

    rendered.rerender(<Harness status="completed" />);

    await waitFor(() => expect(screen.getByRole("button", { name: /Worked for 6s.*1 tool call/ })).toHaveAttribute("aria-expanded", "true"));
  });

  it("protects a phase after 48 pixels remain visible for 250ms during user scrolling", async () => {
    vi.useFakeTimers();
    const timeoutSpy = vi.spyOn(window, "setTimeout");
    const rect = { x: 0, y: 0, top: 0, left: 0, right: 100, bottom: 100, width: 100, height: 100, toJSON: () => ({}) };
    const geometry = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue(rect);
    try {
      const rendered = render(<Harness status="running" atLiveEdge={false} userScrollEpoch={1} />);
      await act(async () => { vi.advanceTimersByTime(251); });
      expect(timeoutSpy.mock.calls.filter(([, delay]) => delay === 250)).toHaveLength(1);

      rendered.rerender(<Harness status="completed" atLiveEdge userScrollEpoch={1} />);

      await act(async () => { vi.runOnlyPendingTimers(); });
      expect(screen.getByRole("button", { name: /Worked for 6s.*1 tool call/ })).toHaveAttribute("aria-expanded", "true");
    } finally {
      geometry.mockRestore();
      timeoutSpy.mockRestore();
      vi.useRealTimers();
    }
  });

  it("keeps an untouched phase mounted when it terminates away from the live edge", async () => {
    const rendered = render(<Harness status="running" atLiveEdge={false} />);
    expect(screen.getByText("Ran `npm test`")).toBeVisible();

    rendered.rerender(<Harness status="completed" atLiveEdge={false} />);

    await waitFor(() => expect(screen.getByRole("button", { name: /Worked for 6s.*1 tool call/ })).toHaveAttribute("aria-expanded", "true"));
    expect(screen.getByText("Ran `npm test`")).toBeVisible();
  });

  it("keeps terminal phases expanded in Detailed mode", () => {
    const { container } = render(<Harness status="completed" detail="detailed" />);
    expect(screen.getByRole("button", { name: /Worked for 6s.*1 tool call/ })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Ran `npm test`")).toBeVisible();
    expect(container.querySelectorAll(".my-4.flex.items-center.gap-3.px-1")).toHaveLength(1);
  });

  it("clears capsule overrides on preference changes and allows a fresh later override", async () => {
    const user = userEvent.setup();
    const rendered = render(<Harness status="completed" detail="compact" />);
    const capsule = screen.getByRole("button", { name: /Worked for 6s.*1 tool call/ });
    expect(capsule).toHaveAttribute("aria-expanded", "false");

    await user.click(capsule);
    expect(capsule).toHaveAttribute("aria-expanded", "true");
    rendered.rerender(<Harness status="completed" detail="detailed" />);
    await waitFor(() => expect(capsule).toHaveAttribute("aria-expanded", "true"));

    await user.click(capsule);
    expect(capsule).toHaveAttribute("aria-expanded", "false");
    rendered.rerender(<Harness status="completed" detail="compact" />);
    await waitFor(() => expect(capsule).toHaveAttribute("aria-expanded", "false"));

    await user.click(capsule);
    expect(capsule).toHaveAttribute("aria-expanded", "true");
  });

  it("mounts a compact terminal phase when a restored anchor targets its child", () => {
    render(<Harness status="completed" anchorEntryId="tuse_1" />);
    expect(screen.getByRole("button", { name: /Worked for 6s.*1 tool call/ })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Ran `npm test`")).toBeVisible();
  });

  it("mounts compact inferred historical activity when a restored anchor targets its child", () => {
    const scrollRef = { current: document.createElement("div") };
    const historicalTool = { ...toolUse, id: 99, activity_phase_id: undefined };
    render(
      <SessionActivityTimeline
        entries={[{ kind: "tool_group", toolUse: historicalTool, transcriptEntryId: "tuse_legacy" }]}
        isRunning={false}
        turns={[{ turn_number: 1, started_at: "2026-08-03T00:00:00Z", entries: [], phases: [] }]}
        detailPreference="compact"
        anchorEntryId="tuse_legacy"
        threadID="thread-1"
        scrollContainerRef={scrollRef}
        userScrollEpoch={0}
        atLiveEdge
      />,
    );
    expect(screen.getByRole("button", { name: /Activity.*1 tool call/ })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Ran `npm test`")).toBeVisible();
  });

  it("emits one day separator for a steering message applied after midnight", () => {
    // Regression: this component groups nodes by presentation time while the
    // nested ChatTimeline grouped by created_at, so a message authored before
    // midnight and applied after produced two stacked separators — the applied
    // day from here, then the authored day from the timeline below it.
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-03T12:00:00Z"));

    const scrollRef = { current: document.createElement("div") };
    const appliedMessage: SessionMessage = {
      id: 42, session_id: "session-1", org_id: "org-1", thread_id: "thread-1",
      turn_number: 1, role: "user", content: "Steering applied after midnight",
      created_at: "2026-08-02T23:59:00Z", applied_at: "2026-08-03T00:00:02Z",
    };
    render(
      <SessionActivityTimeline
        entries={[{ kind: "message", data: appliedMessage, transcriptEntryId: "msg_42" }]}
        isRunning={false}
        turns={[{ turn_number: 1, started_at: "2026-08-03T00:00:00Z", entries: [], phases: [] }]}
        detailPreference="compact"
        threadID="thread-1"
        scrollContainerRef={scrollRef}
        userScrollEpoch={0}
        atLiveEdge
      />,
    );

    expect(screen.getAllByText("Today")).toHaveLength(1);
    expect(screen.queryByText("Yesterday")).not.toBeInTheDocument();

    vi.useRealTimers();
  });
});
