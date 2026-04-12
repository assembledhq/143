import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, waitFor } from "@/test/test-utils";
import { TTLWarning } from "./ttl-warning";

const { extendMock } = vi.hoisted(() => ({
  extendMock: vi.fn().mockResolvedValue({ data: {} }),
}));

vi.mock("@/lib/api", () => ({
  api: {
    sessions: {
      preview: {
        extend: extendMock,
      },
    },
  },
}));

describe("TTLWarning", () => {
  beforeEach(() => {
    extendMock.mockClear();
  });

  it("renders nothing when more than 5 minutes remain", () => {
    const future = new Date(Date.now() + 30 * 60 * 1000).toISOString(); // 30 min
    const { container } = renderWithProviders(
      <TTLWarning expiresAt={future} sessionId="sess-1" />
    );
    expect(container.innerHTML).toBe("");
  });

  it("renders nothing when more than 1 hour remains", () => {
    const future = new Date(Date.now() + 2 * 60 * 60 * 1000).toISOString();
    const { container } = renderWithProviders(
      <TTLWarning expiresAt={future} sessionId="sess-1" />
    );
    expect(container.innerHTML).toBe("");
  });

  it("shows urgent warning when less than 5 minutes remain", async () => {
    const soon = new Date(Date.now() + 3 * 60 * 1000).toISOString(); // 3 min
    renderWithProviders(
      <TTLWarning expiresAt={soon} sessionId="sess-1" />
    );

    await waitFor(() => {
      expect(screen.getByText(/Expires in/)).toBeInTheDocument();
    });
  });

  it("shows seconds-only when less than 1 minute remains", async () => {
    const vSoon = new Date(Date.now() + 30 * 1000).toISOString(); // 30 sec
    renderWithProviders(
      <TTLWarning expiresAt={vSoon} sessionId="sess-1" />
    );

    await waitFor(() => {
      expect(screen.getByText(/Expires in \d+s/)).toBeInTheDocument();
    });
  });

  it('shows "Preview expired" when time has passed', async () => {
    const past = new Date(Date.now() - 60000).toISOString();
    renderWithProviders(
      <TTLWarning expiresAt={past} sessionId="sess-1" />
    );

    await waitFor(() => {
      expect(screen.getByText("Preview expired")).toBeInTheDocument();
    });
  });

  it("shows extend button when not yet expired", async () => {
    const soon = new Date(Date.now() + 2 * 60 * 1000).toISOString();
    renderWithProviders(
      <TTLWarning expiresAt={soon} sessionId="sess-1" />
    );

    await waitFor(() => {
      expect(screen.getByText("Extend")).toBeInTheDocument();
    });
  });

  it("hides extend button when expired", async () => {
    const past = new Date(Date.now() - 60000).toISOString();
    renderWithProviders(
      <TTLWarning expiresAt={past} sessionId="sess-1" />
    );

    await waitFor(() => {
      expect(screen.getByText("Preview expired")).toBeInTheDocument();
    });
    expect(screen.queryByText("Extend")).not.toBeInTheDocument();
  });
});
