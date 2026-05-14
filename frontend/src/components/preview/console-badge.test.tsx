import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, waitFor } from "@/test/test-utils";
import { ConsoleBadge } from "./console-badge";

const { consoleGetMock } = vi.hoisted(() => ({
  consoleGetMock: vi.fn().mockResolvedValue([]),
}));

vi.mock("@/lib/api", () => ({
  api: {
    sessions: {
      preview: {
        console: consoleGetMock,
      },
    },
  },
}));

describe("ConsoleBadge", () => {
  beforeEach(() => {
    consoleGetMock.mockClear();
  });

  it("renders nothing when there are no messages", async () => {
    consoleGetMock.mockResolvedValue([]);
    const { container } = renderWithProviders(
      <ConsoleBadge sessionId="sess-1" />
    );

    // Wait for query to resolve, then check it renders nothing
    await waitFor(() => {
      expect(consoleGetMock).toHaveBeenCalledWith("sess-1");
    });
    // With empty messages, component returns null
    expect(container.innerHTML).toBe("");
  });

  it("treats a null console payload as no messages", async () => {
    consoleGetMock.mockResolvedValue(null);
    const { container } = renderWithProviders(
      <ConsoleBadge sessionId="sess-1" />
    );

    await waitFor(() => {
      expect(consoleGetMock).toHaveBeenCalledWith("sess-1");
    });
    expect(container.innerHTML).toBe("");
  });

  it("shows error badge when errors are present", async () => {
    consoleGetMock.mockResolvedValue([
      { level: "error", text: "TypeError: x is not a function", source: "app.js", line: 42, timestamp: new Date().toISOString() },
      { level: "error", text: "ReferenceError: y is undefined", source: "app.js", line: 50, timestamp: new Date().toISOString() },
    ]);
    renderWithProviders(<ConsoleBadge sessionId="sess-1" />);

    await waitFor(() => {
      expect(screen.getByText("2 errors")).toBeInTheDocument();
    });
  });

  it("shows single error text without plural", async () => {
    consoleGetMock.mockResolvedValue([
      { level: "error", text: "TypeError", source: "app.js", line: 1, timestamp: new Date().toISOString() },
    ]);
    renderWithProviders(<ConsoleBadge sessionId="sess-1" />);

    await waitFor(() => {
      expect(screen.getByText("1 error")).toBeInTheDocument();
    });
  });

  it("shows warning badge when only warnings are present", async () => {
    consoleGetMock.mockResolvedValue([
      { level: "warning", text: "Deprecation warning", source: "lib.js", line_no: 10, time: new Date().toISOString() },
    ]);
    renderWithProviders(<ConsoleBadge sessionId="sess-1" />);

    await waitFor(() => {
      expect(screen.getByText("1 warning")).toBeInTheDocument();
    });
  });
});
