import { afterEach, describe, expect, it, vi } from "vitest";
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
  afterEach(() => vi.restoreAllMocks());

  it.each([
    { resetAt: "2026-09-19T13:35:55Z", expected: "Available again Sep 19, 2026, 9:35 AM EDT" },
    { resetAt: "2026-12-16T14:35:00Z", expected: "Available again Dec 16, 2026, 9:35 AM EST" },
    { resetAt: "2026-09-20T01:35:00Z", expected: "Available again Sep 19, 2026, 9:35 PM EDT" },
  ])("shows the local date, time, and timezone for $resetAt on desktop and mobile", ({ resetAt, expected }) => {
    const formatTime = Date.prototype.toLocaleString;
    // Keep the displayed result deterministic while exercising the component's
    // formatting options through Intl, including daylight-saving time.
    vi.spyOn(Date.prototype, "toLocaleString").mockImplementation(function (this: Date, _locales, options) {
      return formatTime.call(this, "en-US", { ...options, timeZone: "America/New_York" });
    });
    renderWithProviders(
      <CodingAuthStack
        rows={[{ ...rows[0], status: "rate_limited", rate_limited_until: resetAt }]}
        selectedId={null}
        onSelect={vi.fn()}
        onMove={vi.fn()}
        onReorder={vi.fn()}
      />,
    );

    expect(screen.getAllByText(expected)).toHaveLength(2);
  });

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
