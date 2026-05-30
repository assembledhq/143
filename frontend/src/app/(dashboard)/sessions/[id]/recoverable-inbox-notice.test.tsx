import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";

import { RecoverableInboxNotice } from "./recoverable-inbox-notice";
import { renderWithProviders, userEvent } from "@/test/test-utils";
import type { ThreadInboxEntry, ThreadInboxDeliverySummary } from "@/lib/types";

function makeSummary(overrides: Partial<ThreadInboxDeliverySummary> = {}): ThreadInboxDeliverySummary {
  return {
    thread_id: "thread-1",
    state: "dead_letter",
    pending_count: 0,
    delivering_count: 0,
    delivered_count: 0,
    unknown_delivery_count: 1,
    acked_count: 3,
    dead_letter_count: 1,
    last_sequence_no: 14,
    last_error: "payload serialization failed",
    ...overrides,
  };
}

function makeEntry(overrides: Partial<ThreadInboxEntry> = {}): ThreadInboxEntry {
  return {
    id: "entry-1",
    org_id: "org-1",
    session_id: "session-1",
    thread_id: "thread-1",
    sequence_no: 14,
    message_id: 42,
    entry_type: "user_message",
    payload: { content: "Please continue from the previous change." },
    delivery_state: "dead_letter",
    delivery_attempts: 2,
    last_error: "payload serialization failed",
    accepted_at: "2026-05-26T10:00:00Z",
    created_at: "2026-05-26T10:00:00Z",
    ...overrides,
  };
}

describe("RecoverableInboxNotice", () => {
  it("shows failed and uncertain delivery entries with retry actions", async () => {
    const user = userEvent.setup();
    const onRetryEntry = vi.fn();
    const onRetryAll = vi.fn();

    renderWithProviders(
      <RecoverableInboxNotice
        summary={makeSummary()}
        entries={[makeEntry()]}
        isLoading={false}
        isRetrying={false}
        onRetryEntry={onRetryEntry}
        onRetryAll={onRetryAll}
      />,
    );

    expect(screen.getByText("Message delivery needs attention")).toBeInTheDocument();
    expect(screen.getByText("1 failed")).toBeInTheDocument();
    expect(screen.getByText("1 uncertain")).toBeInTheDocument();
    expect(screen.getByText("Please continue from the previous change.")).toBeInTheDocument();
    expect(screen.getByText("payload serialization failed")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Retry entry 14" }));
    await user.click(screen.getByRole("button", { name: "Retry all failed messages" }));

    expect(onRetryEntry).toHaveBeenCalledWith("entry-1", false);
    expect(onRetryAll).toHaveBeenCalledTimes(1);
  });

  it("uses the summary counts while recoverable entries are loading", () => {
    renderWithProviders(
      <RecoverableInboxNotice
        summary={makeSummary({ dead_letter_count: 2, unknown_delivery_count: 0 })}
        entries={[]}
        isLoading={true}
        isRetrying={false}
        onRetryEntry={vi.fn()}
        onRetryAll={vi.fn()}
      />,
    );

    expect(screen.getByText("2 failed")).toBeInTheDocument();
    expect(screen.getByText("Loading recoverable messages...")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry all failed messages" })).toBeDisabled();
  });

  it("does not include uncertain entries in retry all", async () => {
    const user = userEvent.setup();
    const onRetryAll = vi.fn();

    renderWithProviders(
      <RecoverableInboxNotice
        summary={makeSummary({ dead_letter_count: 0, unknown_delivery_count: 1 })}
        entries={[makeEntry({ id: "entry-uncertain", delivery_state: "unknown_delivery" })]}
        isLoading={false}
        isRetrying={false}
        onRetryEntry={vi.fn()}
        onRetryAll={onRetryAll}
      />,
    );

    expect(screen.getByRole("button", { name: "Retry all failed messages" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Replay entry 14" })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "Retry all failed messages" }));
    expect(onRetryAll).not.toHaveBeenCalled();
  });

  it("requires confirmation before replaying an uncertain entry", async () => {
    const user = userEvent.setup();
    const onRetryEntry = vi.fn();

    renderWithProviders(
      <RecoverableInboxNotice
        summary={makeSummary({ dead_letter_count: 0, unknown_delivery_count: 1 })}
        entries={[makeEntry({ id: "entry-uncertain", delivery_state: "unknown_delivery" })]}
        isLoading={false}
        isRetrying={false}
        onRetryEntry={onRetryEntry}
        onRetryAll={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Replay entry 14" }));
    // The replay action is gated on a confirmation dialog, not a native
    // window.confirm, so the click alone does not fire onRetryEntry.
    expect(onRetryEntry).not.toHaveBeenCalled();
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Replay" }));
    expect(onRetryEntry).toHaveBeenCalledWith("entry-uncertain", true);
  });

  it("does not replay when the confirmation dialog is cancelled", async () => {
    const user = userEvent.setup();
    const onRetryEntry = vi.fn();

    renderWithProviders(
      <RecoverableInboxNotice
        summary={makeSummary({ dead_letter_count: 0, unknown_delivery_count: 1 })}
        entries={[makeEntry({ id: "entry-uncertain", delivery_state: "unknown_delivery" })]}
        isLoading={false}
        isRetrying={false}
        onRetryEntry={onRetryEntry}
        onRetryAll={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Replay entry 14" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onRetryEntry).not.toHaveBeenCalled();
  });
});
